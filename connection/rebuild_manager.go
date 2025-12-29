package connection

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charlesren/ylog"
)

// RebuildManager 重建管理器
type RebuildManager struct {
	// 配置参数
	config *EnhancedConnectionConfig

	// 指标收集器
	collector MetricsCollector

	// 事件通道
	eventChan chan<- PoolEvent
}

// NewRebuildManager 创建新的重建管理器
func NewRebuildManager(config *EnhancedConnectionConfig, collector MetricsCollector, eventChan chan<- PoolEvent) *RebuildManager {
	return &RebuildManager{
		config:    config,
		collector: collector,
		eventChan: eventChan,
	}
}

// EvaluateAndMark 评估并标记连接需要重建（原子操作）
// 这是评估连接是否需要重建的统一入口，确保评估和标记的原子性
// 返回值: (是否标记成功, 原因)
func (rm *RebuildManager) EvaluateAndMark(conn *EnhancedPooledConnection) (bool, string) {
	// 如果已经标记，直接返回当前原因
	if conn.isMarkedForRebuild() {
		reason := conn.getRebuildReason()
		ylog.Debugf("rebuild_manager", "连接已标记为需要重建，跳过评估: id=%s, reason=%s", conn.id, reason)
		return false, reason
	}

	// 检查基本条件
	if !rm.checkRebuildEnabled() {
		return false, "rebuild_disabled"
	}

	now := time.Now()

	// 检查重建间隔
	if !rm.checkRebuildInterval(conn, now) {
		return false, "interval_too_short"
	}

	// 检查连接状态
	if !rm.checkConnectionState(conn) {
		return false, "invalid_state"
	}

	// 检查健康状态（不健康更应重建，所以这里只检查状态）
	// 注意：这里不跳过不健康的连接，因为它们更需要重建

	// 根据策略评估并标记
	return rm.evaluateByStrategyAndMark(conn, now)
}

// evaluateByStrategyAndMark 根据策略评估并标记连接
func (rm *RebuildManager) evaluateByStrategyAndMark(conn *EnhancedPooledConnection, now time.Time) (bool, string) {
	switch RebuildStrategy(rm.config.RebuildStrategy) {
	case RebuildStrategyUsage:
		return rm.evaluateUsageAndMark(conn)
	case RebuildStrategyAge:
		return rm.evaluateAgeAndMark(conn, now)
	case RebuildStrategyError:
		return rm.evaluateErrorAndMark(conn)
	case RebuildStrategyAll:
		return rm.evaluateAllAndMark(conn, now)
	case RebuildStrategyAny:
		fallthrough
	default:
		return rm.evaluateAnyAndMark(conn, now)
	}
}

// evaluateUsageAndMark 评估使用次数策略并标记
func (rm *RebuildManager) evaluateUsageAndMark(conn *EnhancedPooledConnection) (bool, string) {
	usageCount := conn.getUsageCount()
	shouldRebuild := usageCount >= rm.config.RebuildMaxUsageCount

	if shouldRebuild {
		reason := fmt.Sprintf("usage_count_exceeded: usage=%d, max=%d", usageCount, rm.config.RebuildMaxUsageCount)
		if conn.markForRebuildWithReason(reason) {
			ylog.Infof("rebuild_manager", "连接标记为需要重建: id=%s, reason=%s", conn.id, reason)
			rm.sendRebuildMarkedEvent(conn, reason)
			return true, reason
		}
		return false, "already_marked_by_other"
	}

	return false, fmt.Sprintf("usage_count_ok: usage=%d, max=%d", usageCount, rm.config.RebuildMaxUsageCount)
}

// evaluateAgeAndMark 评估年龄策略并标记
func (rm *RebuildManager) evaluateAgeAndMark(conn *EnhancedPooledConnection, now time.Time) (bool, string) {
	age := now.Sub(conn.getCreatedAt())
	shouldRebuild := age >= rm.config.RebuildMaxAge

	if shouldRebuild {
		reason := fmt.Sprintf("age_exceeded: age=%v, max=%v", age, rm.config.RebuildMaxAge)
		if conn.markForRebuildWithReason(reason) {
			ylog.Infof("rebuild_manager", "连接标记为需要重建: id=%s, reason=%s", conn.id, reason)
			rm.sendRebuildMarkedEvent(conn, reason)
			return true, reason
		}
		return false, "already_marked_by_other"
	}

	return false, fmt.Sprintf("age_ok: age=%v, max=%v", age, rm.config.RebuildMaxAge)
}

// evaluateErrorAndMark 评估错误率策略并标记
func (rm *RebuildManager) evaluateErrorAndMark(conn *EnhancedPooledConnection) (bool, string) {
	totalRequests := atomic.LoadInt64(&conn.totalRequests)
	if totalRequests < rm.config.RebuildMinRequestsForErrorRate {
		return false, fmt.Sprintf("insufficient_requests: requests=%d, min=%d", totalRequests, rm.config.RebuildMinRequestsForErrorRate)
	}

	if totalRequests > 0 {
		totalErrors := atomic.LoadInt64(&conn.totalErrors)
		errorRate := float64(totalErrors) / float64(totalRequests)
		shouldRebuild := errorRate >= rm.config.RebuildMaxErrorRate

		if shouldRebuild {
			reason := fmt.Sprintf("error_rate_exceeded: requests=%d, errors=%d, rate=%.2f, max=%.2f",
				totalRequests, totalErrors, errorRate, rm.config.RebuildMaxErrorRate)
			if conn.markForRebuildWithReason(reason) {
				ylog.Infof("rebuild_manager", "连接标记为需要重建: id=%s, reason=%s", conn.id, reason)
				rm.sendRebuildMarkedEvent(conn, reason)
				return true, reason
			}
			return false, "already_marked_by_other"
		}

		return false, fmt.Sprintf("error_rate_ok: requests=%d, errors=%d, rate=%.2f, max=%.2f",
			totalRequests, totalErrors, errorRate, rm.config.RebuildMaxErrorRate)
	}

	return false, "no_requests"
}

// evaluateAllAndMark 评估所有条件策略并标记
func (rm *RebuildManager) evaluateAllAndMark(conn *EnhancedPooledConnection, now time.Time) (bool, string) {
	conditions := 0
	var reasons []string

	// 检查使用次数
	usageCount := atomic.LoadInt64(&conn.usageCount)
	if usageCount >= rm.config.RebuildMaxUsageCount {
		conditions++
		reasons = append(reasons, fmt.Sprintf("usage=%d>=%d", usageCount, rm.config.RebuildMaxUsageCount))
	}

	// 检查年龄
	age := now.Sub(conn.getCreatedAt())
	if age >= rm.config.RebuildMaxAge {
		conditions++
		reasons = append(reasons, fmt.Sprintf("age=%v>=%v", age, rm.config.RebuildMaxAge))
	}

	// 检查错误率
	totalRequests := atomic.LoadInt64(&conn.totalRequests)
	if totalRequests >= rm.config.RebuildMinRequestsForErrorRate {
		totalErrors := atomic.LoadInt64(&conn.totalErrors)
		if totalRequests > 0 {
			errorRate := float64(totalErrors) / float64(totalRequests)
			if errorRate >= rm.config.RebuildMaxErrorRate {
				conditions++
				reasons = append(reasons, fmt.Sprintf("error_rate=%.2f>=%.2f", errorRate, rm.config.RebuildMaxErrorRate))
			}
		}
	}

	if conditions == 3 {
		reason := fmt.Sprintf("all_conditions_met: %s", strings.Join(reasons, ", "))
		if conn.markForRebuildWithReason(reason) {
			ylog.Infof("rebuild_manager", "连接标记为需要重建: id=%s, reason=%s", conn.id, reason)
			rm.sendRebuildMarkedEvent(conn, reason)
			return true, reason
		}
		return false, "already_marked_by_other"
	}

	return false, fmt.Sprintf("not_all_conditions: conditions=%d/3, %s", conditions, strings.Join(reasons, ", "))
}

// evaluateAnyAndMark 评估任意条件策略并标记
func (rm *RebuildManager) evaluateAnyAndMark(conn *EnhancedPooledConnection, now time.Time) (bool, string) {
	// 检查使用次数
	usageCount := atomic.LoadInt64(&conn.usageCount)
	if usageCount >= rm.config.RebuildMaxUsageCount {
		reason := fmt.Sprintf("usage_count_exceeded: usage=%d, max=%d", usageCount, rm.config.RebuildMaxUsageCount)
		if conn.markForRebuildWithReason(reason) {
			ylog.Infof("rebuild_manager", "连接标记为需要重建: id=%s, reason=%s", conn.id, reason)
			rm.sendRebuildMarkedEvent(conn, reason)
			return true, reason
		}
		return false, "already_marked_by_other"
	}

	// 检查年龄
	age := now.Sub(conn.getCreatedAt())
	if age >= rm.config.RebuildMaxAge {
		reason := fmt.Sprintf("age_exceeded: age=%v, max=%v", age, rm.config.RebuildMaxAge)
		if conn.markForRebuildWithReason(reason) {
			ylog.Infof("rebuild_manager", "连接标记为需要重建: id=%s, reason=%s", conn.id, reason)
			rm.sendRebuildMarkedEvent(conn, reason)
			return true, reason
		}
		return false, "already_marked_by_other"
	}

	// 检查错误率
	totalRequests := atomic.LoadInt64(&conn.totalRequests)
	if totalRequests >= rm.config.RebuildMinRequestsForErrorRate {
		totalErrors := atomic.LoadInt64(&conn.totalErrors)
		if totalRequests > 0 {
			errorRate := float64(totalErrors) / float64(totalRequests)
			if errorRate >= rm.config.RebuildMaxErrorRate {
				reason := fmt.Sprintf("error_rate_exceeded: requests=%d, errors=%d, rate=%.2f, max=%.2f",
					totalRequests, totalErrors, errorRate, rm.config.RebuildMaxErrorRate)
				if conn.markForRebuildWithReason(reason) {
					ylog.Infof("rebuild_manager", "连接标记为需要重建: id=%s, reason=%s", conn.id, reason)
					rm.sendRebuildMarkedEvent(conn, reason)
					return true, reason
				}
				return false, "already_marked_by_other"
			}
		}
	}

	return false, "no_conditions_met"
}

// ShouldRebuild 判断连接是否需要重建
func (rm *RebuildManager) ShouldRebuild(conn *EnhancedPooledConnection) bool {
	// 先检查是否已标记（快速路径）
	if conn.isMarkedForRebuild() {
		ylog.Debugf("rebuild_manager", "连接已标记为需要重建: id=%s, reason=%s",
			conn.id, conn.getRebuildReason())
		return true
	}

	// 未标记，调用统一入口进行评估和标记
	marked, _ := rm.EvaluateAndMark(conn)
	return marked
}

// checkRebuildEnabled 检查重建是否启用
func (rm *RebuildManager) checkRebuildEnabled() bool {
	if !rm.config.SmartRebuildEnabled {
		ylog.Infof("rebuild_manager", "checkRebuildEnabled: SmartRebuildEnabled=false")
		return false
	}
	return true
}

// checkRebuildInterval 检查重建间隔
func (rm *RebuildManager) checkRebuildInterval(conn *EnhancedPooledConnection, now time.Time) bool {
	// 避免频繁重建：检查最小重建间隔
	// 使用lastRebuiltAt（如果存在）或createdAt作为"当前版本开始时间"
	lastRebuiltAt := conn.getLastRebuiltAt()
	createdAt := conn.getCreatedAt()

	var lastRebuildOrCreate time.Time
	if !lastRebuiltAt.IsZero() {
		lastRebuildOrCreate = lastRebuiltAt
	} else {
		lastRebuildOrCreate = createdAt
	}

	if now.Sub(lastRebuildOrCreate) < rm.config.RebuildMinInterval {
		ylog.Infof("rebuild_manager", "checkRebuildInterval: 重建间隔太短: id=%s, interval=%v, min=%v",
			conn.id, now.Sub(lastRebuildOrCreate), rm.config.RebuildMinInterval)
		return false
	}
	return true
}

// checkConnectionState 检查连接状态是否适合重建
func (rm *RebuildManager) checkConnectionState(conn *EnhancedPooledConnection) bool {
	state, _ := conn.getStatus()
	if !conn.isIdle() || conn.isRebuilding() || state == StateClosing || state == StateClosed {
		ylog.Infof("rebuild_manager", "checkConnectionState: 连接状态不适合重建: id=%s, state=%s, isRebuilding=%v",
			conn.id, state, conn.isRebuilding())
		return false
	}
	return true
}

// checkConnectionHealth 检查连接健康状态
func (rm *RebuildManager) checkConnectionHealth(conn *EnhancedPooledConnection) bool {
	// 健康状态不影响重建决策
	// 实际上，不健康状态更应该重建
	return true
}

// checkRebuildMark 检查是否已标记为重建
func (rm *RebuildManager) checkRebuildMark(conn *EnhancedPooledConnection) bool {
	// 如果已标记，说明需要重建，直接返回 true
	if conn.isMarkedForRebuild() {
		ylog.Infof("rebuild_manager", "checkRebuildMark: 连接已标记需要重建: id=%s, reason=%s",
			conn.id, conn.getRebuildReason())
		return true // 已标记 = 需要重建
	}

	// 未标记，需要评估是否应该标记
	ylog.Debugf("rebuild_manager", "checkRebuildMark: 连接未标记，需要评估: id=%s", conn.id)
	return false // 未标记 = 需要评估策略
}

// logConnectionDetails 记录连接详细信息（用于调试）
func (rm *RebuildManager) logConnectionDetails(conn *EnhancedPooledConnection, now time.Time) {
	usageCount := conn.getUsageCount()
	markedForRebuild := conn.isMarkedForRebuild()
	state, health := conn.getStatus()
	lastRebuiltAt := conn.getLastRebuiltAt()
	createdAt := conn.getCreatedAt()

	var lastRebuildOrCreate time.Time
	if !lastRebuiltAt.IsZero() {
		lastRebuildOrCreate = lastRebuiltAt
	} else {
		lastRebuildOrCreate = createdAt
	}

	ylog.Debugf("rebuild_manager", "logConnectionDetails: id=%s, state=%s, health=%v, usage=%d/%d, createdAt=%v, lastRebuiltAt=%v, strategy=%s, marked=%v, minInterval=%v, actualInterval=%v",
		conn.id, state, health, usageCount, rm.config.RebuildMaxUsageCount, createdAt, lastRebuiltAt, rm.config.RebuildStrategy, markedForRebuild, rm.config.RebuildMinInterval, now.Sub(lastRebuildOrCreate))

	// 直接输出到标准输出，确保调试信息可见
	ylog.Debugf("rebuild_manager", "shouldRebuild: id=%s, state=%s, health=%v, usage=%d/%d, strategy=%s, marked=%v, shouldRebuild=%v",
		conn.id, state, health, usageCount, rm.config.RebuildMaxUsageCount, rm.config.RebuildStrategy, markedForRebuild, usageCount >= rm.config.RebuildMaxUsageCount)
}

// GetRebuildReason 获取重建原因
func (rm *RebuildManager) GetRebuildReason(conn *EnhancedPooledConnection) string {
	now := time.Now()

	// 按优先级检查各个条件，返回最主要的原因
	usageCount := atomic.LoadInt64(&conn.usageCount)
	if usageCount >= rm.config.RebuildMaxUsageCount {
		return fmt.Sprintf("usage_exceeded(%d>=%d)", usageCount, rm.config.RebuildMaxUsageCount)
	}

	age := now.Sub(conn.getCreatedAt())
	if age >= rm.config.RebuildMaxAge {
		return fmt.Sprintf("age_exceeded(%v>=%v)", age, rm.config.RebuildMaxAge)
	}

	totalRequests := atomic.LoadInt64(&conn.totalRequests)
	if totalRequests >= rm.config.RebuildMinRequestsForErrorRate {
		totalErrors := atomic.LoadInt64(&conn.totalErrors)
		if totalRequests > 0 { // 防止除零错误
			// 使用浮点数计算确保精度，但保持与测试一致的格式化
			errorRate := float64(totalErrors) / float64(totalRequests)
			if errorRate >= rm.config.RebuildMaxErrorRate {
				return fmt.Sprintf("error_rate_exceeded(%.2f>=%.2f)", errorRate, rm.config.RebuildMaxErrorRate)
			}
		}
	}

	// 如果没有明确的原因，返回所有满足的条件
	reasons := []string{}
	if usageCount >= rm.config.RebuildMaxUsageCount {
		reasons = append(reasons, fmt.Sprintf("usage(%d)", usageCount))
	}
	if age >= rm.config.RebuildMaxAge {
		reasons = append(reasons, fmt.Sprintf("age(%v)", age))
	}
	if totalRequests >= rm.config.RebuildMinRequestsForErrorRate &&
		float64(atomic.LoadInt64(&conn.totalErrors))/float64(totalRequests) >= rm.config.RebuildMaxErrorRate {
		errorRate := float64(atomic.LoadInt64(&conn.totalErrors)) / float64(totalRequests)
		reasons = append(reasons, fmt.Sprintf("error_rate(%.2f)", errorRate))
	}

	if len(reasons) > 0 {
		return strings.Join(reasons, "|")
	}

	// 如果没有任何条件满足，返回空字符串
	return ""
}

// sendEvent 发送事件（内部方法）
func (rm *RebuildManager) sendEvent(eventType PoolEventType, protocol Protocol, data map[string]interface{}) {
	if rm.eventChan == nil {
		return
	}

	select {
	case rm.eventChan <- PoolEvent{
		Type:      eventType,
		Protocol:  protocol,
		Timestamp: time.Now(),
		Data:      data,
	}:
	default:
		// 事件通道已满，丢弃事件
		ylog.Debugf("rebuild_manager", "event channel full, dropping event: %d", eventType)
	}
}

// sendRebuildMarkedEvent 发送重建标记事件并收集指标
func (rm *RebuildManager) sendRebuildMarkedEvent(conn *EnhancedPooledConnection, reason string) {
	// 发送事件
	rm.sendEvent(EventRebuildMarked, conn.protocol, map[string]interface{}{
		"connection_id": conn.id,
		"reason":        reason,
	})

	// 收集指标
	if rm.collector != nil {
		rm.collector.IncrementRebuildMarked(conn.protocol, reason)
		rm.collector.IncrementConnectionsNeedingRebuild(conn.protocol)
	}
}

// sendRebuildStartedEvent 发送重建开始事件并收集指标
func (rm *RebuildManager) sendRebuildStartedEvent(conn *EnhancedPooledConnection) {
	// 发送事件
	rm.sendEvent(EventRebuildStarted, conn.protocol, map[string]interface{}{
		"connection_id": conn.id,
	})

	// 收集指标
	if rm.collector != nil {
		rm.collector.IncrementRebuildStarted(conn.protocol)
		rm.collector.IncrementRebuildingConnections(conn.protocol)
	}
}

// sendRebuildCompletedEvent 发送重建完成事件并收集指标
func (rm *RebuildManager) sendRebuildCompletedEvent(conn *EnhancedPooledConnection, duration time.Duration, success bool) {
	// 发送事件
	rm.sendEvent(EventRebuildCompleted, conn.protocol, map[string]interface{}{
		"connection_id": conn.id,
		"duration":      duration,
		"success":       success,
	})

	// 收集指标
	if rm.collector != nil {
		rm.collector.RecordRebuildDuration(conn.protocol, duration)
		rm.collector.DecrementRebuildingConnections(conn.protocol)
		if success {
			rm.collector.IncrementRebuildCompleted(conn.protocol)
		} else {
			rm.collector.IncrementRebuildFailed(conn.protocol)
		}
	}
}

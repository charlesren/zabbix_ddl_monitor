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

// ShouldRebuild 判断连接是否需要重建（重构版：使用多个小函数）
func (rm *RebuildManager) ShouldRebuild(conn *EnhancedPooledConnection) bool {
	// 检查基本条件
	if !rm.checkRebuildEnabled() {
		return false
	}

	now := time.Now()

	// 检查重建间隔
	if !rm.checkRebuildInterval(conn, now) {
		return false
	}

	// 检查连接状态
	if !rm.checkConnectionState(conn) {
		return false
	}

	// 检查健康状态
	if !rm.checkConnectionHealth(conn) {
		return false
	}

	// 检查是否已标记为重建
	if !rm.checkRebuildMark(conn) {
		return false
	}

	// 记录调试信息
	rm.logConnectionDetails(conn, now)

	// 根据策略评估
	return rm.evaluateRebuildStrategy(conn, now)
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
	if conn.isInUse() || conn.isRebuilding() || state == StateClosing || state == StateClosed {
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
	if conn.isMarkedForRebuild() {
		ylog.Infof("rebuild_manager", "checkRebuildMark: 连接已标记为正在重建: id=%s", conn.id)
		return false
	}
	return true
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

// evaluateRebuildStrategy 根据策略评估是否需要重建
func (rm *RebuildManager) evaluateRebuildStrategy(conn *EnhancedPooledConnection, now time.Time) bool {
	switch rm.config.RebuildStrategy {
	case "usage":
		return rm.evaluateUsageStrategy(conn)
	case "age":
		return rm.evaluateAgeStrategy(conn, now)
	case "error":
		return rm.evaluateErrorStrategy(conn)
	case "all":
		return rm.evaluateAllStrategy(conn, now)
	case "any": // 默认策略
		fallthrough
	default:
		return rm.evaluateAnyStrategy(conn, now)
	}
}

// evaluateUsageStrategy 评估使用次数策略
func (rm *RebuildManager) evaluateUsageStrategy(conn *EnhancedPooledConnection) bool {
	usageCount := conn.getUsageCount()
	shouldRebuild := usageCount >= rm.config.RebuildMaxUsageCount

	if shouldRebuild {
		// 使用原子操作确保只有一个goroutine能将连接标记为重建
		reason := fmt.Sprintf("usage_count_exceeded: usage=%d, max=%d", usageCount, rm.config.RebuildMaxUsageCount)
		if conn.markForRebuildWithReason(reason) {
			ylog.Debugf("rebuild_manager", "evaluateUsageStrategy: id=%s, usage=%d, max=%d, rebuild=%v (已标记), reason=%s",
				conn.id, usageCount, rm.config.RebuildMaxUsageCount, shouldRebuild, reason)
			return true
		} else {
			ylog.Debugf("rebuild_manager", "evaluateUsageStrategy: id=%s, usage=%d, max=%d, rebuild=%v (已被其他goroutine标记)",
				conn.id, usageCount, rm.config.RebuildMaxUsageCount, shouldRebuild)
			return false
		}
	}

	ylog.Debugf("rebuild_manager", "evaluateUsageStrategy: id=%s, usage=%d, max=%d, rebuild=%v",
		conn.id, usageCount, rm.config.RebuildMaxUsageCount, shouldRebuild)
	return shouldRebuild
}

// evaluateAgeStrategy 评估年龄策略
func (rm *RebuildManager) evaluateAgeStrategy(conn *EnhancedPooledConnection, now time.Time) bool {
	age := now.Sub(conn.getCreatedAt())
	shouldRebuild := age >= rm.config.RebuildMaxAge

	if shouldRebuild {
		// 使用原子操作确保只有一个goroutine能将连接标记为重建
		reason := fmt.Sprintf("age_exceeded: age=%v, max=%v", age, rm.config.RebuildMaxAge)
		if conn.markForRebuildWithReason(reason) {
			ylog.Debugf("rebuild_manager", "evaluateAgeStrategy: id=%s, age=%v, max=%v, rebuild=%v (已标记), reason=%s",
				conn.id, age, rm.config.RebuildMaxAge, shouldRebuild, reason)
			return true
		} else {
			ylog.Debugf("rebuild_manager", "evaluateAgeStrategy: id=%s, age=%v, max=%v, rebuild=%v (已被其他goroutine标记)",
				conn.id, age, rm.config.RebuildMaxAge, shouldRebuild)
			return false
		}
	}

	ylog.Debugf("rebuild_manager", "evaluateAgeStrategy: id=%s, age=%v, max=%v, rebuild=%v",
		conn.id, age, rm.config.RebuildMaxAge, shouldRebuild)
	return shouldRebuild
}

// evaluateErrorStrategy 评估错误率策略
func (rm *RebuildManager) evaluateErrorStrategy(conn *EnhancedPooledConnection) bool {
	totalRequests := atomic.LoadInt64(&conn.totalRequests)
	if totalRequests >= rm.config.RebuildMinRequestsForErrorRate {
		totalErrors := atomic.LoadInt64(&conn.totalErrors)
		if totalRequests > 0 { // 防止除零错误
			// 使用浮点数计算确保精度
			errorRate := float64(totalErrors) / float64(totalRequests)
			shouldRebuild := errorRate >= rm.config.RebuildMaxErrorRate

			if shouldRebuild {
				// 使用原子操作确保只有一个goroutine能将连接标记为重建
				reason := fmt.Sprintf("error_rate_exceeded: requests=%d, errors=%d, rate=%.2f, max=%.2f",
					totalRequests, totalErrors, errorRate, rm.config.RebuildMaxErrorRate)
				if conn.markForRebuildWithReason(reason) {
					ylog.Debugf("rebuild_manager", "evaluateErrorStrategy: id=%s, requests=%d, errors=%d, rate=%.2f, max=%.2f, rebuild=%v (已标记), reason=%s",
						conn.id, totalRequests, totalErrors, errorRate, rm.config.RebuildMaxErrorRate, shouldRebuild, reason)
					return true
				} else {
					ylog.Debugf("rebuild_manager", "evaluateErrorStrategy: id=%s, requests=%d, errors=%d, rate=%.2f, max=%.2f, rebuild=%v (已被其他goroutine标记)",
						conn.id, totalRequests, totalErrors, errorRate, rm.config.RebuildMaxErrorRate, shouldRebuild)
					return false
				}
			}

			ylog.Debugf("rebuild_manager", "evaluateErrorStrategy: id=%s, requests=%d, errors=%d, rate=%.2f, max=%.2f, rebuild=%v",
				conn.id, totalRequests, totalErrors, errorRate, rm.config.RebuildMaxErrorRate, shouldRebuild)
			return shouldRebuild
		}
	}

	ylog.Debugf("rebuild_manager", "evaluateErrorStrategy: id=%s, requests=%d (太少)，跳过", conn.id, totalRequests)
	return false
}

// evaluateAllStrategy 评估所有条件策略
func (rm *RebuildManager) evaluateAllStrategy(conn *EnhancedPooledConnection, now time.Time) bool {
	conditions := 0

	// 检查使用次数条件
	usageCount := atomic.LoadInt64(&conn.usageCount)
	if usageCount >= rm.config.RebuildMaxUsageCount {
		conditions++
		ylog.Debugf("rebuild_manager", "evaluateAllStrategy: id=%s, usage条件满足: %d >= %d",
			conn.id, usageCount, rm.config.RebuildMaxUsageCount)
	}

	// 检查年龄条件
	age := now.Sub(conn.getCreatedAt())
	if age >= rm.config.RebuildMaxAge {
		conditions++
		ylog.Debugf("rebuild_manager", "evaluateAllStrategy: id=%s, age条件满足: %v >= %v",
			conn.id, age, rm.config.RebuildMaxAge)
	}

	// 检查错误率条件
	totalRequests := atomic.LoadInt64(&conn.totalRequests)
	if totalRequests >= rm.config.RebuildMinRequestsForErrorRate {
		totalErrors := atomic.LoadInt64(&conn.totalErrors)
		// 使用浮点数计算确保精度
		errorRate := float64(totalErrors) / float64(totalRequests)
		if errorRate >= rm.config.RebuildMaxErrorRate {
			conditions++
			ylog.Debugf("rebuild_manager", "evaluateAllStrategy: id=%s, error条件满足: %.2f >= %.2f",
				conn.id, errorRate, rm.config.RebuildMaxErrorRate)
		}
	}

	shouldRebuild := conditions == 3
	if shouldRebuild {
		// 使用原子操作确保只有一个goroutine能将连接标记为重建
		reason := fmt.Sprintf("all_conditions_met: conditions=%d/3", conditions)
		if conn.markForRebuildWithReason(reason) {
			ylog.Debugf("rebuild_manager", "evaluateAllStrategy: id=%s, conditions=%d/3, rebuild=%v (已标记), reason=%s",
				conn.id, conditions, shouldRebuild, reason)
			return true
		} else {
			ylog.Debugf("rebuild_manager", "evaluateAllStrategy: id=%s, conditions=%d/3, rebuild=%v (已被其他goroutine标记)",
				conn.id, conditions, shouldRebuild)
			return false
		}
	}

	ylog.Debugf("rebuild_manager", "evaluateAllStrategy: id=%s, conditions=%d/3, rebuild=%v",
		conn.id, conditions, shouldRebuild)
	return shouldRebuild
}

// evaluateAnyStrategy 评估任意条件策略（默认策略）
func (rm *RebuildManager) evaluateAnyStrategy(conn *EnhancedPooledConnection, now time.Time) bool {
	// 检查使用次数条件
	usageCount := atomic.LoadInt64(&conn.usageCount)
	if usageCount >= rm.config.RebuildMaxUsageCount {
		// 使用原子操作确保只有一个goroutine能将连接标记为重建
		reason := fmt.Sprintf("usage_count_exceeded: usage=%d, max=%d", usageCount, rm.config.RebuildMaxUsageCount)
		if conn.markForRebuildWithReason(reason) {
			ylog.Debugf("rebuild_manager", "evaluateAnyStrategy: 连接需要重建: id=%s, 使用次数达到%d >= %d (已标记), reason=%s",
				conn.id, usageCount, rm.config.RebuildMaxUsageCount, reason)
			return true
		} else {
			ylog.Debugf("rebuild_manager", "evaluateAnyStrategy: 连接需要重建: id=%s, 使用次数达到%d >= %d (已被其他goroutine标记)",
				conn.id, usageCount, rm.config.RebuildMaxUsageCount)
			return false
		}
	}

	// 检查年龄条件
	age := now.Sub(conn.getCreatedAt())
	if age >= rm.config.RebuildMaxAge {
		// 使用原子操作确保只有一个goroutine能将连接标记为重建
		reason := fmt.Sprintf("age_exceeded: age=%v, max=%v", age, rm.config.RebuildMaxAge)
		if conn.markForRebuildWithReason(reason) {
			ylog.Debugf("rebuild_manager", "evaluateAnyStrategy: 连接需要重建: id=%s, 年龄达到%v >= %v (已标记), reason=%s",
				conn.id, age, rm.config.RebuildMaxAge, reason)
			return true
		} else {
			ylog.Debugf("rebuild_manager", "evaluateAnyStrategy: 连接需要重建: id=%s, 年龄达到%v >= %v (已被其他goroutine标记)",
				conn.id, age, rm.config.RebuildMaxAge)
			return false
		}
	}

	// 检查错误率条件
	totalRequests := atomic.LoadInt64(&conn.totalRequests)
	if totalRequests >= rm.config.RebuildMinRequestsForErrorRate {
		totalErrors := atomic.LoadInt64(&conn.totalErrors)
		if totalRequests > 0 { // 防止除零错误
			// 使用浮点数计算确保精度
			errorRate := float64(totalErrors) / float64(totalRequests)
			if errorRate >= rm.config.RebuildMaxErrorRate {
				// 使用原子操作确保只有一个goroutine能将连接标记为重建
				reason := fmt.Sprintf("error_rate_exceeded: requests=%d, errors=%d, rate=%.2f, max=%.2f",
					totalRequests, totalErrors, errorRate, rm.config.RebuildMaxErrorRate)
				if conn.markForRebuildWithReason(reason) {
					ylog.Debugf("rebuild_manager", "evaluateAnyStrategy: 连接需要重建: id=%s, 错误率达到%.2f >= %.2f (已标记), reason=%s",
						conn.id, errorRate, rm.config.RebuildMaxErrorRate, reason)
					return true
				} else {
					ylog.Debugf("rebuild_manager", "evaluateAnyStrategy: 连接需要重建: id=%s, 错误率达到%.2f >= %.2f (已被其他goroutine标记)",
						conn.id, errorRate, rm.config.RebuildMaxErrorRate)
					return false
				}
			}
		}
	}

	ylog.Debugf("rebuild_manager", "evaluateAnyStrategy: 连接不需要重建: id=%s", conn.id)
	return false
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

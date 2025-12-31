package connection

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charlesren/ylog"
)

// EnhancedPooledConnection 增强的连接包装器
type EnhancedPooledConnection struct {
	// 连接级读写锁，保护所有状态字段
	mu sync.RWMutex

	driver     ProtocolDriver
	id         string
	protocol   Protocol
	createdAt  time.Time
	lastUsed   time.Time
	usageCount int64

	// 状态管理（使用状态机）
	state ConnectionState

	// 重建管理标记（非连接状态）
	rebuilding       bool      // 正在重建标记（需要锁保护）
	lastRebuiltAt    time.Time // 上次重建时间
	markedForRebuild int32     // 标记是否需要重建，防止并发决策(0=未标记, 1=已标记)
	rebuildReason    string    // 重建原因（便于监控）

	// 健康状态
	healthStatus        HealthStatus
	lastHealthCheck     time.Time
	consecutiveFailures int

	// 性能指标
	avgResponseTime time.Duration
	totalRequests   int64
	totalErrors     int64

	// 使用保护（新增）
	usingCount int32 // 原子操作，记录有多少地方在使用此连接

	// 配置引用（用于健康状态判断）
	config *EnhancedConnectionConfig

	// 标签和元数据
	labels   map[string]string
	metadata map[string]interface{}
}

// tryAcquireForTask 尝试获取连接用于执行任务（非阻塞）
//
// 这个方法用于任务执行时获取连接，会检查：
// 1. 使用计数为0（没有被其他任务使用）
// 2. 连接状态为 StateIdle（空闲）
// 3. 健康状态不为 HealthStatusUnhealthy（非不健康）
// 4. 不在重建中（rebuilding == false）
//
// 只有满足所有条件的连接才会被标记为 StateAcquired 并返回 true
func (conn *EnhancedPooledConnection) tryAcquireForTask() bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	// 第一层检查：使用计数 > 0 的连接不应该被获取（用于清理）
	if conn.getUseCount() > 0 {
		ylog.Infof("EnhancedPooledConnection", "tryAcquireForTask: 连接正在使用中: id=%s, useCount=%d", conn.id, conn.getUseCount())
		return false
	}

	// 检查连接是否可用（必须是空闲状态）
	if conn.state != StateIdle {
		ylog.Debugf("EnhancedPooledConnection", "tryAcquireForTask: 连接不可用: id=%s, state=%s", conn.id, conn.state)
		return false
	}

	// 检查健康状态（不健康的连接不能用于任务执行）
	if conn.healthStatus == HealthStatusUnhealthy {
		ylog.Debugf("EnhancedPooledConnection", "tryAcquireForTask: 连接不健康: id=%s, health=%s", conn.id, conn.healthStatus)
		return false
	}

	// 检查是否正在重建（重建中的连接不能用于任务执行）
	if conn.rebuilding {
		ylog.Infof("EnhancedPooledConnection", "tryAcquireForTask: 连接正在重建中: id=%s", conn.id)
		return false
	}

	// 转换为使用中状态
	if !conn.transitionStateLocked(StateAcquired) {
		ylog.Debugf("EnhancedPooledConnection", "tryAcquireForTask: 状态转换失败: id=%s, from=%s, to=%s", conn.id, conn.state, StateAcquired)
		return false
	}

	conn.lastUsed = time.Now()
	atomic.AddInt64(&conn.usageCount, 1)
	ylog.Debugf("EnhancedPooledConnection", "tryAcquireForTask: 成功获取连接用于任务执行: id=%s", conn.id)
	return true
}

// release 释放连接
func (conn *EnhancedPooledConnection) release() {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	// 只有使用中的连接才能释放
	if conn.state != StateAcquired {
		ylog.Warnf("EnhancedPooledConnection", "release: 连接不在使用中: id=%s, state=%s", conn.id, conn.state)
		return
	}

	// 转换为空闲状态
	if !conn.transitionStateLocked(StateIdle) {
		ylog.Warnf("EnhancedPooledConnection", "release: 状态转换失败: id=%s, from=%s, to=%s", conn.id, conn.state, StateIdle)
		// 如果无法转换为空闲状态，则标记为关闭
		conn.transitionStateLocked(StateClosing)
		return
	}

	conn.lastUsed = time.Now()
	ylog.Debugf("EnhancedPooledConnection", "release: 成功释放连接: id=%s", conn.id)
}

// getState 获取连接状态（线程安全）
func (conn *EnhancedPooledConnection) getState() (inUse, valid bool, health HealthStatus) {
	conn.mu.RLock()
	defer conn.mu.RUnlock()

	inUse = conn.state == StateAcquired
	valid = conn.state != StateClosed && conn.state != StateClosing
	health = conn.healthStatus
	return
}

// getStatus 获取连接状态和健康状态
func (conn *EnhancedPooledConnection) getStatus() (state ConnectionState, health HealthStatus) {
	conn.mu.RLock()
	defer conn.mu.RUnlock()
	return conn.state, conn.healthStatus
}

// setHealth 设置健康状态（线程安全）
func (conn *EnhancedPooledConnection) setHealth(status HealthStatus, valid bool) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	conn.setHealthLocked(status, valid)
}

// recordHealthCheck 记录健康检查结果
func (conn *EnhancedPooledConnection) recordHealthCheck(success bool, err error) {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	conn.lastHealthCheck = time.Now()

	// 使用setHealthLocked保持一致性并记录日志
	if success {
		conn.consecutiveFailures = 0
		conn.setHealthLocked(HealthStatusHealthy, true)
		ylog.Infof("EnhancedPooledConnection", "recordHealthCheck: 健康检查成功: id=%s", conn.id)
	} else {
		conn.consecutiveFailures++

		// 获取配置阈值
		degradedThreshold := conn.config.DegradedFailureThreshold   // 默认1
		unhealthyThreshold := conn.config.UnhealthyFailureThreshold // 默认3

		// 记录错误类型
		errorType := "unknown"
		if err != nil {
			if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "deadline") {
				errorType = "timeout"
			}
		}

		// 根据连续失败次数设置健康状态
		if conn.consecutiveFailures >= unhealthyThreshold {
			// 达到不健康阈值
			conn.setHealthLocked(HealthStatusUnhealthy, false)

			ylog.Warnf("EnhancedPooledConnection", "recordHealthCheck: 连接不健康: id=%s, error=%v, type=%s, consecutiveFailures=%d, threshold=%d",
				conn.id, err, errorType, conn.consecutiveFailures, unhealthyThreshold)

			// 检查是否需要触发重建（注意：已持有锁，使用内部版本）
			if conn.config.HealthCheckTriggerRebuild {
				reason := fmt.Sprintf("health_check_failed_%d_times", conn.consecutiveFailures)
				if conn.markForRebuildWithReasonLocked(reason) {
					ylog.Infof("EnhancedPooledConnection", "recordHealthCheck: 已标记重建: id=%s, reason=%s", conn.id, reason)
				}
			}
		} else if conn.consecutiveFailures >= degradedThreshold {
			// 达到降级阈值
			conn.setHealthLocked(HealthStatusDegraded, false)

			ylog.Warnf("EnhancedPooledConnection", "recordHealthCheck: 连接降级: id=%s, error=%v, type=%s, consecutiveFailures=%d, threshold=%d",
				conn.id, err, errorType, conn.consecutiveFailures, degradedThreshold)

			// 如果配置了降级也重建，则标记（注意：已持有锁，使用内部版本）
			if conn.config.RebuildOnDegraded {
				reason := fmt.Sprintf("health_check_degraded_%d_times", conn.consecutiveFailures)
				if conn.markForRebuildWithReasonLocked(reason) {
					ylog.Infof("EnhancedPooledConnection", "recordHealthCheck: 已标记重建: id=%s, reason=%s", conn.id, reason)
				}
			}
		} else {
			// 低于阈值，仍视为健康（但记录警告）
			conn.setHealthLocked(HealthStatusHealthy, false)

			ylog.Infof("EnhancedPooledConnection", "recordHealthCheck: 健康检查失败但未达到阈值: id=%s, error=%v, type=%s, consecutiveFailures=%d, degradedThreshold=%d, unhealthyThreshold=%d",
				conn.id, err, errorType, conn.consecutiveFailures, degradedThreshold, unhealthyThreshold)
		}
	}

	// 状态恢复：健康检查结束后转换到 StateAcquired
	// 由上层调用者决定后续如何处理（如 Release() 转回 StateIdle）
	if conn.state == StateChecking {
		// 正常情况：健康检查完成，转换到 Acquired
		if !conn.transitionStateLocked(StateAcquired) {
			// 转换失败（已持有锁，直接访问 conn.state）
			ylog.Errorf("EnhancedPooledConnection",
				"recordHealthCheck: Checking→Acquired 转换失败: id=%s, current_state=%s",
				conn.id, conn.state)
		} else {
			ylog.Debugf("EnhancedPooledConnection", "recordHealthCheck: 健康检查完成，状态转为Acquired: id=%s", conn.id)
		}
	} else if conn.state != StateAcquired {
		// 异常情况：不在 Checking 也不在 Acquired（已持有锁，直接访问 conn.state）
		ylog.Warnf("EnhancedPooledConnection",
			"recordHealthCheck: 连接不在预期状态: id=%s, expected=Checking, actual=%s",
			conn.id, conn.state)
	}
}

// setHealthLocked 设置健康状态（需要持有锁）
func (conn *EnhancedPooledConnection) setHealthLocked(status HealthStatus, valid bool) {
	oldStatus := conn.healthStatus
	conn.healthStatus = status

	// 记录健康状态变化
	if oldStatus != status {
		ylog.Infof("EnhancedPooledConnection", "setHealthLocked: 健康状态变化: id=%s, old=%s, new=%s",
			conn.id, oldStatus, status)
	}

	// 移除尝试转换为Closing的逻辑
	// 健康检查只负责标记健康状态，不负责关闭连接
	// 连接池清理逻辑会处理不健康的连接
}

// incrementUsage 增加使用计数
func (conn *EnhancedPooledConnection) incrementUsage() {
	atomic.AddInt64(&conn.usageCount, 1)
}

// getUsageCount 获取使用计数
func (conn *EnhancedPooledConnection) getUsageCount() int64 {
	return atomic.LoadInt64(&conn.usageCount)
}

// getLastUsed 获取最后使用时间
func (conn *EnhancedPooledConnection) getLastUsed() time.Time {
	conn.mu.RLock()
	defer conn.mu.RUnlock()
	return conn.lastUsed
}

// getCreatedAt 获取创建时间
func (conn *EnhancedPooledConnection) getCreatedAt() time.Time {
	conn.mu.RLock()
	defer conn.mu.RUnlock()
	return conn.createdAt
}

// setLastRebuiltAt 设置上次重建时间
func (conn *EnhancedPooledConnection) setLastRebuiltAt(t time.Time) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	conn.lastRebuiltAt = t
}

// getLastRebuiltAt 获取上次重建时间
func (conn *EnhancedPooledConnection) getLastRebuiltAt() time.Time {
	conn.mu.RLock()
	defer conn.mu.RUnlock()
	return conn.lastRebuiltAt
}

// setLastUsed 设置最后使用时间
func (conn *EnhancedPooledConnection) setLastUsed(t time.Time) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	conn.lastUsed = t
}

// recordRequest 记录请求结果
func (conn *EnhancedPooledConnection) recordRequest(success bool, duration time.Duration) {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	atomic.AddInt64(&conn.totalRequests, 1)
	if !success {
		atomic.AddInt64(&conn.totalErrors, 1)
	}

	// 更新平均响应时间（简单移动平均）
	if conn.totalRequests == 1 {
		conn.avgResponseTime = duration
	} else {
		// 使用加权平均，新样本权重为0.3
		conn.avgResponseTime = time.Duration(float64(conn.avgResponseTime)*0.7 + float64(duration)*0.3)
	}
}

// markForRebuild 标记连接需要重建
func (conn *EnhancedPooledConnection) markForRebuild() bool {
	return atomic.CompareAndSwapInt32(&conn.markedForRebuild, 0, 1)
}

// markForRebuildWithReason 标记连接需要重建并记录原因
func (conn *EnhancedPooledConnection) markForRebuildWithReason(reason string) bool {
	if atomic.CompareAndSwapInt32(&conn.markedForRebuild, 0, 1) {
		conn.mu.Lock()
		conn.rebuildReason = reason
		conn.mu.Unlock()
		return true
	}
	return false
}

// markForRebuildWithReasonLocked 标记连接需要重建并记录原因（需要已持有锁）
func (conn *EnhancedPooledConnection) markForRebuildWithReasonLocked(reason string) bool {
	if atomic.CompareAndSwapInt32(&conn.markedForRebuild, 0, 1) {
		conn.rebuildReason = reason
		return true
	}
	return false
}

// clearRebuildMark 清除重建标记
func (conn *EnhancedPooledConnection) clearRebuildMark() {
	atomic.StoreInt32(&conn.markedForRebuild, 0)
}

// isMarkedForRebuild 检查是否标记为需要重建
func (conn *EnhancedPooledConnection) isMarkedForRebuild() bool {
	return atomic.LoadInt32(&conn.markedForRebuild) == 1
}

// isRebuilding 检查是否正在重建中
func (conn *EnhancedPooledConnection) isRebuilding() bool {
	conn.mu.RLock()
	defer conn.mu.RUnlock()
	return conn.rebuilding
}

// getRebuildReason 获取重建原因
func (conn *EnhancedPooledConnection) getRebuildReason() string {
	conn.mu.RLock()
	defer conn.mu.RUnlock()
	return conn.rebuildReason
}

// validateState 验证连接状态
func (conn *EnhancedPooledConnection) validateState() error {
	conn.mu.RLock()
	defer conn.mu.RUnlock()

	switch conn.state {
	case StateClosed, StateClosing:
		return fmt.Errorf("connection is %s", conn.state)
	default:
		// 检查是否正在重建中（使用管理标记）
		if conn.rebuilding {
			return fmt.Errorf("connection is being rebuilt")
		}
		return nil
	}
}

// transitionStateLocked 转换连接状态（需要持有锁）
func (conn *EnhancedPooledConnection) transitionStateLocked(targetState ConnectionState) bool {
	if !conn.canTransitionTo(targetState) {
		ylog.Debugf("EnhancedPooledConnection", "transitionStateLocked: 无效的状态转换: id=%s, from=%s, to=%s", conn.id, conn.state, targetState)
		return false
	}

	oldState := conn.state
	conn.state = targetState
	ylog.Debugf("EnhancedPooledConnection", "transitionStateLocked: 状态转换成功: id=%s, from=%s, to=%s", conn.id, oldState, targetState)
	return true
}

// canTransitionTo 检查是否可以转换到目标状态
func (conn *EnhancedPooledConnection) canTransitionTo(targetState ConnectionState) bool {
	return CanTransition(conn.state, targetState)
}

// transitionState 转换连接状态（线程安全）
func (conn *EnhancedPooledConnection) transitionState(targetState ConnectionState) bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	return conn.transitionStateLocked(targetState)
}

// beginHealthCheck 开始健康检查
// 尝试将当前状态转换为 StateChecking（通过状态机）
func (conn *EnhancedPooledConnection) beginHealthCheck() bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	ylog.Infof("EnhancedPooledConnection", "beginHealthCheck: 尝试开始健康检查, id=%s, state=%s", conn.id, conn.state)

	// 直接尝试转换到 StateChecking，由状态机决定是否允许
	if !conn.transitionStateLocked(StateChecking) {
		ylog.Warnf("EnhancedPooledConnection", "beginHealthCheck: 状态转换失败, id=%s, from=%s, to=%s", conn.id, conn.state, StateChecking)
		return false
	}

	ylog.Infof("EnhancedPooledConnection", "beginHealthCheck: 成功开始健康检查, id=%s", conn.id)
	return true
}

// completeHealthCheck 完成健康检查
func (conn *EnhancedPooledConnection) completeHealthCheck(success bool) bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	// 只有健康检查中的连接才能完成健康检查
	if conn.state != StateChecking {
		ylog.Warnf("EnhancedPooledConnection", "completeHealthCheck: 连接不在健康检查中: id=%s, state=%s", conn.id, conn.state)
		return false
	}

	// 根据健康检查结果决定目标状态
	var targetState ConnectionState
	if success {
		targetState = StateIdle
	} else {
		targetState = StateClosing
	}

	if !conn.transitionStateLocked(targetState) {
		ylog.Warnf("EnhancedPooledConnection", "completeHealthCheck: 状态转换失败: id=%s, from=%s, to=%s", conn.id, conn.state, targetState)
		return false
	}

	return true
}

// beginRebuild 开始重建连接
func (conn *EnhancedPooledConnection) beginRebuild() bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	// 检查是否已经在重建中
	if conn.rebuilding {
		ylog.Debugf("EnhancedPooledConnection", "beginRebuild: 已在重建中: id=%s", conn.id)
		return false
	}

	// 检查物理状态是否允许开始重建
	// 不能从这些状态开始重建
	if conn.state == StateClosing || conn.state == StateClosed {
		ylog.Debugf("EnhancedPooledConnection", "beginRebuild: 连接状态不适合重建: id=%s, state=%s", conn.id, conn.state)
		return false
	}

	// 设置重建标记（执行阶段）
	conn.rebuilding = true
	// markedForRebuild 保持为 1（决策阶段标记），在完成时根据成功/失败决定是否清除

	ylog.Debugf("EnhancedPooledConnection", "beginRebuild: 成功开始重建: id=%s, state=%s", conn.id, conn.state)
	return true
}

// completeRebuild 完成重建
func (conn *EnhancedPooledConnection) completeRebuild(success bool) {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	if !conn.rebuilding {
		ylog.Warnf("EnhancedPooledConnection", "completeRebuild: 连接不在重建中: id=%s", conn.id)
		return
	}

	// 清除重建标记
	conn.rebuilding = false

	if success {
		// 重建成功：连接已关闭，新连接已创建
		ylog.Infof("EnhancedPooledConnection", "completeRebuild: 重建成功: id=%s", conn.id)
		// 成功时清除决策标记
		atomic.StoreInt32(&conn.markedForRebuild, 0)
	} else {
		// 重建失败：连接可能处于各种状态
		ylog.Warnf("EnhancedPooledConnection", "completeRebuild: 重建失败: id=%s", conn.id)
		// 失败时保持 markedForRebuild = 1，以便可以重试
		// markedForRebuild 已经是 1，不需要修改
	}
}

// beginClose 开始关闭连接
func (conn *EnhancedPooledConnection) beginClose() bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	// 转换为关闭中状态
	if !conn.transitionStateLocked(StateClosing) {
		ylog.Debugf("EnhancedPooledConnection", "beginClose: 状态转换失败: id=%s, from=%s, to=%s", conn.id, conn.state, StateClosing)
		return false
	}

	return true
}

// completeClose 完成关闭连接
func (conn *EnhancedPooledConnection) completeClose() bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	// 只有关闭中的连接才能完成关闭
	if conn.state != StateClosing {
		ylog.Warnf("EnhancedPooledConnection", "completeClose: 连接不在关闭中: id=%s, state=%s", conn.id, conn.state)
		return false
	}

	// 转换为已关闭状态
	if !conn.transitionStateLocked(StateClosed) {
		ylog.Warnf("EnhancedPooledConnection", "completeClose: 状态转换失败: id=%s, from=%s, to=%s", conn.id, conn.state, StateClosed)
		return false
	}

	return true
}

// isAvailable 检查连接是否可用（空闲且健康）
func (conn *EnhancedPooledConnection) isAvailable() bool {
	conn.mu.RLock()
	defer conn.mu.RUnlock()
	return conn.state == StateIdle && conn.healthStatus != HealthStatusUnhealthy
}

// isIdle 检查连接是否空闲（仅状态检查）
func (conn *EnhancedPooledConnection) isIdle() bool {
	conn.mu.RLock()
	defer conn.mu.RUnlock()
	return conn.state == StateIdle
}

// isHealthy 检查连接是否健康
func (conn *EnhancedPooledConnection) isHealthy() bool {
	conn.mu.RLock()
	defer conn.mu.RUnlock()

	// 连接健康的条件：不是不健康状态
	// HealthStatusUnknown 和 HealthStatusDegraded 都认为是健康的
	return conn.healthStatus != HealthStatusUnhealthy
}

// 使用计数相关方法（新增）

// acquireUse 增加使用计数
func (conn *EnhancedPooledConnection) acquireUse() int32 {
	return atomic.AddInt32(&conn.usingCount, 1)
}

// releaseUse 减少使用计数
func (conn *EnhancedPooledConnection) releaseUse() int32 {
	return atomic.AddInt32(&conn.usingCount, -1)
}

// getUseCount 获取使用计数
func (conn *EnhancedPooledConnection) getUseCount() int32 {
	return atomic.LoadInt32(&conn.usingCount)
}

// safeClose 安全关闭连接，等待使用计数归零
func (conn *EnhancedPooledConnection) safeClose() error {
	// 关键：先增加使用计数，防止TOCTOU竞态条件
	conn.acquireUse()
	defer conn.releaseUse()

	// 现在检查是否有其他使用者（使用计数>1表示除了我们之外还有其他人使用）
	useCount := conn.getUseCount()
	if useCount > 1 {
		ylog.Infof("EnhancedPooledConnection", "safeClose: 连接 %s 仍有 %d 个其他使用，等待归零", conn.id, useCount-1)

		// 等待其他使用者完成（最多等待2秒）
		deadline := time.Now().Add(2 * time.Second)
		for conn.getUseCount() > 1 && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}

		// 再次检查，如果仍有其他使用者，返回错误而不关闭
		finalUseCount := conn.getUseCount()
		if finalUseCount > 1 {
			ylog.Warnf("EnhancedPooledConnection",
				"safeClose: 连接 %s 仍有 %d 个其他使用，跳过关闭", conn.id, finalUseCount-1)
			return fmt.Errorf("connection still in use by others: useCount=%d", finalUseCount-1)
		}
	}

	// 执行关闭
	return conn.driver.Close()
}

// forceClose 强制关闭连接（用于清理任务）
//
// 特点：
// - 不管连接是否正在使用（usingCount>0），直接关闭底层连接
// - 不等待使用计数归零
// - 适用于清理僵尸连接（卡住的状态）
//
// 返回值：
// - 如果关闭成功，返回 nil
// - 如果关闭失败，返回 error（不panic）
func (conn *EnhancedPooledConnection) forceClose() error {
	state, _ := conn.getStatus()
	usingCount := conn.getUseCount()

	ylog.Warnf("EnhancedPooledConnection",
		"forceClose: 强制关闭连接: id=%s, state=%s, usingCount=%d",
		conn.id, state, usingCount)

	// 保护：防止 panic
	defer func() {
		if r := recover(); r != nil {
			ylog.Errorf("EnhancedPooledConnection", "forceClose: 关闭连接时发生panic: id=%s, error=%v", conn.id, r)
		}
	}()

	// 直接关闭底层驱动
	// 注意：如果连接正在使用，正在进行的操作会失败并返回错误
	// 这是预期行为：清理任务优先级高于任务执行
	if err := conn.driver.Close(); err != nil {
		ylog.Errorf("EnhancedPooledConnection", "forceClose: 关闭底层驱动失败: id=%s, error=%v", conn.id, err)
		return err
	}

	ylog.Infof("EnhancedPooledConnection", "forceClose: 成功强制关闭连接: id=%s", conn.id)
	return nil
}

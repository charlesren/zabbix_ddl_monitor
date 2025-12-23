package connection

import (
	"fmt"
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

	// 重建相关
	lastRebuiltAt    time.Time // 上次重建时间
	markedForRebuild int32     // 标记是否正在重建，防止并发重建(0=未标记, 1=已标记)

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

	// 标签和元数据
	labels   map[string]string
	metadata map[string]interface{}
}

// tryAcquire 尝试获取连接（非阻塞）
func (conn *EnhancedPooledConnection) tryAcquire() bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	// 第一层检查：使用计数 > 0 的连接不应该被获取（用于清理）
	if conn.getUseCount() > 0 {
		ylog.Infof("EnhancedPooledConnection", "tryAcquire: 连接正在使用中: id=%s, useCount=%d", conn.id, conn.getUseCount())
		return false
	}

	// 检查连接是否可用
	if conn.state != StateIdle {
		ylog.Debugf("EnhancedPooledConnection", "tryAcquire: 连接不可用: id=%s, state=%s", conn.id, conn.state)
		return false
	}

	// 检查健康状态
	if conn.healthStatus == HealthStatusUnhealthy {
		ylog.Debugf("EnhancedPooledConnection", "tryAcquire: 连接不健康: id=%s, health=%s", conn.id, conn.healthStatus)
		return false
	}

	// 转换为使用中状态
	if !conn.transitionStateLocked(StateAcquired) {
		ylog.Debugf("EnhancedPooledConnection", "tryAcquire: 状态转换失败: id=%s, from=%s, to=%s", conn.id, conn.state, StateAcquired)
		return false
	}

	conn.lastUsed = time.Now()
	atomic.AddInt64(&conn.usageCount, 1)
	ylog.Debugf("EnhancedPooledConnection", "tryAcquire: 成功获取连接: id=%s", conn.id)
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

	if success {
		conn.consecutiveFailures = 0
		conn.setHealthLocked(HealthStatusHealthy, true)
		ylog.Debugf("EnhancedPooledConnection", "recordHealthCheck: 健康检查成功: id=%s", conn.id)
	} else {
		conn.consecutiveFailures++
		conn.setHealthLocked(HealthStatusUnhealthy, false)
		ylog.Warnf("EnhancedPooledConnection", "recordHealthCheck: 健康检查失败: id=%s, error=%v, consecutiveFailures=%d", conn.id, err, conn.consecutiveFailures)
	}
}

// setHealthLocked 设置健康状态（需要持有锁）
func (conn *EnhancedPooledConnection) setHealthLocked(status HealthStatus, valid bool) {
	oldStatus := conn.healthStatus
	conn.healthStatus = status

	// 如果连接变得不健康，可能需要关闭
	if status == HealthStatusUnhealthy && valid {
		ylog.Infof("EnhancedPooledConnection", "setHealthLocked: 连接变得不健康: id=%s, old=%s, new=%s", conn.id, oldStatus, status)
		// 标记为需要关闭
		conn.transitionStateLocked(StateClosing)
	}
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

// clearRebuildMark 清除重建标记
func (conn *EnhancedPooledConnection) clearRebuildMark() {
	atomic.StoreInt32(&conn.markedForRebuild, 0)
}

// isMarkedForRebuild 检查是否标记为需要重建
func (conn *EnhancedPooledConnection) isMarkedForRebuild() bool {
	return atomic.LoadInt32(&conn.markedForRebuild) == 1
}

// validateState 验证连接状态
func (conn *EnhancedPooledConnection) validateState() error {
	conn.mu.RLock()
	defer conn.mu.RUnlock()

	switch conn.state {
	case StateClosed, StateClosing:
		return fmt.Errorf("connection is %s", conn.state)
	case StateRebuilding:
		return fmt.Errorf("connection is being rebuilt")
	default:
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
func (conn *EnhancedPooledConnection) beginHealthCheck() bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	// 只有空闲或使用中的连接才能进行健康检查
	if conn.state != StateIdle && conn.state != StateAcquired {
		ylog.Debugf("EnhancedPooledConnection", "beginHealthCheck: 连接状态不适合健康检查: id=%s, state=%s", conn.id, conn.state)
		return false
	}

	// 转换为健康检查中状态
	if !conn.transitionStateLocked(StateChecking) {
		ylog.Debugf("EnhancedPooledConnection", "beginHealthCheck: 状态转换失败: id=%s, from=%s, to=%s", conn.id, conn.state, StateChecking)
		return false
	}

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
	return conn.beginRebuildWithLock()
}

// beginRebuildWithLock 开始重建连接（需要持有锁）
func (conn *EnhancedPooledConnection) beginRebuildWithLock() bool {
	// 只有空闲的连接才能重建
	if conn.state != StateIdle {
		ylog.Debugf("EnhancedPooledConnection", "beginRebuildWithLock: 连接状态不适合重建: id=%s, state=%s", conn.id, conn.state)
		return false
	}

	// 转换为重建中状态
	if !conn.transitionStateLocked(StateRebuilding) {
		ylog.Debugf("EnhancedPooledConnection", "beginRebuildWithLock: 状态转换失败: id=%s, from=%s, to=%s", conn.id, conn.state, StateRebuilding)
		return false
	}

	// 清除重建标记
	conn.clearRebuildMark()
	return true
}

// completeRebuild 完成重建
func (conn *EnhancedPooledConnection) completeRebuild(success bool) bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	// 只有重建中的连接才能完成重建
	if conn.state != StateRebuilding {
		ylog.Warnf("EnhancedPooledConnection", "completeRebuild: 连接不在重建中: id=%s, state=%s", conn.id, conn.state)
		return false
	}

	var targetState ConnectionState
	if success {
		targetState = StateIdle
		conn.lastRebuiltAt = time.Now()
		// 重置使用计数
		atomic.StoreInt64(&conn.usageCount, 0)
		// 重置健康状态
		conn.healthStatus = HealthStatusHealthy
		conn.consecutiveFailures = 0
		ylog.Infof("EnhancedPooledConnection", "completeRebuild: 重建成功: id=%s", conn.id)
	} else {
		targetState = StateClosing
		ylog.Warnf("EnhancedPooledConnection", "completeRebuild: 重建失败: id=%s", conn.id)
	}

	if !conn.transitionStateLocked(targetState) {
		ylog.Warnf("EnhancedPooledConnection", "completeRebuild: 状态转换失败: id=%s, from=%s, to=%s", conn.id, conn.state, targetState)
		return false
	}

	return true
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

// isAvailable 检查连接是否可用
func (conn *EnhancedPooledConnection) isAvailable() bool {
	conn.mu.RLock()
	defer conn.mu.RUnlock()
	return conn.state == StateIdle && conn.healthStatus != HealthStatusUnhealthy
}

// isInUse 检查连接是否在使用中
func (conn *EnhancedPooledConnection) isInUse() bool {
	conn.mu.RLock()
	defer conn.mu.RUnlock()
	return conn.state == StateAcquired
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

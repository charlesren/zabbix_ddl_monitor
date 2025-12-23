package connection

import (
	"context"
	"fmt"
	"math/rand"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charlesren/ylog"
)

// 连接状态枚举
type ConnectionState int

const (
	// StateIdle 空闲状态：连接可用，未被使用
	StateIdle ConnectionState = iota
	// StateAcquired 已获取状态：连接已被获取，正在使用
	StateAcquired
	// StateChecking 检查状态：连接正在接受健康检查
	StateChecking
	// StateRebuilding 重建状态：连接正在被重建
	StateRebuilding
	// StateClosing 关闭中状态：连接正在关闭
	StateClosing
	// StateClosed 已关闭状态：连接已关闭
	StateClosed
)

func (s ConnectionState) String() string {
	switch s {
	case StateIdle:
		return "Idle"
	case StateAcquired:
		return "Acquired"
	case StateChecking:
		return "Checking"
	case StateRebuilding:
		return "Rebuilding"
	case StateClosing:
		return "Closing"
	case StateClosed:
		return "Closed"
	default:
		return "Unknown"
	}
}

// 增强的连接池实现
type EnhancedConnectionPool struct {
	config EnhancedConnectionConfig
	mu     sync.RWMutex

	// 协议工厂和驱动池
	factories map[Protocol]ProtocolFactory
	pools     map[Protocol]*EnhancedDriverPool

	// 生命周期管理
	parentCtx context.Context // 父级上下文，用于协调关闭
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup // 用于同步后台任务

	// 配置参数
	idleTimeout     time.Duration
	maxConnections  int
	minConnections  int
	healthCheckTime time.Duration

	// 指标收集
	collector MetricsCollector

	// 弹性机制
	resilientExecutor *ResilientExecutor

	// 调试和监控
	debugMode   bool
	activeConns map[string]*ConnectionTrace

	// 状态管理
	isShuttingDown int32

	// 事件通知
	eventChan chan PoolEvent

	// 预热状态
	warmupState map[Protocol]*WarmupStatus
}

// GetEventChan 返回连接池事件通道，用于监控连接状态
func (p *EnhancedConnectionPool) GetEventChan() <-chan PoolEvent {
	return p.eventChan
}

// 连接追踪信息
type ConnectionTrace struct {
	ID         string
	Protocol   Protocol
	CreatedAt  time.Time
	LastUsed   time.Time
	UsageCount int64
	StackTrace string
	Labels     map[string]string
}

// 预热状态
type WarmupStatus struct {
	Target    int
	Current   int
	Success   int64
	Failed    int64
	StartTime time.Time
	EndTime   time.Time
	Status    WarmupState
}

type WarmupState int

const (
	WarmupStateNotStarted WarmupState = iota
	WarmupStateInProgress
	WarmupStateCompleted
	WarmupStateFailed
)

// 连接池事件
type PoolEvent struct {
	Type      PoolEventType
	Protocol  Protocol
	Timestamp time.Time
	Data      interface{}
}

type PoolEventType int

const (
	EventConnectionCreated PoolEventType = iota
	EventConnectionDestroyed
	EventConnectionReused
	EventConnectionFailed
	EventHealthCheckFailed
	EventPoolWarmupStarted
	EventPoolWarmupCompleted
	EventPoolShutdown
	EventConnectionRebuilt
)

// 增强的驱动池
type EnhancedDriverPool struct {
	protocol    Protocol
	connections map[string]*EnhancedPooledConnection
	factory     ProtocolFactory
	mu          sync.RWMutex

	// 统计信息
	stats *DriverPoolStats

	// 健康检查
	healthChecker *HealthChecker

	// 连接生命周期管理
	connectionLifecycle *ConnectionLifecycleManager

	// 负载均衡策略
	loadBalancer LoadBalancer
}

// 增强的连接包装
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

	// 标签和元数据
	labels   map[string]string
	metadata map[string]interface{}
}

// tryAcquire 尝试获取连接
// 返回true表示成功获取，false表示连接不可用
func (conn *EnhancedPooledConnection) tryAcquire() bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	// 只有空闲且健康的连接可以被获取
	if conn.state != StateIdle || conn.healthStatus == HealthStatusUnhealthy {
		ylog.Infof("EnhancedConnectionPool", "tryAcquire: 连接不可用: id=%s, state=%s, health=%v",
			conn.id, conn.state, conn.healthStatus)
		return false
	}

	// 执行状态转换
	if !conn.transitionStateLocked(StateAcquired) {
		ylog.Infof("EnhancedConnectionPool", "tryAcquire: 状态转换失败: id=%s, from=%s, to=%s",
			conn.id, conn.state, StateAcquired)
		return false
	}

	conn.setLastUsed(time.Now())
	atomic.AddInt64(&conn.usageCount, 1)
	ylog.Infof("EnhancedConnectionPool", "tryAcquire: 成功获取连接: id=%s, new_state=%s",
		conn.id, conn.state)
	return true
}

// release 释放连接
func (conn *EnhancedPooledConnection) release() {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	// 只有已获取状态的连接可以被释放
	if conn.state == StateAcquired {
		// 执行状态转换
		if !conn.transitionStateLocked(StateIdle) {
			ylog.Warnf("connection_pool", "状态转换失败: id=%s, from=%s, to=%s", conn.id, conn.state, StateIdle)
		}
		conn.setLastUsed(time.Now())
	} else {
		// 记录警告，但继续执行
		ylog.Warnf("connection_pool", "尝试释放非获取状态的连接: id=%s, state=%s", conn.id, conn.state)
	}
}

// getState 获取连接状态（只读）- 保持向后兼容
func (conn *EnhancedPooledConnection) getState() (inUse, valid bool, health HealthStatus) {
	conn.mu.RLock()
	defer conn.mu.RUnlock()

	// 转换状态机状态为旧的布尔值
	inUse = conn.state == StateAcquired
	valid = conn.state != StateClosed && conn.healthStatus != HealthStatusUnhealthy

	return inUse, valid, conn.healthStatus
}

// getStatus 获取完整状态信息（新方法）
func (conn *EnhancedPooledConnection) getStatus() (state ConnectionState, health HealthStatus) {
	conn.mu.RLock()
	defer conn.mu.RUnlock()
	return conn.state, conn.healthStatus
}

// setHealth 设置健康状态
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

	if !success {
		conn.consecutiveFailures++

		// 根据错误类型和连续失败次数调整状态
		errorType := classifyHealthCheckError(err)
		switch errorType {
		case HealthErrorAuth, HealthErrorProtocol:
			// 认证或协议错误立即标记为无效
			conn.setHealthLocked(HealthStatusUnhealthy, false)
			// 认证/协议错误不立即关闭连接，只是标记为不健康
			// 连接将在后续清理过程中被关闭
		default:
			// 其他错误，连续3次失败标记为不健康
			if conn.consecutiveFailures >= 3 {
				conn.setHealthLocked(HealthStatusUnhealthy, false)
			} else {
				conn.setHealthLocked(HealthStatusDegraded, true)
			}
		}
	} else {
		conn.consecutiveFailures = 0
		conn.setHealthLocked(HealthStatusHealthy, true)
		// 健康检查成功，如果当前状态是检查中，则转换为空闲状态
		if conn.state == StateChecking {
			conn.transitionStateLocked(StateIdle)
		}
	}
}

// setHealthLocked 设置健康状态（内部方法，调用者必须持有锁）
func (conn *EnhancedPooledConnection) setHealthLocked(status HealthStatus, valid bool) {
	conn.healthStatus = status

	// 注意：这里不立即设置状态为StateClosing
	// 连接将在后续清理过程中被关闭（cleanupConnections方法）
	// 这样可以避免在健康检查失败时立即中断正在使用的连接

	// 重置连续失败计数
	if status == HealthStatusHealthy {
		conn.consecutiveFailures = 0
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

// setLastRebuiltAt 设置最后重建时间
func (conn *EnhancedPooledConnection) setLastRebuiltAt(t time.Time) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	conn.lastRebuiltAt = t
}

// getLastRebuiltAt 获取最后重建时间
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

// recordRequest 记录请求指标
func (conn *EnhancedPooledConnection) recordRequest(success bool, duration time.Duration) {
	atomic.AddInt64(&conn.totalRequests, 1)
	if !success {
		atomic.AddInt64(&conn.totalErrors, 1)
	}

	// 更新平均响应时间
	if conn.totalRequests > 0 {
		currentAvg := time.Duration(atomic.LoadInt64((*int64)(&conn.avgResponseTime)))
		newAvg := (currentAvg*time.Duration(conn.totalRequests-1) + duration) / time.Duration(conn.totalRequests)
		atomic.StoreInt64((*int64)(&conn.avgResponseTime), int64(newAvg))
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
	return atomic.LoadInt32(&conn.markedForRebuild) != 0
}

// validateState 验证连接状态一致性
func (conn *EnhancedPooledConnection) validateState() error {
	state, health := conn.getStatus()

	// 检查状态一致性
	if state == StateAcquired && health == HealthStatusUnhealthy {
		return fmt.Errorf("connection %s is acquired but unhealthy", conn.id)
	}

	if state == StateClosed && health == HealthStatusHealthy {
		return fmt.Errorf("connection %s is closed but healthy", conn.id)
	}

	if !conn.getLastRebuiltAt().IsZero() && conn.getLastRebuiltAt().Before(conn.getCreatedAt()) {
		return fmt.Errorf("connection %s lastRebuiltAt is before createdAt", conn.id)
	}

	return nil
}

// canTransitionTo 检查是否可以从当前状态转换到目标状态
func (conn *EnhancedPooledConnection) canTransitionTo(targetState ConnectionState) bool {
	currentState := conn.state

	// 状态转换规则
	switch currentState {
	case StateIdle:
		// 空闲状态可以转换到：已获取、检查中、重建中、关闭中、已关闭
		return targetState == StateAcquired || targetState == StateChecking ||
			targetState == StateRebuilding || targetState == StateClosing ||
			targetState == StateClosed
	case StateAcquired:
		// 已获取状态可以转换到：空闲、检查中、关闭中、已关闭
		return targetState == StateIdle || targetState == StateChecking ||
			targetState == StateClosing || targetState == StateClosed
	case StateChecking:
		// 检查中状态可以转换到：空闲、已获取、关闭中、已关闭
		return targetState == StateIdle || targetState == StateAcquired ||
			targetState == StateClosing || targetState == StateClosed
	case StateRebuilding:
		// 重建中状态可以转换到：空闲、关闭中、已关闭
		return targetState == StateIdle || targetState == StateClosing ||
			targetState == StateClosed
	case StateClosing:
		// 关闭中状态只能转换到：已关闭
		return targetState == StateClosed
	case StateClosed:
		// 已关闭状态不能转换到任何其他状态
		return false
	default:
		return false
	}
}

// transitionStateLocked 执行状态转换（内部方法，调用者必须持有锁）
func (conn *EnhancedPooledConnection) transitionStateLocked(targetState ConnectionState) bool {
	if !conn.canTransitionTo(targetState) {
		ylog.Errorf("connection_pool", "无效状态转换: id=%s, from=%s, to=%s", conn.id, conn.state, targetState)
		return false
	}

	oldState := conn.state
	conn.state = targetState

	// 记录状态转换日志
	ylog.Debugf("connection_pool", "状态转换: id=%s, from=%s, to=%s", conn.id, oldState, targetState)

	return true
}

// transitionState 执行状态转换（线程安全版本）
func (conn *EnhancedPooledConnection) transitionState(targetState ConnectionState) bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	return conn.transitionStateLocked(targetState)
}

// beginHealthCheck 开始健康检查
func (conn *EnhancedPooledConnection) beginHealthCheck() bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	// 只有空闲或已获取状态的连接可以开始健康检查
	if conn.state != StateIdle && conn.state != StateAcquired {
		return false
	}

	return conn.transitionStateLocked(StateChecking)
}

// completeHealthCheck 完成健康检查
func (conn *EnhancedPooledConnection) completeHealthCheck(success bool) bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	// 只有在检查中状态的连接可以完成健康检查
	if conn.state != StateChecking {
		return false
	}

	// 根据健康检查结果决定目标状态
	var targetState ConnectionState
	if success {
		// 健康检查成功，返回空闲状态
		targetState = StateIdle
	} else {
		// 健康检查失败，如果连接不健康则标记为关闭中
		if conn.healthStatus == HealthStatusUnhealthy {
			targetState = StateClosing
		} else {
			targetState = StateIdle
		}
	}

	return conn.transitionStateLocked(targetState)
}

// beginRebuild 开始重建连接
func (conn *EnhancedPooledConnection) beginRebuild() bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	// 只有空闲状态的连接可以开始重建
	if conn.state != StateIdle {
		return false
	}

	return conn.transitionStateLocked(StateRebuilding)
}

// beginRebuildWithLock 在已经持有池锁的情况下开始重建
// 调用者必须已经持有池锁，此方法会尝试获取连接锁
// 返回true表示成功获取连接锁并开始重建，false表示失败
func (conn *EnhancedPooledConnection) beginRebuildWithLock() bool {
	// 尝试获取连接锁（带超时，避免死锁）
	lockAcquired := make(chan bool, 1)
	done := make(chan struct{})

	go func() {
		conn.mu.Lock()
		select {
		case lockAcquired <- true:
			// 主goroutine收到了信号，保持锁
			<-done // 等待主goroutine通知释放
		case <-done:
			// 超时了，释放锁
			conn.mu.Unlock()
		}
	}()

	select {
	case <-lockAcquired:
		// 成功获取连接锁
		// 检查连接状态
		if conn.state != StateIdle {
			close(done) // 通知goroutine释放锁
			return false
		}
		close(done) // 通知goroutine可以退出了（锁已转移）
		return conn.transitionStateLocked(StateRebuilding)
	case <-time.After(100 * time.Millisecond):
		// 获取连接锁超时，避免死锁
		close(done) // 通知goroutine释放锁
		ylog.Infof("connection_pool", "获取连接锁超时，放弃重建: id=%s", conn.id)
		return false
	}
}

// completeRebuild 完成重建连接
func (conn *EnhancedPooledConnection) completeRebuild(success bool) bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	// 只有在重建中状态的连接可以完成重建
	if conn.state != StateRebuilding {
		return false
	}

	// 根据重建结果决定目标状态
	var targetState ConnectionState
	if success {
		// 重建成功，返回空闲状态
		targetState = StateIdle
	} else {
		// 重建失败，标记为关闭中
		targetState = StateClosing
	}

	return conn.transitionStateLocked(targetState)
}

// beginClose 开始关闭连接
func (conn *EnhancedPooledConnection) beginClose() bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	// 除了已关闭状态，其他状态都可以开始关闭
	if conn.state == StateClosed {
		return false
	}

	return conn.transitionStateLocked(StateClosing)
}

// completeClose 完成关闭连接
func (conn *EnhancedPooledConnection) completeClose() bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	// 只有在关闭中状态的连接可以完成关闭
	if conn.state != StateClosing {
		return false
	}

	return conn.transitionStateLocked(StateClosed)
}

// isAvailable 检查连接是否可用（空闲且健康）
func (conn *EnhancedPooledConnection) isAvailable() bool {
	conn.mu.RLock()
	defer conn.mu.RUnlock()

	return conn.state == StateIdle && conn.healthStatus != HealthStatusUnhealthy
}

// isInUse 检查连接是否正在使用
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

// 健康状态
type HealthStatus int

const (
	HealthStatusUnknown HealthStatus = iota
	HealthStatusHealthy
	HealthStatusDegraded
	HealthStatusUnhealthy
)

// 健康检查错误类型
type HealthCheckErrorType int

const (
	HealthErrorUnknown HealthCheckErrorType = iota
	HealthErrorTimeout
	HealthErrorNetwork
	HealthErrorAuth
	HealthErrorCommand
	HealthErrorProtocol
)

func (t HealthCheckErrorType) String() string {
	switch t {
	case HealthErrorTimeout:
		return "timeout"
	case HealthErrorNetwork:
		return "network"
	case HealthErrorAuth:
		return "authentication"
	case HealthErrorCommand:
		return "command"
	case HealthErrorProtocol:
		return "protocol"
	default:
		return "unknown"
	}
}

// 驱动池统计信息
type DriverPoolStats struct {
	Protocol             Protocol
	TotalConnections     int64
	ActiveConnections    int64
	IdleConnections      int64
	CreatedConnections   int64
	DestroyedConnections int64
	RebuiltConnections   int64
	RebuildErrors        int64
	ReuseCount           int64
	FailureCount         int64
	HealthCheckCount     int64
	HealthCheckFailures  int64
	AverageResponseTime  time.Duration
	LastActivity         int64
}

// 健康检查器
type HealthChecker struct {
	interval    time.Duration
	timeout     time.Duration
	maxFailures int
	checkFunc   func(driver ProtocolDriver) error
	onUnhealthy func(conn *EnhancedPooledConnection)
	ctx         context.Context
	cancel      context.CancelFunc
}

// 连接生命周期管理器
type ConnectionLifecycleManager struct {
	maxLifetime     time.Duration
	maxIdleTime     time.Duration
	maxUsageCount   int64
	cleanupInterval time.Duration
	ctx             context.Context
	cancel          context.CancelFunc
}

// 负载均衡器接口
type LoadBalancer interface {
	SelectConnection(connections []*EnhancedPooledConnection) *EnhancedPooledConnection
	UpdateConnectionMetrics(conn *EnhancedPooledConnection, responseTime time.Duration, success bool)
}

// 轮询负载均衡器
type RoundRobinBalancer struct {
	counter uint64
}

func (rb *RoundRobinBalancer) SelectConnection(connections []*EnhancedPooledConnection) *EnhancedPooledConnection {
	if len(connections) == 0 {
		return nil
	}

	// 过滤健康的连接
	healthy := make([]*EnhancedPooledConnection, 0, len(connections))
	for _, conn := range connections {
		inUse, valid, health := conn.getState()
		if !inUse && valid && health == HealthStatusHealthy {
			healthy = append(healthy, conn)
		}
	}

	if len(healthy) == 0 {
		return nil
	}

	index := atomic.AddUint64(&rb.counter, 1) % uint64(len(healthy))
	return healthy[index]
}

func (rb *RoundRobinBalancer) UpdateConnectionMetrics(conn *EnhancedPooledConnection, responseTime time.Duration, success bool) {
	atomic.AddInt64(&conn.totalRequests, 1)
	if !success {
		atomic.AddInt64(&conn.totalErrors, 1)
	}

	// 更新平均响应时间
	if conn.totalRequests > 0 {
		currentAvg := time.Duration(atomic.LoadInt64((*int64)(&conn.avgResponseTime)))
		newAvg := (currentAvg*time.Duration(conn.totalRequests-1) + responseTime) / time.Duration(conn.totalRequests)
		atomic.StoreInt64((*int64)(&conn.avgResponseTime), int64(newAvg))
	}
}

// 最少连接负载均衡器
type LeastConnectionsBalancer struct{}

func (lcb *LeastConnectionsBalancer) SelectConnection(connections []*EnhancedPooledConnection) *EnhancedPooledConnection {
	if len(connections) == 0 {
		return nil
	}

	var selected *EnhancedPooledConnection
	minUsage := int64(^uint64(0) >> 1) // max int64

	for _, conn := range connections {
		inUse, valid, health := conn.getState()
		if !inUse && valid && health == HealthStatusHealthy {
			usageCount := conn.getUsageCount()
			if usageCount < minUsage {
				minUsage = usageCount
				selected = conn
			}
		}
	}

	return selected
}

func (lcb *LeastConnectionsBalancer) UpdateConnectionMetrics(conn *EnhancedPooledConnection, responseTime time.Duration, success bool) {
	atomic.AddInt64(&conn.totalRequests, 1)
	if !success {
		atomic.AddInt64(&conn.totalErrors, 1)
	}
}

// NewEnhancedConnectionPool 创建增强的连接池
func NewEnhancedConnectionPool(parentCtx context.Context, config *EnhancedConnectionConfig) *EnhancedConnectionPool {
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithCancel(parentCtx)

	pool := &EnhancedConnectionPool{
		config:          *config,
		factories:       make(map[Protocol]ProtocolFactory),
		pools:           make(map[Protocol]*EnhancedDriverPool),
		parentCtx:       parentCtx,
		ctx:             ctx,
		cancel:          cancel,
		idleTimeout:     config.MaxIdleTime,
		maxConnections:  config.MaxConnections,
		minConnections:  config.MinConnections,
		healthCheckTime: config.HealthCheckTime,
		collector:       GetGlobalMetricsCollector(),
		// 连接建立使用配置中的连接重试策略（后台自动重试）
		// 任务执行的重试策略通过 WithTaskRetryPolicy() 单独配置（前台任务重试）
		resilientExecutor: createConnectionRetryExecutor(config),
		debugMode:         false,
		activeConns:       make(map[string]*ConnectionTrace),
		eventChan:         make(chan PoolEvent, 1000),
		warmupState:       make(map[Protocol]*WarmupStatus),
	}

	// 注册默认工厂
	pool.RegisterFactory(ProtocolSSH, &SSHFactory{})
	pool.RegisterFactory(ProtocolScrapli, &ScrapliFactory{})

	// 启动后台任务
	pool.startBackgroundTasks()

	return pool
}

// createConnectionRetryExecutor 根据配置创建连接重试执行器
func createConnectionRetryExecutor(config *EnhancedConnectionConfig) *ResilientExecutor {
	// 使用配置中的连接重试参数
	// 计算最大尝试次数：maxAttempts = ConnectionMaxRetries + 1
	maxAttempts := config.ConnectionMaxRetries + 1

	retryPolicy := &ExponentialBackoffPolicy{
		BaseDelay:     config.ConnectionRetryInterval,
		MaxDelay:      10 * time.Second, // 连接重试最大延迟10秒
		BackoffFactor: config.ConnectionBackoffFactor,
		MaxAttempts:   maxAttempts,
		Jitter:        true,
	}

	// 计算连接重试总超时：ConnectTimeout × (ConnectionMaxRetries + 1)
	connectionRetryTimeout := config.ConnectTimeout * time.Duration(maxAttempts)
	if connectionRetryTimeout == 0 {
		connectionRetryTimeout = 30 * time.Second // 默认值
	}

	// 创建带重试回调的Retrier，用于记录重试事件
	retrier := NewRetrier(retryPolicy, connectionRetryTimeout).
		WithRetryCallback(func(attempt int, err error) {
			ylog.Debugf("EnhancedConnectionPool", "连接重试 attempt=%d/%d, timeout=%v, error=%v, nextDelay=%v",
				attempt, maxAttempts, connectionRetryTimeout, err, retryPolicy.NextDelay(attempt))
		})

	return NewResilientExecutor().WithRetrier(retrier)
}

// RegisterFactory 注册协议工厂
func (p *EnhancedConnectionPool) RegisterFactory(proto Protocol, factory ProtocolFactory) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.factories[proto] = factory

	// 创建增强的驱动池
	p.pools[proto] = &EnhancedDriverPool{
		protocol:    proto,
		connections: make(map[string]*EnhancedPooledConnection),
		factory:     factory,
		stats:       &DriverPoolStats{Protocol: proto},
		healthChecker: &HealthChecker{
			interval:    p.healthCheckTime,
			timeout:     10 * time.Second,
			maxFailures: 10,
			checkFunc:   p.defaultHealthCheck,
		},
		connectionLifecycle: &ConnectionLifecycleManager{
			maxLifetime:     1 * time.Hour,
			maxIdleTime:     p.idleTimeout,
			maxUsageCount:   1000,
			cleanupInterval: 1 * time.Minute,
		},
		loadBalancer: &RoundRobinBalancer{},
	}

	// 初始化预热状态
	p.warmupState[proto] = &WarmupStatus{
		Status: WarmupStateNotStarted,
	}
}

// GetWithContext 上下文感知的连接获取
func (p *EnhancedConnectionPool) GetWithContext(ctx context.Context, proto Protocol) (ProtocolDriver, error) {
	if atomic.LoadInt32(&p.isShuttingDown) != 0 {
		return nil, fmt.Errorf("connection pool is shutting down")
	}

	// 方案1：如果ctx没有超时，添加默认超时（防止永久阻塞）
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second) // 30秒默认超时
		defer cancel()
		ylog.Debugf("EnhancedConnectionPool", "添加默认超时30秒: protocol=%s", proto)
	}

	// 合并上下文
	mergedCtx := p.mergeContext(ctx)

	// 检查上下文是否已取消
	select {
	case <-mergedCtx.Done():
		return nil, mergedCtx.Err()
	default:
		// 上下文有效，继续获取连接
	}

	pool := p.getDriverPool(proto)
	if pool == nil {
		return nil, fmt.Errorf("unsupported protocol: %s", proto)
	}

	start := time.Now()
	defer func() {
		duration := time.Since(start)
		p.collector.RecordOperationDuration(proto, "get_connection", duration)
		ylog.Infof("EnhancedConnectionPool", "连接获取完成: protocol=%s, duration=%v", proto, duration)
	}()

	// 记录连接获取开始
	ylog.Infof("EnhancedConnectionPool", "开始获取连接: protocol=%s", proto)

	// 使用弹性执行器获取连接（保留重试，移除断路器）
	var conn *EnhancedPooledConnection
	var err error

	ylog.Infof("EnhancedConnectionPool", "开始执行弹性执行器: protocol=%s", proto)
	err = p.resilientExecutor.Execute(mergedCtx, func() error {
		ylog.Infof("EnhancedConnectionPool", "弹性执行器开始执行操作: protocol=%s", proto)
		conn, err = p.getConnectionFromPool(mergedCtx, pool)
		if err != nil {
			ylog.Warnf("EnhancedConnectionPool", "连接获取失败: protocol=%s, error=%v", proto, err)
		} else {
			ylog.Infof("EnhancedConnectionPool", "连接获取成功（内部）: protocol=%s, connection_id=%s", proto, conn.id)
		}
		return err
	})

	if err != nil {
		p.collector.IncrementConnectionsFailed(proto)
		ylog.Errorf("EnhancedConnectionPool", "连接获取最终失败: protocol=%s, error=%v", proto, err)
		return nil, err
	}

	ylog.Infof("EnhancedConnectionPool", "连接获取成功: protocol=%s, connection_id=%s", proto, conn.id)
	return p.activateConnection(conn), nil
}

// Get 获取连接（向后兼容版本）
func (p *EnhancedConnectionPool) Get(proto Protocol) (ProtocolDriver, error) {
	return p.GetWithContext(context.Background(), proto)
}

// getConnectionFromPool 从池中获取连接（重构版：避免死锁）
func (p *EnhancedConnectionPool) getConnectionFromPool(ctx context.Context, pool *EnhancedDriverPool) (*EnhancedPooledConnection, error) {
	// 在获取锁之前检查上下文
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	ylog.Infof("EnhancedConnectionPool", "getConnectionFromPool: 开始获取连接: protocol=%s", pool.protocol)

	// 使用乐观锁策略，最多尝试3次
	maxAttempts := 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		ylog.Debugf("EnhancedConnectionPool", "getConnectionFromPool: 尝试 %d/%d", attempt+1, maxAttempts)

		// 尝试获取空闲连接
		conn, err := p.tryGetIdleConnection(ctx, pool)
		if conn != nil {
			return conn, nil
		}
		if err != nil {
			return nil, err
		}

		// 尝试创建新连接
		conn, err = p.tryCreateNewConnection(ctx, pool)
		if conn != nil {
			return conn, nil
		}
		if err != nil {
			return nil, err
		}

		// 等待一小段时间再重试（避免忙等待）
		if attempt < maxAttempts-1 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(50 * time.Millisecond):
				// 继续下一次尝试
			}
		}
	}

	return nil, fmt.Errorf("failed to get connection after %d attempts", maxAttempts)
}

// tryGetIdleConnection 尝试获取空闲连接（无锁竞争设计）
func (p *EnhancedConnectionPool) tryGetIdleConnection(ctx context.Context, pool *EnhancedDriverPool) (*EnhancedPooledConnection, error) {
	// 第一阶段：快速收集候选连接ID（只读，最小化锁持有时间）
	pool.mu.RLock()
	connectionIDs := make([]string, 0, len(pool.connections))
	for id := range pool.connections {
		connectionIDs = append(connectionIDs, id)
	}
	pool.mu.RUnlock()

	ylog.Infof("EnhancedConnectionPool", "tryGetIdleConnection: 连接池中有 %d 个连接", len(connectionIDs))

	if len(connectionIDs) == 0 {
		ylog.Infof("EnhancedConnectionPool", "tryGetIdleConnection: 连接池为空")
		return nil, nil
	}

	// 第二阶段：尝试获取每个连接（不持有池锁）
	for _, id := range connectionIDs {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// 获取连接引用（需要池读锁，但时间很短）
		pool.mu.RLock()
		conn, exists := pool.connections[id]
		pool.mu.RUnlock()

		if !exists {
			continue
		}

		// 尝试获取连接（不持有池锁）
		if conn.tryAcquire() {
			// 获取成功，检查连接健康状态
			if !conn.isHealthy() {
				ylog.Infof("EnhancedConnectionPool", "tryGetIdleConnection: 连接不健康，释放: id=%s", conn.id)
				conn.release()
				continue
			}

			// 检查连接是否需要重建
			if p.shouldRebuildConnection(conn) {
				ylog.Infof("EnhancedConnectionPool", "tryGetIdleConnection: 连接需要重建，释放并标记: id=%s", conn.id)
				conn.release()
				// 标记连接需要重建，由后台任务处理
				// 避免直接启动goroutine可能被阻塞
				p.markConnectionForRebuildAsync(pool, id, conn)
				continue
			}

			// 更新池统计（需要池写锁，但操作简单快速）
			pool.mu.Lock()
			pool.stats.ActiveConnections++
			pool.stats.IdleConnections--
			pool.mu.Unlock()

			state, _ := conn.getStatus()
			ylog.Infof("EnhancedConnectionPool", "tryGetIdleConnection: 重用健康连接: id=%s, usage=%d, state=%s",
				conn.id, conn.getUsageCount(), state)
			p.collector.IncrementConnectionsReused(pool.protocol)
			return conn, nil
		}
	}

	return nil, nil
}

// tryCreateNewConnection 尝试创建新连接
func (p *EnhancedConnectionPool) tryCreateNewConnection(ctx context.Context, pool *EnhancedDriverPool) (*EnhancedPooledConnection, error) {
	// 检查是否达到最大连接数（需要池读锁）
	pool.mu.RLock()
	currentCount := len(pool.connections)
	pool.mu.RUnlock()

	ylog.Debugf("EnhancedConnectionPool", "tryCreateNewConnection: 当前连接数=%d, 最大连接数=%d", currentCount, p.maxConnections)

	if currentCount >= p.maxConnections {
		ylog.Debugf("EnhancedConnectionPool", "tryCreateNewConnection: 连接池已满: %d/%d", currentCount, p.maxConnections)
		return nil, nil
	}

	ylog.Debugf("EnhancedConnectionPool", "tryCreateNewConnection: 开始创建新连接")
	// 创建新连接（不持有任何锁）
	newConn, err := p.createConnection(ctx, pool)
	if err != nil {
		ylog.Warnf("EnhancedConnectionPool", "tryCreateNewConnection: 创建连接失败: %v", err)
		return nil, err
	}
	ylog.Debugf("EnhancedConnectionPool", "tryCreateNewConnection: 新连接创建成功: id=%s", newConn.id)

	// 将新连接添加到池中（需要池写锁）
	pool.mu.Lock()
	defer pool.mu.Unlock()

	// 双重检查：连接数是否仍然未满
	if len(pool.connections) >= p.maxConnections {
		ylog.Debugf("EnhancedConnectionPool", "tryCreateNewConnection: 连接池在创建过程中已满，关闭新连接")
		newConn.driver.Close()
		return nil, nil
	}

	// 检查是否有其他goroutine已经添加了相同的连接
	// 注意：这里不尝试获取现有连接，避免死锁
	// 如果连接已存在，关闭新创建的连接，让调用者重试
	for _, existingConn := range pool.connections {
		if existingConn.id == newConn.id {
			ylog.Infof("EnhancedConnectionPool", "tryCreateNewConnection: 连接已存在，关闭重复连接: id=%s", newConn.id)
			newConn.driver.Close()
			return nil, nil
		}
	}

	// 添加新连接到池
	pool.connections[newConn.id] = newConn
	atomic.AddInt64(&pool.stats.CreatedConnections, 1)
	atomic.AddInt64(&pool.stats.IdleConnections, 1)
	p.collector.IncrementConnectionsCreated(pool.protocol)

	// 尝试获取新连接
	if newConn.tryAcquire() {
		pool.stats.ActiveConnections++
		pool.stats.IdleConnections--
		ylog.Infof("EnhancedConnectionPool", "tryCreateNewConnection: 创建并获取新连接: id=%s", newConn.id)
		return newConn, nil
	}

	// 不应该发生
	ylog.Errorf("EnhancedConnectionPool", "tryCreateNewConnection: 新创建连接无法获取: id=%s", newConn.id)
	return nil, fmt.Errorf("failed to acquire newly created connection")
}

// createConnection 创建新连接
func (p *EnhancedConnectionPool) createConnection(ctx context.Context, pool *EnhancedDriverPool) (*EnhancedPooledConnection, error) {
	// 检查上下文
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// 使用配置的连接超时
	if p.config.ConnectTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.config.ConnectTimeout)
		defer cancel()
	}

	// 使用带上下文的工厂方法
	driver, err := pool.factory.CreateWithContext(ctx, p.config)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection: %w", err)
	}

	conn := &EnhancedPooledConnection{
		driver:           driver,
		id:               fmt.Sprintf("%s|%s|%d", p.config.Host, pool.protocol, time.Now().UnixNano()),
		protocol:         pool.protocol,
		createdAt:        time.Now(),
		lastUsed:         time.Now(),
		lastRebuiltAt:    time.Time{}, // 初始化为零值，表示从未重建
		markedForRebuild: 0,           // 新连接不应该标记为正在重建
		state:            StateIdle,   // 新连接初始状态为空闲
		healthStatus:     HealthStatusUnknown,
		labels:           make(map[string]string),
		metadata:         make(map[string]interface{}),
	}

	// 复制配置中的标签
	for k, v := range p.config.Labels {
		conn.labels[k] = v
	}

	// 发送事件
	p.sendEvent(EventConnectionCreated, pool.protocol, conn)

	return conn, nil
}

// markConnectionForRebuildAsync 异步标记连接需要重建
func (p *EnhancedConnectionPool) markConnectionForRebuildAsync(pool *EnhancedDriverPool, connID string, conn *EnhancedPooledConnection) {
	// 使用goroutine启动重建，但添加保护机制
	go func() {
		// 添加随机延迟，避免多个重建同时竞争锁
		time.Sleep(time.Duration(rand.Intn(100)) * time.Millisecond)
		p.asyncRebuildConnection(pool, connID, conn)
	}()
}

// asyncRebuildConnection 异步重建连接（重构版：避免死锁）
func (p *EnhancedConnectionPool) asyncRebuildConnection(pool *EnhancedDriverPool, oldID string, oldConn *EnhancedPooledConnection) {
	defer func() {
		if r := recover(); r != nil {
			ylog.Errorf("connection_pool", "async rebuild panic: %v", r)
		}
		oldConn.clearRebuildMark()
	}()

	// 阶段1：快速检查（不持有任何锁）
	if oldConn.isInUse() {
		ylog.Debugf("connection_pool", "连接 %s 正在使用，跳过重建", oldID)
		return
	}

	// 阶段2：获取池锁和连接锁（按正确顺序：先池锁，后连接锁）
	pool.mu.Lock()

	// 检查连接是否还在池中
	if _, exists := pool.connections[oldID]; !exists {
		ylog.Debugf("connection_pool", "连接 %s 已不在池中，取消重建", oldID)
		pool.mu.Unlock()
		return
	}

	// 尝试获取连接锁并开始重建
	if !oldConn.beginRebuildWithLock() {
		ylog.Debugf("connection_pool", "无法开始重建连接 %s", oldID)
		pool.mu.Unlock()
		return
	}
	// 此时持有 pool.mu 和 conn.mu 锁

	// 阶段3：创建新连接（仍然持有锁，但创建连接可能较慢）
	ylog.Debugf("connection_pool", "开始创建新连接以替换 %s", oldID)
	newConn, err := p.createConnection(p.ctx, pool)
	if err != nil {
		ylog.Warnf("connection_pool", "创建新连接失败: %v", err)
		// 我们已经持有连接锁，直接调用transitionStateLocked
		oldConn.transitionStateLocked(StateClosing) // 重建失败，标记为关闭中
		oldConn.mu.Unlock()                         // 释放连接锁
		pool.mu.Unlock()                            // 释放池锁
		return
	}
	newConn.setLastRebuiltAt(time.Now())

	// 执行替换（快速操作）
	delete(pool.connections, oldID)
	pool.connections[newConn.id] = newConn

	// 更新统计
	atomic.AddInt64(&pool.stats.CreatedConnections, 1)
	atomic.AddInt64(&pool.stats.RebuiltConnections, 1)
	p.collector.IncrementConnectionsCreated(pool.protocol)

	ylog.Infof("connection_pool", "异步替换连接: %s→%s", oldID, newConn.id)

	// 发送重建事件
	p.sendEvent(EventConnectionRebuilt, oldConn.protocol, map[string]interface{}{
		"old_connection_id": oldID,
		"new_connection_id": newConn.id,
		"old_usage_count":   oldConn.getUsageCount(),
		"old_age":           time.Since(oldConn.createdAt).String(),
		"reason":            p.getRebuildReason(oldConn),
		"old_state":         oldConn.state.String(),
		"new_state":         newConn.state.String(),
	})

	// 完成旧连接的重建状态转换（我们已经持有连接锁）
	oldConn.transitionStateLocked(StateIdle) // 重建成功，标记为空闲（即将被关闭）

	// 释放锁
	oldConn.mu.Unlock()
	pool.mu.Unlock()

	// 异步关闭旧连接（不持有任何锁）
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ylog.Errorf("connection_pool", "关闭旧连接时发生panic: %v", r)
			}
		}()
		oldConn.driver.Close()
		atomic.AddInt64(&pool.stats.DestroyedConnections, 1)
		oldConn.completeClose()
	}()
}

// Release 释放连接
func (p *EnhancedConnectionPool) Release(driver ProtocolDriver) error {
	monitored, ok := driver.(*MonitoredDriver)
	if !ok {
		return fmt.Errorf("invalid driver type")
	}

	pool := p.getDriverPool(monitored.protocol)
	if pool == nil {
		return fmt.Errorf("protocol not found: %s", monitored.protocol)
	}

	pool.mu.Lock()
	defer pool.mu.Unlock()

	conn := monitored.conn

	// 释放连接
	conn.release()

	// 注意：usageCount已经在activateConnection中增加，这里不再重复增加
	// 只记录当前使用次数用于调试
	currentUsageCount := conn.getUsageCount()
	ylog.Debugf("connection_pool", "连接释放: id=%s, 当前使用次数=%d, 重建阈值=%d",
		conn.id, currentUsageCount, p.config.RebuildMaxUsageCount)

	// 更新统计信息
	atomic.AddInt64(&pool.stats.ActiveConnections, -1)
	atomic.AddInt64(&pool.stats.IdleConnections, 1)

	// 更新性能指标
	responseTime := time.Since(monitored.startTime)
	pool.loadBalancer.UpdateConnectionMetrics(conn, responseTime, monitored.lastError == nil)

	// 记录调试信息
	if p.debugMode {
		p.recordConnectionTrace(conn, "released")
	}

	return nil
}

// WarmUp 预热连接池
func (p *EnhancedConnectionPool) WarmUp(proto Protocol, targetCount int) error {
	pool := p.getDriverPool(proto)
	if pool == nil {
		return fmt.Errorf("protocol %s not registered", proto)
	}

	p.mu.Lock()
	warmup := p.warmupState[proto]
	warmup.Target = targetCount
	warmup.StartTime = time.Now()
	warmup.Status = WarmupStateInProgress
	p.mu.Unlock()

	p.sendEvent(EventPoolWarmupStarted, proto, warmup)

	errChan := make(chan error, targetCount)

	// 限制并发度，避免对设备造成太大压力
	maxConcurrent := 2
	if targetCount < maxConcurrent {
		maxConcurrent = targetCount
	}

	sem := make(chan struct{}, maxConcurrent)

	// 并发创建连接，带重试机制
	for i := 0; i < targetCount; i++ {
		go func(index int) {
			sem <- struct{}{}
			defer func() { <-sem }()

			var err error
			// 重试机制：最多重试2次
			for attempt := 0; attempt < 3; attempt++ {
				if attempt > 0 {
					// 重试前等待一段时间
					waitTime := time.Duration(attempt) * time.Second
					ylog.Debugf("EnhancedConnectionPool", "warmup retry %d for connection %d, waiting %v", attempt, index+1, waitTime)
					time.Sleep(waitTime)
				}

				newConn, err := p.createConnection(p.ctx, pool)
				if err == nil {
					// 将新创建的连接添加到连接池中
					pool.mu.Lock()
					pool.connections[newConn.id] = newConn
					atomic.AddInt64(&pool.stats.CreatedConnections, 1)
					atomic.AddInt64(&pool.stats.IdleConnections, 1)
					pool.mu.Unlock()

					atomic.AddInt64(&warmup.Success, 1)
					break
				}

				ylog.Debugf("EnhancedConnectionPool", "warmup attempt %d failed for connection %d: %v", attempt+1, index+1, err)
			}
			if err != nil {
				atomic.AddInt64(&warmup.Failed, 1)
				ylog.Warnf("EnhancedConnectionPool", "warmup failed for connection %d after retries: %v", index+1, err)
			}
			errChan <- err
		}(i)
	}

	// 等待所有连接创建完成
	var errors []error
	for i := 0; i < targetCount; i++ {
		if err := <-errChan; err != nil {
			errors = append(errors, err)
		}
	}

	// 等待所有goroutine完成
	for i := 0; i < maxConcurrent; i++ {
		sem <- struct{}{}
	}
	close(sem)

	p.mu.Lock()
	warmup.EndTime = time.Now()

	// 计算成功连接数
	successCount := int(atomic.LoadInt64(&warmup.Success))

	// 判断warmup结果：完全成功、部分成功或完全失败
	if successCount == targetCount {
		// 完全成功：所有连接都建立成功
		warmup.Status = WarmupStateCompleted
		ylog.Infof("EnhancedConnectionPool", "warmup successful: %d/%d connections established for protocol %s",
			successCount, targetCount, proto)
	} else if successCount > 0 {
		// 部分成功：至少有一个连接成功
		warmup.Status = WarmupStateCompleted
		ylog.Warnf("EnhancedConnectionPool", "warmup partially successful: %d/%d connections established for protocol %s",
			successCount, targetCount, proto)
	} else {
		// 完全失败：没有任何连接成功
		warmup.Status = WarmupStateFailed
		ylog.Errorf("EnhancedConnectionPool", "warmup failed: 0/%d connections established for protocol %s",
			targetCount, proto)
	}
	p.mu.Unlock()

	p.sendEvent(EventPoolWarmupCompleted, proto, warmup)

	// 如果没有任何连接成功，才返回错误
	if successCount == 0 {
		return fmt.Errorf("warmup failed: 0 connections established out of %d attempts", targetCount)
	}

	// 记录部分成功的详细信息
	if len(errors) > 0 {
		ylog.Warnf("EnhancedConnectionPool", "warmup had %d failures out of %d attempts for protocol %s",
			len(errors), targetCount, proto)
		for i, err := range errors {
			ylog.Debugf("EnhancedConnectionPool", "warmup failure %d: %v", i+1, err)
		}
	}

	return nil
}

// startBackgroundTasks 启动后台任务
func (p *EnhancedConnectionPool) startBackgroundTasks() {
	// 健康检查任务
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.healthCheckTask()
	}()

	// 连接清理任务
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.cleanupTask()
	}()

	// 指标收集任务
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.metricsTask()
	}()

	// 事件处理任务
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.eventHandlerTask()
	}()

	// 空闲连接健康检查任务
	// go p.idleConnectionHealthTask()
}

// idleConnectionHealthTask 空闲连接健康检查任务
func (p *EnhancedConnectionPool) idleConnectionHealthTask() {
	ticker := time.NewTicker(p.healthCheckTime / 2) // 每半次健康检查间隔执行
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.checkIdleConnectionsHealth()
		}
	}
}

// checkIdleConnectionsHealth 检查空闲连接健康状态
func (p *EnhancedConnectionPool) checkIdleConnectionsHealth() {
	for proto, pool := range p.pools {
		pool.mu.RLock()
		idleConnections := make([]*EnhancedPooledConnection, 0)
		now := time.Now()

		for _, conn := range pool.connections {
			if !conn.isInUse() && now.Sub(conn.lastHealthCheck) > p.healthCheckTime/2 {
				idleConnections = append(idleConnections, conn)
			}
		}
		pool.mu.RUnlock()

		// 并发检查空闲连接健康状态
		for _, conn := range idleConnections {
			go p.checkConnectionHealth(proto, conn)
		}
	}
}

// healthCheckTask 健康检查任务
func (p *EnhancedConnectionPool) healthCheckTask() {
	ticker := time.NewTicker(p.healthCheckTime)
	defer ticker.Stop()

	for {
		select {
		case <-p.parentCtx.Done(): // 监听父级上下文
			return
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.performHealthChecks()
		}
	}
}

// performHealthChecks 执行健康检查
func (p *EnhancedConnectionPool) performHealthChecks() {
	// 限制并发检查数量（最多10个）
	const maxConcurrentChecks = 10
	sem := make(chan struct{}, maxConcurrentChecks)

	for proto, pool := range p.pools {
		// 快速收集需要检查的连接（使用读锁，最小化持有时间）
		pool.mu.RLock()
		connections := make([]*EnhancedPooledConnection, 0, len(pool.connections))
		for _, conn := range pool.connections {
			if !conn.isInUse() {
				connections = append(connections, conn)
			}
		}
		pool.mu.RUnlock()

		if len(connections) == 0 {
			continue
		}

		ylog.Debugf("connection_pool", "准备健康检查 %d 个连接: protocol=%s", len(connections), proto)

		// 并发进行健康检查，但有限制
		for _, conn := range connections {
			sem <- struct{}{}
			go func(proto Protocol, conn *EnhancedPooledConnection) {
				defer func() { <-sem }()
				p.checkConnectionHealth(proto, conn)
			}(proto, conn)
		}
	}
}

// checkConnectionHealth 检查单个连接健康状态
func (p *EnhancedConnectionPool) checkConnectionHealth(proto Protocol, conn *EnhancedPooledConnection) {
	// 检查连接是否正在使用，跳过正在使用的连接
	if conn.isInUse() {
		return
	}

	// 开始健康检查（设置状态为检查中）
	if !conn.beginHealthCheck() {
		state, _ := conn.getStatus()
		ylog.Debugf("pool", "无法开始健康检查: connection %s state=%s", conn.id, state)
		return
	}

	start := time.Now()
	err := p.defaultHealthCheck(conn.driver)
	duration := time.Since(start)

	success := err == nil
	conn.recordHealthCheck(success, err)

	// 记录指标和事件
	if !success {
		errorType := classifyHealthCheckError(err)

		// 根据错误类型记录不同级别的日志
		switch errorType {
		case HealthErrorTimeout:
			ylog.Warnf("pool", "connection %s health check timeout: %v", conn.id, err)
		case HealthErrorNetwork:
			ylog.Warnf("pool", "connection %s network error: %v", conn.id, err)
		case HealthErrorAuth:
			ylog.Errorf("pool", "connection %s authentication error: %v", conn.id, err)
		case HealthErrorCommand:
			ylog.Warnf("pool", "connection %s command error: %v", conn.id, err)
		case HealthErrorProtocol:
			ylog.Errorf("pool", "connection %s protocol error: %v", conn.id, err)
		default:
			ylog.Warnf("pool", "connection %s health check failed: %v", conn.id, err)
		}

		p.collector.IncrementHealthCheckFailed(proto)
		state, _ := conn.getStatus()
		p.sendEvent(EventHealthCheckFailed, proto, map[string]interface{}{
			"connection_id": conn.id,
			"error":         err.Error(),
			"error_type":    errorType.String(),
			"state":         state.String(),
		})
	} else {
		p.collector.IncrementHealthCheckSuccess(proto)
	}

	p.collector.RecordHealthCheckDuration(proto, duration)
}

// classifyHealthCheckError 分类健康检查错误
func classifyHealthCheckError(err error) HealthCheckErrorType {
	if err == nil {
		return HealthErrorUnknown
	}

	errStr := err.Error()
	switch {
	case strings.Contains(errStr, "timeout") || strings.Contains(errStr, "deadline") ||
		strings.Contains(errStr, "context deadline exceeded"):
		return HealthErrorTimeout
	case strings.Contains(errStr, "network") || strings.Contains(errStr, "connection") ||
		strings.Contains(errStr, "reset by peer") || strings.Contains(errStr, "broken pipe"):
		return HealthErrorNetwork
	case strings.Contains(errStr, "authentication") || strings.Contains(errStr, "password") ||
		strings.Contains(errStr, "login") || strings.Contains(errStr, "auth"):
		return HealthErrorAuth
	case strings.Contains(errStr, "command") || strings.Contains(errStr, "syntax") ||
		strings.Contains(errStr, "invalid command"):
		return HealthErrorCommand
	case strings.Contains(errStr, "protocol") || strings.Contains(errStr, "unsupported"):
		return HealthErrorProtocol
	default:
		return HealthErrorUnknown
	}
}

// defaultHealthCheck 默认健康检查
func (p *EnhancedConnectionPool) defaultHealthCheck(driver ProtocolDriver) error {
	// 使用配置的超时时间，默认5秒
	timeout := p.config.HealthCheckTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// 平台适配的健康检查命令
	var cmd string
	if scrapliDriver, ok := driver.(*ScrapliDriver); ok {
		// Scrapli驱动使用GetPrompt（已有超时控制）
		_, err := scrapliDriver.GetPromptWithContext(ctx)
		return err
	} else {
		// 其他驱动使用通用命令
		// 尝试show clock，大多数网络设备都支持
		cmd = "show clock"
	}

	_, err := driver.Execute(ctx, &ProtocolRequest{
		CommandType: CommandTypeCommands,
		Payload:     []string{cmd},
	})
	return err
}

// cleanupTask 连接清理任务
func (p *EnhancedConnectionPool) cleanupTask() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-p.parentCtx.Done(): // 监听父级上下文
			return
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.cleanupConnections()
		}
	}
}

// cleanupConnections 清理过期连接
func (p *EnhancedConnectionPool) cleanupConnections() {
	now := time.Now()

	for proto, pool := range p.pools {
		// 第一阶段：收集所有连接ID（只读，快速）
		pool.mu.RLock()
		allIDs := make([]string, 0, len(pool.connections))
		for id := range pool.connections {
			allIDs = append(allIDs, id)
		}
		pool.mu.RUnlock()

		if len(allIDs) == 0 {
			continue
		}

		ylog.Debugf("connection_pool", "准备检查 %d 个连接是否需要清理: protocol=%s", len(allIDs), proto)

		// 第二阶段：逐个检查并清理（不持有池锁）
		removedCount := 0
		for _, id := range allIDs {
			// 获取连接（需要池读锁，但时间短）
			pool.mu.RLock()
			conn, exists := pool.connections[id]
			pool.mu.RUnlock()

			if !exists {
				continue
			}

			// 检查是否需要清理（不持有池锁）
			if p.shouldCleanupConnection(conn, now) {
				// 尝试获取连接
				if conn.tryAcquire() {
					// 获取池写锁进行删除（时间短）
					pool.mu.Lock()
					// 再次检查连接是否还在
					if _, stillExists := pool.connections[id]; stillExists {
						delete(pool.connections, id)
						pool.mu.Unlock()

						// 异步关闭连接
						go func(c *EnhancedPooledConnection) {
							defer func() {
								if r := recover(); r != nil {
									ylog.Errorf("connection_pool", "清理连接时发生panic: %v", r)
								}
							}()

							c.driver.Close()
							atomic.AddInt64(&pool.stats.DestroyedConnections, 1)
							p.collector.IncrementConnectionsDestroyed(proto)
							p.sendEvent(EventConnectionDestroyed, proto, c)
							ylog.Debugf("connection_pool", "连接清理完成: id=%s, protocol=%s", c.id, proto)
						}(conn)
						removedCount++
					} else {
						pool.mu.Unlock()
						// 连接已被删除，释放获取的连接
						conn.release()
					}
				}
			}
		}

		if removedCount > 0 {
			ylog.Infof("connection_pool", "清理完成: 移除了 %d 个连接, protocol=%s", removedCount, proto)
		}
	}
}

// shouldCleanupConnection 判断是否应该清理连接
func (p *EnhancedConnectionPool) shouldCleanupConnection(conn *EnhancedPooledConnection, now time.Time) bool {
	// 使用线程安全的方法获取连接状态
	inUse, valid, health := conn.getState()

	// 连接正在使用，不应该清理
	if inUse {
		return false
	}

	// 检查空闲时间
	lastUsed := conn.getLastUsed()
	createdAt := conn.getCreatedAt()
	usageCount := conn.getUsageCount()

	if now.Sub(lastUsed) > p.idleTimeout {
		return true
	}

	// 检查连接生命周期
	lifecycle := p.pools[conn.protocol].connectionLifecycle
	if now.Sub(createdAt) > lifecycle.maxLifetime {
		return true
	}

	// 检查使用次数
	if usageCount > lifecycle.maxUsageCount {
		return true
	}

	// 检查健康状态
	if health == HealthStatusUnhealthy {
		return true
	}

	// 检查连接有效性
	if !valid {
		return true
	}

	return false
}

// shouldRebuildConnection 判断连接是否需要智能重建
func (p *EnhancedConnectionPool) shouldRebuildConnection(conn *EnhancedPooledConnection) bool {
	if !p.config.SmartRebuildEnabled {
		ylog.Debugf("connection_pool", "shouldRebuildConnection: SmartRebuildEnabled=false")
		return false
	}

	now := time.Now()

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

	if now.Sub(lastRebuildOrCreate) < p.config.RebuildMinInterval {
		ylog.Debugf("connection_pool", "重建间隔太短: id=%s, interval=%v, min=%v",
			conn.id, now.Sub(lastRebuildOrCreate), p.config.RebuildMinInterval)
		return false
	}

	// 检查连接状态，避免重建正在使用或无效的连接
	state, _ := conn.getStatus()
	if conn.isInUse() || state == StateRebuilding || state == StateClosing || state == StateClosed {
		ylog.Debugf("connection_pool", "连接状态不适合重建: id=%s, state=%s", conn.id, state)
		return false
	}

	// 检查健康状态
	if !conn.isHealthy() {
		ylog.Debugf("connection_pool", "连接不健康，跳过重建: id=%s", conn.id)
		return false
	}

	// 检查是否已经标记为正在重建，防止并发重建
	// 使用原子操作确保只有一个goroutine能将连接标记为重建
	if conn.isMarkedForRebuild() {
		ylog.Debugf("connection_pool", "连接已标记为正在重建: id=%s", conn.id)
		return false
	}

	// 调试：打印连接详细信息
	usageCount := conn.getUsageCount()
	markedForRebuild := conn.isMarkedForRebuild()
	state, health := conn.getStatus()
	ylog.Debugf("connection_pool", "检查连接重建条件: id=%s, state=%s, health=%v, usage=%d/%d, createdAt=%v, lastRebuiltAt=%v, strategy=%s, marked=%v, minInterval=%v, actualInterval=%v",
		conn.id, state, health, usageCount, p.config.RebuildMaxUsageCount, createdAt, lastRebuiltAt, p.config.RebuildStrategy, markedForRebuild, p.config.RebuildMinInterval, now.Sub(lastRebuildOrCreate))

	// 直接输出到标准输出，确保调试信息可见
	ylog.Debugf("connection_pool", "shouldRebuildConnection: id=%s, state=%s, health=%v, usage=%d/%d, strategy=%s, marked=%v, shouldRebuild=%v", conn.id, state, health, usageCount, p.config.RebuildMaxUsageCount, p.config.RebuildStrategy, markedForRebuild, usageCount >= p.config.RebuildMaxUsageCount)

	// 根据策略判断
	switch p.config.RebuildStrategy {
	case "usage":
		// 仅基于使用次数
		usageCount := conn.getUsageCount()
		shouldRebuild := usageCount >= p.config.RebuildMaxUsageCount
		if shouldRebuild {
			// 使用原子操作确保只有一个goroutine能将连接标记为重建
			if conn.markForRebuild() {
				ylog.Debugf("connection_pool", "usage策略检查: id=%s, usage=%d, max=%d, rebuild=%v (已标记)",
					conn.id, usageCount, p.config.RebuildMaxUsageCount, shouldRebuild)
				return true
			} else {
				ylog.Debugf("connection_pool", "usage策略检查: id=%s, usage=%d, max=%d, rebuild=%v (已被其他goroutine标记)",
					conn.id, usageCount, p.config.RebuildMaxUsageCount, shouldRebuild)
				return false
			}
		}
		ylog.Debugf("connection_pool", "usage策略检查: id=%s, usage=%d, max=%d, rebuild=%v",
			conn.id, usageCount, p.config.RebuildMaxUsageCount, shouldRebuild)
		return shouldRebuild

	case "age":
		// 仅基于连接年龄
		age := now.Sub(conn.getCreatedAt())
		shouldRebuild := age >= p.config.RebuildMaxAge
		if shouldRebuild {
			// 使用原子操作确保只有一个goroutine能将连接标记为重建
			if conn.markForRebuild() {
				ylog.Debugf("connection_pool", "age策略检查: id=%s, age=%v, max=%v, rebuild=%v (已标记)",
					conn.id, age, p.config.RebuildMaxAge, shouldRebuild)
				return true
			} else {
				ylog.Debugf("connection_pool", "age策略检查: id=%s, age=%v, max=%v, rebuild=%v (已被其他goroutine标记)",
					conn.id, age, p.config.RebuildMaxAge, shouldRebuild)
				return false
			}
		}
		ylog.Debugf("connection_pool", "age策略检查: id=%s, age=%v, max=%v, rebuild=%v",
			conn.id, age, p.config.RebuildMaxAge, shouldRebuild)
		return shouldRebuild

	case "error":
		// 仅基于错误率
		totalRequests := atomic.LoadInt64(&conn.totalRequests)
		if totalRequests >= p.config.RebuildMinRequestsForErrorRate {
			totalErrors := atomic.LoadInt64(&conn.totalErrors)
			if totalRequests > 0 { // 防止除零错误
				// 使用浮点数计算确保精度
				errorRate := float64(totalErrors) / float64(totalRequests)
				shouldRebuild := errorRate >= p.config.RebuildMaxErrorRate
				if shouldRebuild {
					// 使用原子操作确保只有一个goroutine能将连接标记为重建
					if atomic.CompareAndSwapInt32(&conn.markedForRebuild, 0, 1) {
						ylog.Debugf("connection_pool", "error策略检查: id=%s, requests=%d, errors=%d, rate=%.2f, max=%.2f, rebuild=%v (已标记)",
							conn.id, totalRequests, totalErrors, errorRate, p.config.RebuildMaxErrorRate, shouldRebuild)
						return true
					} else {
						ylog.Debugf("connection_pool", "error策略检查: id=%s, requests=%d, errors=%d, rate=%.2f, max=%.2f, rebuild=%v (已被其他goroutine标记)",
							conn.id, totalRequests, totalErrors, errorRate, p.config.RebuildMaxErrorRate, shouldRebuild)
						return false
					}
				}
				ylog.Debugf("connection_pool", "error策略检查: id=%s, requests=%d, errors=%d, rate=%.2f, max=%.2f, rebuild=%v",
					conn.id, totalRequests, totalErrors, errorRate, p.config.RebuildMaxErrorRate, shouldRebuild)
				return shouldRebuild
			}
		}
		ylog.Debugf("connection_pool", "error策略检查: id=%s, requests=%d (太少)，跳过", conn.id, totalRequests)
		return false

	case "all":
		// 满足所有条件
		conditions := 0
		usageCount := atomic.LoadInt64(&conn.usageCount)
		if usageCount >= p.config.RebuildMaxUsageCount {
			conditions++
			ylog.Debugf("connection_pool", "all策略: id=%s, usage条件满足: %d >= %d",
				conn.id, usageCount, p.config.RebuildMaxUsageCount)
		}

		age := now.Sub(conn.getCreatedAt())
		if age >= p.config.RebuildMaxAge {
			conditions++
			ylog.Debugf("connection_pool", "all策略: id=%s, age条件满足: %v >= %v",
				conn.id, age, p.config.RebuildMaxAge)
		}

		totalRequests := atomic.LoadInt64(&conn.totalRequests)
		if totalRequests >= p.config.RebuildMinRequestsForErrorRate {
			totalErrors := atomic.LoadInt64(&conn.totalErrors)
			// 使用浮点数计算确保精度
			errorRate := float64(totalErrors) / float64(totalRequests)
			if errorRate >= p.config.RebuildMaxErrorRate {
				conditions++
				ylog.Debugf("connection_pool", "all策略: id=%s, error条件满足: %.2f >= %.2f",
					conn.id, errorRate, p.config.RebuildMaxErrorRate)
			}
		}

		shouldRebuild := conditions == 3
		if shouldRebuild {
			// 使用原子操作确保只有一个goroutine能将连接标记为重建
			if atomic.CompareAndSwapInt32(&conn.markedForRebuild, 0, 1) {
				ylog.Debugf("connection_pool", "all策略检查: id=%s, conditions=%d/3, rebuild=%v (已标记)",
					conn.id, conditions, shouldRebuild)
				return true
			} else {
				ylog.Debugf("connection_pool", "all策略检查: id=%s, conditions=%d/3, rebuild=%v (已被其他goroutine标记)",
					conn.id, conditions, shouldRebuild)
				return false
			}
		}
		ylog.Debugf("connection_pool", "all策略检查: id=%s, conditions=%d/3, rebuild=%v",
			conn.id, conditions, shouldRebuild)
		return shouldRebuild

	case "any": // 默认策略
		fallthrough
	default:
		// 满足任意条件
		usageCount := atomic.LoadInt64(&conn.usageCount)
		if usageCount >= p.config.RebuildMaxUsageCount {
			// 使用原子操作确保只有一个goroutine能将连接标记为重建
			if atomic.CompareAndSwapInt32(&conn.markedForRebuild, 0, 1) {
				ylog.Debugf("connection_pool", "any策略: 连接需要重建: id=%s, 使用次数达到%d >= %d (已标记)",
					conn.id, usageCount, p.config.RebuildMaxUsageCount)
				return true
			} else {
				ylog.Debugf("connection_pool", "any策略: 连接需要重建: id=%s, 使用次数达到%d >= %d (已被其他goroutine标记)",
					conn.id, usageCount, p.config.RebuildMaxUsageCount)
				return false
			}
		}

		age := now.Sub(conn.getCreatedAt())
		if age >= p.config.RebuildMaxAge {
			// 使用原子操作确保只有一个goroutine能将连接标记为重建
			if atomic.CompareAndSwapInt32(&conn.markedForRebuild, 0, 1) {
				ylog.Debugf("connection_pool", "any策略: 连接需要重建: id=%s, 年龄达到%v >= %v (已标记)",
					conn.id, age, p.config.RebuildMaxAge)
				return true
			} else {
				ylog.Debugf("connection_pool", "any策略: 连接需要重建: id=%s, 年龄达到%v >= %v (已被其他goroutine标记)",
					conn.id, age, p.config.RebuildMaxAge)
				return false
			}
		}

		totalRequests := atomic.LoadInt64(&conn.totalRequests)
		if totalRequests >= p.config.RebuildMinRequestsForErrorRate {
			totalErrors := atomic.LoadInt64(&conn.totalErrors)
			if totalRequests > 0 { // 防止除零错误
				// 使用浮点数计算确保精度
				errorRate := float64(totalErrors) / float64(totalRequests)
				if errorRate >= p.config.RebuildMaxErrorRate {
					// 使用原子操作确保只有一个goroutine能将连接标记为重建
					if atomic.CompareAndSwapInt32(&conn.markedForRebuild, 0, 1) {
						ylog.Debugf("connection_pool", "any策略: 连接需要重建: id=%s, 错误率%.4f >= %.4f (已标记)",
							conn.id, errorRate, p.config.RebuildMaxErrorRate)
						return true
					} else {
						ylog.Debugf("connection_pool", "any策略: 连接需要重建: id=%s, 错误率%.4f >= %.4f (已被其他goroutine标记)",
							conn.id, errorRate, p.config.RebuildMaxErrorRate)
						return false
					}
				}
			}
		} else if totalRequests > 0 {
			// 请求数太少，不计算错误率，记录日志
			ylog.Debugf("connection_pool", "any策略: 连接请求数太少不计算错误率: id=%s, requests=%d < %d",
				conn.id, totalRequests, p.config.RebuildMinRequestsForErrorRate)
		}

		ylog.Debugf("connection_pool", "any策略: 连接不需要重建: id=%s, usage=%d/%d, age=%v/%v",
			conn.id, usageCount, p.config.RebuildMaxUsageCount, age, p.config.RebuildMaxAge)
		return false
	}
}

// metricsTask 指标收集任务
func (p *EnhancedConnectionPool) metricsTask() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.parentCtx.Done(): // 监听父级上下文
			return
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.updateMetrics()
		}
	}
}

// updateMetrics 更新指标
func (p *EnhancedConnectionPool) updateMetrics() {
	for proto, pool := range p.pools {
		pool.mu.RLock()

		var active, idle, total int64
		for _, conn := range pool.connections {
			total++
			if conn.isInUse() {
				active++
			} else {
				idle++
			}
		}

		pool.mu.RUnlock()

		p.collector.SetActiveConnections(proto, active)
		p.collector.SetIdleConnections(proto, idle)
		p.collector.SetPoolSize(proto, total)

		// 更新池统计信息
		atomic.StoreInt64(&pool.stats.TotalConnections, total)
		atomic.StoreInt64(&pool.stats.ActiveConnections, active)
		atomic.StoreInt64(&pool.stats.IdleConnections, idle)
		atomic.StoreInt64(&pool.stats.LastActivity, time.Now().Unix())
	}
}

// eventHandlerTask 事件处理任务
func (p *EnhancedConnectionPool) eventHandlerTask() {
	for {
		select {
		case <-p.ctx.Done():
			// 池上下文取消，等待事件通道关闭
			// 继续循环，直到事件通道关闭
		case event, ok := <-p.eventChan:
			if !ok {
				// 事件通道已关闭，退出任务
				return
			}
			p.handleEvent(event)
		}
	}
}

// handleEvent 处理事件
func (p *EnhancedConnectionPool) handleEvent(event PoolEvent) {
	// 格式化事件信息，提供更友好的显示
	var eventStr string

	switch event.Type {
	case EventConnectionCreated:
		eventStr = fmt.Sprintf("连接创建: protocol=%s", event.Protocol)
	case EventConnectionDestroyed:
		eventStr = fmt.Sprintf("连接销毁: protocol=%s", event.Protocol)
	case EventConnectionReused:
		eventStr = fmt.Sprintf("连接重用: protocol=%s", event.Protocol)
	case EventConnectionFailed:
		eventStr = fmt.Sprintf("连接失败: protocol=%s", event.Protocol)
	case EventHealthCheckFailed:
		eventStr = fmt.Sprintf("健康检查失败: protocol=%s", event.Protocol)
	case EventPoolWarmupStarted:
		eventStr = fmt.Sprintf("连接池预热开始: protocol=%s", event.Protocol)
	case EventPoolWarmupCompleted:
		eventStr = fmt.Sprintf("连接池预热完成: protocol=%s", event.Protocol)
	case EventPoolShutdown:
		eventStr = fmt.Sprintf("连接池关闭: protocol=%s", event.Protocol)
	case EventConnectionRebuilt:
		// 特殊处理重建事件，格式化Data字段
		if data, ok := event.Data.(map[string]interface{}); ok {
			eventStr = fmt.Sprintf("连接重建: protocol=%s, old_id=%v, new_id=%v, reason=%v",
				event.Protocol,
				data["old_connection_id"],
				data["new_connection_id"],
				data["reason"])
		} else {
			eventStr = fmt.Sprintf("连接重建: protocol=%s, data=%v", event.Protocol, event.Data)
		}
	default:
		eventStr = fmt.Sprintf("未知事件(type=%d): protocol=%s", event.Type, event.Protocol)
	}

	// 记录事件，包含时间戳
	ylog.Infof("PoolEvent", "%s (timestamp: %s)", eventStr, event.Timestamp.Format("2006-01-02 15:04:05.000"))
}

// sendEvent 发送事件
func (p *EnhancedConnectionPool) sendEvent(eventType PoolEventType, protocol Protocol, data interface{}) {
	// 检查连接池是否正在关闭
	if atomic.LoadInt32(&p.isShuttingDown) == 1 {
		return
	}

	// 使用 defer recover 保护，避免向已关闭的通道发送导致 panic
	defer func() {
		if r := recover(); r != nil {
			// 通道已关闭，忽略发送失败
			ylog.Debugf("PoolEvent", "事件通道已关闭，忽略事件发送: type=%d, protocol=%s", eventType, protocol)
		}
	}()

	select {
	case p.eventChan <- PoolEvent{
		Type:      eventType,
		Protocol:  protocol,
		Timestamp: time.Now(),
		Data:      data,
	}:
		// 事件发送成功，记录调试信息
		ylog.Debugf("PoolEvent", "事件发送成功: type=%d, protocol=%s", eventType, protocol)
	default:
		// 事件通道已满，丢弃事件
		ylog.Warnf("PoolEvent", "事件通道已满，丢弃事件: type=%d, protocol=%s", eventType, protocol)
	}
}

// recordConnectionTrace 记录连接追踪信息
func (p *EnhancedConnectionPool) recordConnectionTrace(conn *EnhancedPooledConnection, action string) {
	if !p.debugMode {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	trace := &ConnectionTrace{
		ID:         conn.id,
		Protocol:   conn.protocol,
		CreatedAt:  conn.createdAt,
		LastUsed:   conn.lastUsed,
		UsageCount: conn.usageCount,
		StackTrace: string(debug.Stack()),
		Labels:     make(map[string]string),
	}

	for k, v := range conn.labels {
		trace.Labels[k] = v
	}
	trace.Labels["action"] = action

	p.activeConns[conn.id] = trace
}

// getDriverPool 获取驱动池
func (p *EnhancedConnectionPool) getDriverPool(proto Protocol) *EnhancedDriverPool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pools[proto]
}

// EnableDebug 开启调试模式
func (p *EnhancedConnectionPool) EnableDebug() {
	p.debugMode = true
}

// GetFactory 获取协议工厂（用于测试）
func (p *EnhancedConnectionPool) GetFactory(proto Protocol) ProtocolFactory {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.factories[proto]
}

// CheckLeaks 检查连接泄漏（用于测试和调试）
func (p *EnhancedConnectionPool) CheckLeaks() []string {
	if !p.debugMode {
		return nil
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	var leaks []string
	for id, trace := range p.activeConns {
		leaks = append(leaks, fmt.Sprintf("LEAK: connection %s created at %v, last used %v, usage count %d",
			id, trace.CreatedAt, trace.LastUsed, trace.UsageCount))
	}

	return leaks
}

// DisableDebug 关闭调试模式
func (p *EnhancedConnectionPool) DisableDebug() {
	p.debugMode = false
}

// GetStats 获取统计信息
func (p *EnhancedConnectionPool) GetStats() map[Protocol]*DriverPoolStats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	stats := make(map[Protocol]*DriverPoolStats)
	for proto, pool := range p.pools {
		// 创建统计信息副本
		poolStats := *pool.stats
		stats[proto] = &poolStats
	}

	return stats
}

// GetWarmupStatus 获取预热状态
func (p *EnhancedConnectionPool) GetWarmupStatus() map[Protocol]*WarmupStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()

	status := make(map[Protocol]*WarmupStatus)
	for proto, warmup := range p.warmupState {
		// 创建状态副本
		warmupCopy := *warmup
		status[proto] = &warmupCopy
	}

	return status
}

// getRebuildReason 获取重建原因
func (p *EnhancedConnectionPool) getRebuildReason(conn *EnhancedPooledConnection) string {
	now := time.Now()

	// 按优先级检查各个条件，返回最主要的原因
	usageCount := atomic.LoadInt64(&conn.usageCount)
	if usageCount >= p.config.RebuildMaxUsageCount {
		return fmt.Sprintf("usage_exceeded(%d>=%d)", usageCount, p.config.RebuildMaxUsageCount)
	}

	age := now.Sub(conn.getCreatedAt())
	if age >= p.config.RebuildMaxAge {
		return fmt.Sprintf("age_exceeded(%v>=%v)", age, p.config.RebuildMaxAge)
	}

	totalRequests := atomic.LoadInt64(&conn.totalRequests)
	if totalRequests >= p.config.RebuildMinRequestsForErrorRate {
		totalErrors := atomic.LoadInt64(&conn.totalErrors)
		if totalRequests > 0 { // 防止除零错误
			// 使用浮点数计算确保精度，但保持与测试一致的格式化
			errorRate := float64(totalErrors) / float64(totalRequests)
			if errorRate >= p.config.RebuildMaxErrorRate {
				return fmt.Sprintf("error_rate_exceeded(%.2f>=%.2f)", errorRate, p.config.RebuildMaxErrorRate)
			}
		}
	}

	// 如果没有明确的原因，返回所有满足的条件
	reasons := []string{}
	if usageCount >= p.config.RebuildMaxUsageCount {
		reasons = append(reasons, fmt.Sprintf("usage(%d)", usageCount))
	}
	if age >= p.config.RebuildMaxAge {
		reasons = append(reasons, fmt.Sprintf("age(%v)", age))
	}
	if totalRequests >= p.config.RebuildMinRequestsForErrorRate &&
		float64(atomic.LoadInt64(&conn.totalErrors))/float64(totalRequests) >= p.config.RebuildMaxErrorRate {
		errorRate := float64(atomic.LoadInt64(&conn.totalErrors)) / float64(totalRequests)
		reasons = append(reasons, fmt.Sprintf("error_rate(%.2f)", errorRate))
	}

	if len(reasons) > 0 {
		return strings.Join(reasons, "|")
	}

	// 如果没有任何条件满足，返回空字符串
	return ""
}

// ShutdownGraceful 优雅关闭连接池
func (p *EnhancedConnectionPool) ShutdownGraceful(timeout time.Duration) error {
	// 1. 标记为正在关闭，停止接受新请求
	atomic.StoreInt32(&p.isShuttingDown, 1)

	// 2. 等待正在使用的连接完成（如果配置了超时）
	if timeout > 0 {
		deadline := time.Now().Add(timeout)
		for p.getActiveConnectionCount() > 0 && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}

		// 检查是否还有活动连接
		activeCount := p.getActiveConnectionCount()
		if activeCount > 0 {
			ylog.Warnf("connection_pool", "优雅关闭超时，仍有 %d 个活动连接", activeCount)
		}
	}

	// 3. 执行正常关闭（Close方法会发送关闭事件）
	return p.Close()
}

// getActiveConnectionCount 获取活动连接数量
func (p *EnhancedConnectionPool) getActiveConnectionCount() int {
	total := 0
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, pool := range p.pools {
		pool.mu.RLock()
		for _, conn := range pool.connections {
			inUse, _, _ := conn.getState()
			if inUse {
				total++
			}
		}
		pool.mu.RUnlock()
	}

	return total
}

// Close 关闭连接池
func (p *EnhancedConnectionPool) Close() error {
	// 1. 标记为正在关闭，停止接受新请求
	atomic.StoreInt32(&p.isShuttingDown, 1)

	// 2. 取消上下文，通知所有后台任务停止
	p.cancel()

	// 3. 发送关闭事件（此时事件通道仍然打开）
	p.sendEvent(EventPoolShutdown, "", nil)

	// 4. 关闭事件通道，让事件处理任务可以退出
	close(p.eventChan)

	// 5. 等待后台任务退出
	p.wg.Wait()

	// 6. 关闭所有连接
	var errors []error
	for proto, pool := range p.pools {
		pool.mu.Lock()

		// 收集需要关闭的连接
		toClose := make([]*EnhancedPooledConnection, 0, len(pool.connections))
		for id, conn := range pool.connections {
			// 检查连接是否正在使用
			inUse, _, _ := conn.getState()
			if inUse {
				ylog.Warnf("connection_pool", "关闭连接池时发现正在使用的连接: id=%s, protocol=%s", id, proto)
				// 尝试释放连接
				conn.release()
			}
			toClose = append(toClose, conn)
		}

		// 清空连接池
		pool.connections = make(map[string]*EnhancedPooledConnection)
		pool.mu.Unlock()

		// 并发关闭连接，但限制并发数
		const maxConcurrentClose = 5
		sem := make(chan struct{}, maxConcurrentClose)
		var closeWg sync.WaitGroup
		var closeErrorsMu sync.Mutex

		for _, conn := range toClose {
			sem <- struct{}{}
			closeWg.Add(1)

			go func(c *EnhancedPooledConnection) {
				defer func() {
					<-sem
					closeWg.Done()

					// 恢复panic，避免整个程序崩溃
					if r := recover(); r != nil {
						ylog.Errorf("connection_pool", "关闭连接时发生panic: %v", r)
						closeErrorsMu.Lock()
						errors = append(errors, fmt.Errorf("protocol %s, connection %s: panic during close: %v", proto, c.id, r))
						closeErrorsMu.Unlock()
					}
				}()

				// 设置连接为无效状态
				c.setHealth(HealthStatusUnhealthy, false)

				// 关闭底层驱动
				if err := c.driver.Close(); err != nil {
					closeErrorsMu.Lock()
					errors = append(errors, fmt.Errorf("protocol %s, connection %s: %w", proto, c.id, err))
					closeErrorsMu.Unlock()
				}

				// 更新统计
				atomic.AddInt64(&pool.stats.DestroyedConnections, 1)
				p.collector.IncrementConnectionsDestroyed(proto)
			}(conn)
		}

		// 等待所有连接关闭完成
		closeWg.Wait()
	}

	if len(errors) > 0 {
		return fmt.Errorf("close errors: %v", errors)
	}

	return nil
}

// MonitoredDriver 监控包装的驱动
type MonitoredDriver struct {
	ProtocolDriver
	conn      *EnhancedPooledConnection
	pool      *EnhancedConnectionPool
	protocol  Protocol
	startTime time.Time
	lastError error

	// 并发保护
	mu       sync.Mutex
	inUse    bool
	useCount int // 使用计数，用于调试和监控
}

func (md *MonitoredDriver) Execute(ctx context.Context, req *ProtocolRequest) (*ProtocolResponse, error) {
	// 并发保护：检查driver是否正在被使用
	md.mu.Lock()
	if md.inUse {
		// 记录并发使用警告（用于调试和监控）
		currentUse := md.useCount
		md.mu.Unlock()
		ylog.Warnf("MonitoredDriver", "driver %s already in use (concurrent access detected, use count=%d)",
			md.conn.id, currentUse)

		// 重新获取锁继续执行（而不是返回错误）
		// 这样可以保持向后兼容，同时记录问题
		md.mu.Lock()
	}

	md.inUse = true
	md.useCount++
	currentUse := md.useCount
	md.mu.Unlock()

	// 确保在函数退出时释放使用标志
	defer func() {
		md.mu.Lock()
		md.inUse = false
		md.mu.Unlock()
	}()

	// 记录调试信息
	ylog.Debugf("MonitoredDriver", "driver %s executing (use count=%d)", md.conn.id, currentUse)

	start := time.Now()
	resp, err := md.ProtocolDriver.Execute(ctx, req)
	duration := time.Since(start)
	md.lastError = err

	// 记录指标
	md.pool.collector.RecordOperationDuration(md.protocol, "execute", duration)
	md.pool.collector.IncrementOperationCount(md.protocol, "execute")

	if err != nil {
		md.pool.collector.IncrementOperationErrors(md.protocol, "execute")
	}

	// 更新连接统计
	atomic.AddInt64(&md.conn.totalRequests, 1)
	if err != nil {
		atomic.AddInt64(&md.conn.totalErrors, 1)
	}

	return resp, err
}

func (md *MonitoredDriver) Close() error {
	// 检查driver是否还在使用中
	md.mu.Lock()
	if md.inUse {
		ylog.Warnf("MonitoredDriver", "closing driver %s while still in use (use count=%d)",
			md.conn.id, md.useCount)
	}
	md.mu.Unlock()

	// 确保连接被正确释放
	if md.pool != nil && md.conn != nil {
		defer func() {
			if err := md.pool.Release(md); err != nil {
				ylog.Debugf("pool", "failed to release connection: %v", err)
			}
		}()
	}

	// 不实际关闭底层驱动，由连接池管理
	return nil
}

// activateConnection 统一激活连接并返回监控包装器
// 注意：连接已经在getConnectionFromPool中通过tryAcquire()获取
func (p *EnhancedConnectionPool) activateConnection(conn *EnhancedPooledConnection) ProtocolDriver {
	// 连接已经在tryAcquire()中标记为inUse=true并更新了lastUsed和usageCount
	// 这里只需要更新池统计和创建监控包装器

	pool := p.getDriverPool(conn.protocol)
	if pool != nil {
		atomic.AddInt64(&pool.stats.ActiveConnections, 1)
		atomic.AddInt64(&pool.stats.IdleConnections, -1)
		atomic.AddInt64(&pool.stats.ReuseCount, 1)
	}

	// 记录调试信息
	if p.debugMode {
		p.recordConnectionTrace(conn, "acquired")
	}

	// 发送事件
	p.sendEvent(EventConnectionReused, conn.protocol, conn)

	return &MonitoredDriver{
		ProtocolDriver: conn.driver,
		conn:           conn,
		pool:           p,
		protocol:       conn.protocol,
		startTime:      time.Now(),
	}
}

// mergeContext 合并请求上下文和池上下文
func (p *EnhancedConnectionPool) mergeContext(requestCtx context.Context) context.Context {
	if requestCtx == nil {
		return p.ctx
	}

	// 如果请求上下文有超时，优先使用请求上下文的超时
	if deadline, ok := requestCtx.Deadline(); ok {
		// 请求上下文有超时，创建新的合并上下文
		mergedCtx, cancel := context.WithDeadline(p.ctx, deadline)

		// 监听请求上下文的取消，确保资源及时释放
		go func() {
			defer cancel() // 确保cancel被调用以释放资源
			select {
			case <-requestCtx.Done():
				// 请求上下文取消，传播取消
			case <-mergedCtx.Done():
				// 合并上下文自然结束
			case <-p.ctx.Done():
				// 池上下文取消
			}
		}()

		return mergedCtx
	}

	// 请求上下文没有超时，直接使用池上下文
	return p.ctx
}

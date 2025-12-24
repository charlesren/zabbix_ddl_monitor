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

// ConnectionState 和状态管理相关代码已移动到 state_manager.go
// 使用 connection.CanTransition() 等函数

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

	// 健康管理
	healthManager *HealthManager

	// 重建管理
	rebuildManager *RebuildManager

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

	// 创建HealthManager（需要在pool创建后，因为需要eventChan）
	pool.healthManager = NewHealthManager(config.HealthCheckTime, config.HealthCheckTimeout, GetGlobalMetricsCollector(), pool.eventChan)

	// 创建RebuildManager
	pool.rebuildManager = NewRebuildManager(config, GetGlobalMetricsCollector(), pool.eventChan)

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

// tryGetIdleConnection 尝试获取空闲连接（优化版：职责单一，只负责获取连接）
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
			// 更新池统计（需要池写锁，但操作简单快速）
			pool.mu.Lock()
			pool.stats.ActiveConnections++
			pool.stats.IdleConnections--
			pool.mu.Unlock()

			state, health := conn.getStatus()
			ylog.Infof("EnhancedConnectionPool", "tryGetIdleConnection: 获取连接成功: id=%s, usage=%d, state=%s, health=%s",
				conn.id, conn.getUsageCount(), state, health)
			p.collector.IncrementConnectionsReused(pool.protocol)
			return conn, nil
		}
	}

	ylog.Infof("EnhancedConnectionPool", "tryGetIdleConnection: 未找到可用空闲连接")
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
		newConn.safeClose()
		return nil, nil
	}

	// 检查是否有其他goroutine已经添加了相同的连接
	// 注意：这里不尝试获取现有连接，避免死锁
	// 如果连接已存在，关闭新创建的连接，让调用者重试
	for _, existingConn := range pool.connections {
		if existingConn.id == newConn.id {
			ylog.Infof("EnhancedConnectionPool", "tryCreateNewConnection: 连接已存在，关闭重复连接: id=%s", newConn.id)
			newConn.safeClose()
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
		usingCount:       0,           // 新连接使用计数为0（新增）
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

// asyncRebuildConnection 异步重建连接（优化版：拆分为多个小函数，避免锁内阻塞操作）
func (p *EnhancedConnectionPool) asyncRebuildConnection(pool *EnhancedDriverPool, oldID string, oldConn *EnhancedPooledConnection) {
	defer func() {
		if r := recover(); r != nil {
			ylog.Errorf("connection_pool", "async rebuild panic: %v", r)
		}
		oldConn.clearRebuildMark()
	}()

	// 阶段1：快速检查（不持有任何锁）
	if !p.canStartRebuild(oldConn, oldID) {
		return
	}

	// 阶段2：获取锁并验证
	lockAcquired, shouldContinue := p.acquireRebuildLocks(pool, oldID, oldConn)
	if !lockAcquired || !shouldContinue {
		return
	}
	// 此时持有 pool.mu 和 conn.mu 锁

	// 阶段3：释放锁，创建新连接（避免在锁内执行阻塞操作）
	pool.mu.Unlock()
	oldConn.mu.Unlock()

	newConn, err := p.createReplacementConnection(pool, oldConn)
	if err != nil {
		ylog.Warnf("connection_pool", "创建新连接失败: %v", err)
		// 重建失败，标记连接为关闭中
		p.markConnectionForClosing(oldConn)
		return
	}

	// 阶段4：重新获取锁，执行替换
	replaced := p.replaceConnectionWithLock(pool, oldID, oldConn, newConn)
	if !replaced {
		// 替换失败，关闭新连接
		newConn.safeClose()
		return
	}

	// 阶段5：完成重建操作
	p.completeRebuild(pool, oldID, oldConn, newConn)

	// 阶段6：异步清理旧连接
	go p.cleanupOldConnection(pool, oldConn)
}

// canStartRebuild 检查是否可以开始重建（快速检查，不持有锁）
func (p *EnhancedConnectionPool) canStartRebuild(oldConn *EnhancedPooledConnection, oldID string) bool {
	// 检查连接是否正在使用（状态检查）
	if oldConn.isInUse() {
		ylog.Infof("connection_pool", "连接 %s 正在使用，跳过重建", oldID)
		return false
	}

	// 检查使用计数
	if oldConn.getUseCount() > 0 {
		ylog.Infof("connection_pool", "连接 %s 使用计数=%d，跳过重建", oldID, oldConn.getUseCount())
		return false
	}

	return true
}

// acquireRebuildLocks 获取重建所需的锁并验证
func (p *EnhancedConnectionPool) acquireRebuildLocks(pool *EnhancedDriverPool, oldID string, oldConn *EnhancedPooledConnection) (lockAcquired, shouldContinue bool) {
	// 获取池锁和连接锁（按正确顺序：先池锁，后连接锁）
	pool.mu.Lock()

	// 检查连接是否还在池中
	if _, exists := pool.connections[oldID]; !exists {
		ylog.Infof("connection_pool", "连接 %s 已不在池中，取消重建", oldID)
		pool.mu.Unlock()
		return false, false
	}

	// 尝试获取连接锁并开始重建
	if !oldConn.beginRebuildWithLock() {
		ylog.Infof("connection_pool", "无法开始重建连接 %s", oldID)
		pool.mu.Unlock()
		return false, false
	}

	return true, true
}

// createReplacementConnection 创建替换连接（不持有锁）
func (p *EnhancedConnectionPool) createReplacementConnection(pool *EnhancedDriverPool, oldConn *EnhancedPooledConnection) (*EnhancedPooledConnection, error) {
	ylog.Infof("connection_pool", "开始创建新连接以替换 %s", oldConn.id)
	newConn, err := p.createConnection(p.ctx, pool)
	if err != nil {
		return nil, err
	}
	newConn.setLastRebuiltAt(time.Now())
	return newConn, nil
}

// replaceConnectionWithLock 执行连接替换（需要持有锁）
func (p *EnhancedConnectionPool) replaceConnectionWithLock(pool *EnhancedDriverPool, oldID string, oldConn *EnhancedPooledConnection, newConn *EnhancedPooledConnection) bool {
	// 重新获取锁
	pool.mu.Lock()
	oldConn.mu.Lock()

	// 再次检查连接是否还在池中（可能在此期间被删除）
	if _, exists := pool.connections[oldID]; !exists {
		ylog.Infof("connection_pool", "连接 %s 在创建新连接期间被删除，取消替换", oldID)
		oldConn.mu.Unlock()
		pool.mu.Unlock()
		return false
	}

	// 执行替换（快速操作）
	delete(pool.connections, oldID)
	pool.connections[newConn.id] = newConn

	// 更新统计
	atomic.AddInt64(&pool.stats.CreatedConnections, 1)
	atomic.AddInt64(&pool.stats.RebuiltConnections, 1)
	p.collector.IncrementConnectionsCreated(pool.protocol)

	ylog.Infof("connection_pool", "异步替换连接: %s→%s", oldID, newConn.id)

	// 完成旧连接的重建状态转换
	oldConn.transitionStateLocked(StateIdle) // 重建成功，标记为空闲（即将被关闭）

	// 释放锁
	oldConn.mu.Unlock()
	pool.mu.Unlock()

	return true
}

// completeRebuild 完成重建操作（不持有锁）
func (p *EnhancedConnectionPool) completeRebuild(pool *EnhancedDriverPool, oldID string, oldConn *EnhancedPooledConnection, newConn *EnhancedPooledConnection) {
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

	ylog.Infof("connection_pool", "重建完成: %s→%s", oldID, newConn.id)
}

// cleanupOldConnection 清理旧连接（异步）
func (p *EnhancedConnectionPool) cleanupOldConnection(pool *EnhancedDriverPool, oldConn *EnhancedPooledConnection) {
	defer func() {
		if r := recover(); r != nil {
			ylog.Errorf("connection_pool", "关闭旧连接时发生panic: %v", r)
		}
	}()

	oldConn.safeClose()
	atomic.AddInt64(&pool.stats.DestroyedConnections, 1)
	oldConn.completeClose()
}

// markConnectionForClosing 标记连接为关闭中（重建失败时使用）
func (p *EnhancedConnectionPool) markConnectionForClosing(oldConn *EnhancedPooledConnection) {
	oldConn.mu.Lock()
	defer oldConn.mu.Unlock()
	oldConn.transitionStateLocked(StateClosing)
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
	if p.healthManager == nil {
		return
	}

	// 获取需要健康检查的连接
	connectionsByProtocol := p.GetConnectionsForHealthCheck()
	if len(connectionsByProtocol) == 0 {
		return
	}

	// 限制并发检查数量
	sem := make(chan struct{}, 10) // 默认最多10个并发检查

	// 为每个连接执行健康检查，使用checkConnectionHealthWithContext
	for proto, connections := range connectionsByProtocol {
		for _, conn := range connections {
			sem <- struct{}{}
			go func(proto Protocol, conn *EnhancedPooledConnection) {
				defer func() { <-sem }()
				p.checkConnectionHealthWithContext(proto, conn)
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

	// 检查连接是否太新，避免对刚创建的连接进行健康检查（新增）
	// 给Scrapli内部goroutine足够时间启动，避免健康检查失败导致连接被立即清理
	minAgeForHealthCheck := 15 * time.Second
	now := time.Now()
	createdAt := conn.getCreatedAt()
	if now.Sub(createdAt) < minAgeForHealthCheck {
		ylog.Infof("pool", "跳过健康检查：连接 %s 太新（%v < %v）",
			conn.id, now.Sub(createdAt), minAgeForHealthCheck)
		return
	}

	// 开始健康检查（设置状态为检查中）
	if !conn.beginHealthCheck() {
		state, _ := conn.getStatus()
		ylog.Infof("pool", "无法开始健康检查: connection %s state=%s", conn.id, state)
		return
	}

	// 创建MonitoredDriver来包装driver，以便正确管理使用计数
	monitoredDriver := &MonitoredDriver{
		ProtocolDriver: conn.driver,
		conn:           conn,
		pool:           p,
		protocol:       conn.protocol,
		startTime:      time.Now(),
	}

	start := time.Now()
	err := p.defaultHealthCheck(monitoredDriver)
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

// checkConnectionHealthWithContext 使用GetWithContext检查连接健康状态（与pingtask相同的方式）
func (p *EnhancedConnectionPool) checkConnectionHealthWithContext(proto Protocol, conn *EnhancedPooledConnection) {
	// 检查连接是否正在使用，跳过正在使用的连接
	if conn.isInUse() {
		return
	}

	// 检查连接是否太新，避免对刚创建的连接进行健康检查（新增）
	// 给Scrapli内部goroutine足够时间启动，避免健康检查失败导致连接被立即清理
	minAgeForHealthCheck := 15 * time.Second
	now := time.Now()
	createdAt := conn.getCreatedAt()
	if now.Sub(createdAt) < minAgeForHealthCheck {
		ylog.Infof("pool", "跳过健康检查：连接 %s 太新（%v < %v）",
			conn.id, now.Sub(createdAt), minAgeForHealthCheck)
		return
	}

	// 开始健康检查（设置状态为检查中）
	if !conn.beginHealthCheck() {
		state, _ := conn.getStatus()
		ylog.Infof("pool", "无法开始健康检查: connection %s state=%s", conn.id, state)
		return
	}

	// 使用GetWithContext获取连接（与pingtask相同的方式）
	start := time.Now()

	// 创建上下文，使用健康检查超时时间
	timeout := p.config.HealthCheckTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	driver, err := p.GetWithContext(ctx, proto)
	acquireDuration := time.Since(start)

	if err != nil {
		ylog.Warnf("pool", "健康检查获取连接失败: connection_id=%s, protocol=%s, error=%v, duration=%v",
			conn.id, proto, err, acquireDuration)

		// 记录健康检查失败
		conn.recordHealthCheck(false, err)

		errorType := classifyHealthCheckError(err)
		p.collector.IncrementHealthCheckFailed(proto)
		state, _ := conn.getStatus()
		p.sendEvent(EventHealthCheckFailed, proto, map[string]interface{}{
			"connection_id":    conn.id,
			"error":            err.Error(),
			"error_type":       errorType.String(),
			"state":            state.String(),
			"acquire_duration": acquireDuration.String(),
		})

		p.collector.RecordHealthCheckDuration(proto, acquireDuration)
		return
	}

	// 确保连接被释放
	defer func() {
		if releaseErr := p.Release(driver); releaseErr != nil {
			ylog.Warnf("pool", "健康检查释放连接失败: connection_id=%s, protocol=%s, error=%v",
				conn.id, proto, releaseErr)
		}
	}()

	ylog.Debugf("pool", "健康检查获取连接成功: connection_id=%s, protocol=%s, acquire_duration=%v",
		conn.id, proto, acquireDuration)

	// 执行健康检查
	healthCheckStart := time.Now()
	healthCheckErr := p.defaultHealthCheck(driver)
	healthCheckDuration := time.Since(healthCheckStart)

	totalDuration := time.Since(start)
	success := healthCheckErr == nil

	// 记录健康检查结果到连接
	conn.recordHealthCheck(success, healthCheckErr)

	// 记录指标和事件
	if !success {
		errorType := classifyHealthCheckError(healthCheckErr)

		// 根据错误类型记录不同级别的日志
		switch errorType {
		case HealthErrorTimeout:
			ylog.Warnf("pool", "connection %s health check timeout: %v", conn.id, healthCheckErr)
		case HealthErrorNetwork:
			ylog.Warnf("pool", "connection %s network error: %v", conn.id, healthCheckErr)
		case HealthErrorAuth:
			ylog.Errorf("pool", "connection %s authentication error: %v", conn.id, healthCheckErr)
		case HealthErrorCommand:
			ylog.Warnf("pool", "connection %s command error: %v", conn.id, healthCheckErr)
		case HealthErrorProtocol:
			ylog.Errorf("pool", "connection %s protocol error: %v", conn.id, healthCheckErr)
		default:
			ylog.Warnf("pool", "connection %s health check failed: %v", conn.id, healthCheckErr)
		}

		p.collector.IncrementHealthCheckFailed(proto)
		state, _ := conn.getStatus()
		p.sendEvent(EventHealthCheckFailed, proto, map[string]interface{}{
			"connection_id":    conn.id,
			"error":            healthCheckErr.Error(),
			"error_type":       errorType.String(),
			"state":            state.String(),
			"total_duration":   totalDuration.String(),
			"check_duration":   healthCheckDuration.String(),
			"acquire_duration": acquireDuration.String(),
		})
	} else {
		p.collector.IncrementHealthCheckSuccess(proto)
		ylog.Debugf("pool", "健康检查成功: connection_id=%s, protocol=%s, total_duration=%v, check_duration=%v",
			conn.id, proto, totalDuration, healthCheckDuration)
	}

	p.collector.RecordHealthCheckDuration(proto, totalDuration)
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

							if err := c.safeClose(); err != nil {
								ylog.Warnf("connection_pool", "安全关闭连接失败: id=%s, error=%v", c.id, err)
							}
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
	// 第一层检查：使用计数 > 0 的连接不应该清理
	if conn.getUseCount() > 0 {
		ylog.Infof("connection_pool", "跳过清理：连接 %s 使用计数=%d", conn.id, conn.getUseCount())
		return false
	}

	// 获取连接状态
	state, health := conn.getStatus()

	// 第二层检查：以下状态的连接不应该被清理
	nonCleanableStates := []ConnectionState{
		StateConnecting, // 连接中
		StateAcquired,   // 已获取
		StateExecuting,  // 执行中
		StateChecking,   // 检查中
		StateRebuilding, // 重建中
		StateClosing,    // 关闭中
	}

	for _, s := range nonCleanableStates {
		if state == s {
			ylog.Debugf("connection_pool", "跳过清理：连接 %s 状态=%s", conn.id, state)
			return false
		}
	}

	// 只有 StateIdle 状态的连接可以被清理
	// 检查连接有效性（StateClosed 状态也应该被清理）
	valid := state != StateClosed && state != StateClosing
	if !valid {
		ylog.Debugf("connection_pool", "连接无效需要清理：%s 状态=%s", conn.id, state)
		return true
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
	if p.rebuildManager != nil {
		return p.rebuildManager.ShouldRebuild(conn)
	}

	// 兼容旧测试：当rebuildManager为nil时，使用旧的逻辑
	// 注意：这只是为了向后兼容测试，生产代码应该总是有rebuildManager
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

	// 根据策略判断
	switch p.config.RebuildStrategy {
	case "usage":
		// 仅基于使用次数
		usageCount := conn.getUsageCount()
		shouldRebuild := usageCount >= p.config.RebuildMaxUsageCount
		if shouldRebuild {
			// 使用原子操作确保只有一个goroutine能将连接标记为重建
			if conn.markForRebuild() {
				return true
			} else {
				return false
			}
		}
		return shouldRebuild

	case "age":
		// 仅基于连接年龄
		age := now.Sub(conn.getCreatedAt())
		shouldRebuild := age >= p.config.RebuildMaxAge
		if shouldRebuild {
			// 使用原子操作确保只有一个goroutine能将连接标记为重建
			if conn.markForRebuild() {
				return true
			} else {
				return false
			}
		}
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
						return true
					} else {
						return false
					}
				}
				return shouldRebuild
			}
		}
		return false

	case "all":
		// 满足所有条件
		conditions := 0
		usageCount := atomic.LoadInt64(&conn.usageCount)
		if usageCount >= p.config.RebuildMaxUsageCount {
			conditions++
		}

		age := now.Sub(conn.getCreatedAt())
		if age >= p.config.RebuildMaxAge {
			conditions++
		}

		totalRequests := atomic.LoadInt64(&conn.totalRequests)
		if totalRequests >= p.config.RebuildMinRequestsForErrorRate {
			totalErrors := atomic.LoadInt64(&conn.totalErrors)
			// 使用浮点数计算确保精度
			errorRate := float64(totalErrors) / float64(totalRequests)
			if errorRate >= p.config.RebuildMaxErrorRate {
				conditions++
			}
		}

		shouldRebuild := conditions == 3
		if shouldRebuild {
			// 使用原子操作确保只有一个goroutine能将连接标记为重建
			if atomic.CompareAndSwapInt32(&conn.markedForRebuild, 0, 1) {
				return true
			} else {
				return false
			}
		}
		return shouldRebuild

	case "any": // 默认策略
		fallthrough
	default:
		// 满足任意条件
		usageCount := atomic.LoadInt64(&conn.usageCount)
		if usageCount >= p.config.RebuildMaxUsageCount {
			// 使用原子操作确保只有一个goroutine能将连接标记为重建
			if atomic.CompareAndSwapInt32(&conn.markedForRebuild, 0, 1) {
				return true
			} else {
				return false
			}
		}

		age := now.Sub(conn.getCreatedAt())
		if age >= p.config.RebuildMaxAge {
			// 使用原子操作确保只有一个goroutine能将连接标记为重建
			if atomic.CompareAndSwapInt32(&conn.markedForRebuild, 0, 1) {
				return true
			} else {
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
						return true
					} else {
						return false
					}
				}
			}
		}

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

// GetConnectionsForHealthCheck 获取需要健康检查的连接
func (p *EnhancedConnectionPool) GetConnectionsForHealthCheck() map[Protocol][]*EnhancedPooledConnection {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make(map[Protocol][]*EnhancedPooledConnection)

	for proto, pool := range p.pools {
		// 获取需要健康检查的连接
		connections := make([]*EnhancedPooledConnection, 0, len(pool.connections))
		for _, conn := range pool.connections {
			if !conn.isInUse() {
				connections = append(connections, conn)
			}
		}
		if len(connections) > 0 {
			result[proto] = connections
		}
	}

	return result
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
	if p.rebuildManager == nil {
		return ""
	}
	return p.rebuildManager.GetRebuildReason(conn)
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

				// 安全关闭底层驱动
				if err := c.safeClose(); err != nil {
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

func (md *MonitoredDriver) Connect() error {
	// 增加连接使用计数
	md.conn.acquireUse()
	defer md.conn.releaseUse()

	// 状态转换：到StateConnecting
	oldState, _ := md.conn.getStatus()
	if !md.conn.transitionState(StateConnecting) {
		ylog.Warnf("MonitoredDriver", "无法转换到连接状态: 当前状态=%s", oldState)
		return fmt.Errorf("cannot transition to connecting state: current=%s", oldState)
	}

	ylog.Debugf("MonitoredDriver", "状态转换: %s -> %s", oldState, StateConnecting)

	// 调用底层驱动的Connect（需要类型断言，因为ProtocolDriver接口没有Connect方法）
	// 注意：ScrapliDriver和SSHDriver都有Connect方法，但接口中没有定义
	var err error
	if scrapliDriver, ok := md.ProtocolDriver.(interface{ Connect() error }); ok {
		err = scrapliDriver.Connect()
	} else if sshDriver, ok := md.ProtocolDriver.(interface{ Connect() error }); ok {
		err = sshDriver.Connect()
	} else {
		// 如果驱动没有Connect方法，记录警告但继续
		ylog.Warnf("MonitoredDriver", "底层驱动没有Connect方法: %T", md.ProtocolDriver)
		// 不返回错误，因为有些驱动可能不需要显式连接
	}

	// 根据连接结果更新状态
	if err == nil {
		// 连接成功，转换到StateAcquired
		if !md.conn.transitionState(StateAcquired) {
			ylog.Warnf("MonitoredDriver", "连接成功但无法转换到Acquired状态")
		} else {
			ylog.Debugf("MonitoredDriver", "连接成功，状态转换: %s -> %s", StateConnecting, StateAcquired)
		}
	} else {
		// 连接失败，回到StateIdle
		if md.conn.transitionState(StateIdle) {
			ylog.Debugf("MonitoredDriver", "连接失败，状态恢复: %s -> %s", StateConnecting, StateIdle)
		}
	}

	return err
}

func (md *MonitoredDriver) Execute(ctx context.Context, req *ProtocolRequest) (*ProtocolResponse, error) {
	// 增加连接使用计数
	md.conn.acquireUse()

	// 确保在函数退出时减少使用计数
	defer md.conn.releaseUse()

	// 关键：在调用底层驱动之前，确保连接已建立
	// 检查当前状态，如果不在执行相关状态，需要先建立连接
	currentState, _ := md.conn.getStatus()

	switch currentState {
	case StateIdle:
		// 连接空闲，需要先获取并建立连接
		ylog.Debugf("MonitoredDriver", "连接空闲，需要建立连接: state=%s", currentState)
		// 注意：这里不转换状态，因为tryAcquire()已经转换到StateAcquired
		// 实际连接建立会在ScrapliDriver.Execute()中触发

	case StateAcquired:
		// 连接已获取，准备执行
		ylog.Debugf("MonitoredDriver", "连接已获取，准备执行: state=%s", currentState)

	case StateConnecting:
		// 连接正在建立中，等待完成
		ylog.Debugf("MonitoredDriver", "连接正在建立中: state=%s", currentState)

	default:
		ylog.Warnf("MonitoredDriver", "连接在非常规状态下执行: state=%s", currentState)
	}

	// 状态转换：从Acquired到Executing（只有已获取的连接才能执行）
	oldState, _ := md.conn.getStatus()
	if oldState == StateAcquired {
		if md.conn.transitionState(StateExecuting) {
			// 确保在函数退出时恢复状态
			defer func() {
				md.conn.transitionState(StateAcquired)
			}()
			ylog.Debugf("MonitoredDriver", "状态转换: %s -> %s", oldState, StateExecuting)
		} else {
			ylog.Warnf("MonitoredDriver", "无法转换到执行状态: 当前状态=%s", oldState)
		}
	} else {
		ylog.Debugf("MonitoredDriver", "保持当前状态执行: %s", oldState)
	}

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
	currentUse := md.useCount // 重新声明，因为之前的声明在if块内
	md.mu.Unlock()

	// 确保在函数退出时释放使用标志
	defer func() {
		md.mu.Lock()
		md.inUse = false
		md.mu.Unlock()
	}()

	// 记录调试信息
	currentState, _ = md.conn.getStatus() // 使用赋值，不是声明
	ylog.Infof("MonitoredDriver", "driver %s executing (state=%s, use count=%d, connection use count=%d)",
		md.conn.id, currentState, currentUse, md.conn.getUseCount())

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

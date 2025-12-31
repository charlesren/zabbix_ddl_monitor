package connection

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charlesren/ylog"
)

// ============================================================================
// 锁顺序规则（全局遵循，避免死锁）
// ============================================================================
//
// 1. 当需要同时持有池锁和连接锁时，始终按以下顺序获取：
//    pool.mu.Lock()    // 先获取池锁
//    conn.mu.Lock()    // 后获取连接锁
//
// 2. 释放时按相反顺序：
//    conn.mu.Unlock()  // 先释放连接锁
//    pool.mu.Unlock()  // 后释放池锁
//
// 3. 尽量避免嵌套锁：
//    - 使用"检查-锁定"模式：先无锁检查，再获取锁快速操作
//    - 在持有锁时只做快速操作（<1ms）
//    - 耗时操作（创建连接、网络IO等）在释放锁后执行
//
// 4. 使用defer确保锁释放：
//    mu.Lock()
//    defer mu.Unlock()  // 即使panic也会释放
//
// 5. 原子操作优先：
//    - 使用atomic包代替锁（如计数器、标记位）
//    - 减少锁竞争，提高性能
//
// 6. 关键设计决策：
//    - performCoreRebuild 在创建新连接期间（5-10秒）释放所有锁
//    - 这期间连接池状态短暂不一致（旧连接已删除，新连接未加入）
//    - 但实际影响很小：重建是低频操作（5分钟一次），池中有多个连接
//
// ============================================================================

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
	EventRebuildFailed

	// 重建相关事件
	EventRebuildMarked          // 连接被标记为需要重建
	EventRebuildStarted         // 重建开始
	EventRebuildCompleted       // 重建完成
	EventConnectionsNeedRebuild // 连接需要重建统计
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
			ylog.Debugf("EnhancedConnectionPool", "连接获取成功（内部）: protocol=%s, connection_id=%s", proto, conn.id)
		}
		return err
	})

	if err != nil {
		p.collector.IncrementConnectionsFailed(proto)
		ylog.Errorf("EnhancedConnectionPool", "连接获取最终失败: protocol=%s, error=%v", proto, err)
		return nil, err
	}

	ylog.Debugf("EnhancedConnectionPool", "连接获取成功: protocol=%s, connection_id=%s", proto, conn.id)
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

		// 尝试获取可用连接
		conn, err := p.tryGetAvailableConnection(ctx, pool)
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

// tryGetAvailableConnection 尝试获取可用连接（优化版：职责单一，只负责获取连接）
//
// "可用连接"定义为满足以下所有条件的连接：
// 1. 状态为 StateIdle（空闲）
// 2. 健康状态不为 HealthStatusUnhealthy（非不健康）
// 3. 不在重建中（rebuilding == false）
// 4. 使用计数为0（没有被其他任务使用）
//
// 这个方法会遍历连接池中的所有连接，尝试找到第一个可用的连接并获取它
func (p *EnhancedConnectionPool) tryGetAvailableConnection(ctx context.Context, pool *EnhancedDriverPool) (*EnhancedPooledConnection, error) {
	// 第一阶段：快速收集候选连接ID（只读，最小化锁持有时间）
	pool.mu.RLock()
	connectionIDs := make([]string, 0, len(pool.connections))
	for id := range pool.connections {
		connectionIDs = append(connectionIDs, id)
	}
	pool.mu.RUnlock()

	ylog.Infof("EnhancedConnectionPool", "tryGetAvailableConnection: 连接池中有 %d 个连接", len(connectionIDs))

	if len(connectionIDs) == 0 {
		ylog.Infof("EnhancedConnectionPool", "tryGetAvailableConnection: 连接池为空")
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

		// 尝试获取连接用于任务执行（不持有池锁）
		// 这个方法会检查：空闲、非不健康、非重建中、使用计数为0
		if conn.tryAcquireForTask() {
			// 更新池统计（需要池写锁，但操作简单快速）
			pool.mu.Lock()
			pool.stats.ActiveConnections++
			pool.stats.IdleConnections--
			pool.mu.Unlock()

			state, health := conn.getStatus()
			ylog.Infof("EnhancedConnectionPool", "tryGetAvailableConnection: 获取连接成功: id=%s, usage=%d, state=%s, health=%s",
				conn.id, conn.getUsageCount(), state, health)
			p.collector.IncrementConnectionsReused(pool.protocol)
			return conn, nil
		}
	}

	ylog.Infof("EnhancedConnectionPool", "tryGetAvailableConnection: 未找到可用连接")
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

	// 尝试获取新连接用于任务执行
	if newConn.tryAcquireForTask() {
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
		config:           &p.config, // 传入配置引用
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

// performCoreRebuild 核心重建函数，包含优化的锁策略和错误处理
//
// 锁策略说明：
// 1. 遵循"先池锁后连接锁"的顺序（当需要同时获取时）
// 2. 大多数操作只持有一个锁
// 3. 创建新连接期间（5-10秒）释放所有锁，避免阻塞其他操作
// 4. 使用defer确保锁释放
//
// 执行流程：
//
//	阶段1（无锁）: 快速检查
//	阶段2（连接锁）: 开始重建，设置标记
//	阶段3（连接锁+池锁）: 关闭旧连接，从池删除
//	阶段4（无锁，5-10秒）: 创建新连接 ← 关键：无锁区间
//	阶段5（池锁）: 添加新连接到池
//	阶段6（无锁）: 完成重建，发送事件
func (p *EnhancedConnectionPool) performCoreRebuild(ctx context.Context, oldConn *EnhancedPooledConnection) (string, error) {
	// 从连接对象获取信息
	oldID := oldConn.id
	proto := oldConn.protocol

	// 获取驱动池
	driverPool := p.getDriverPool(proto)
	if driverPool == nil {
		return "", fmt.Errorf("驱动池不存在: protocol=%s", proto)
	}

	// ========== 【阶段1：快速检查】（无锁） ==========
	// 目的：在不持有任何锁的情况下快速判断是否可以重建
	// 避免获取锁后发现无法重建而浪费资源
	if !p.canStartRebuild(oldConn, oldID) {
		return "", fmt.Errorf("连接 %s 不适合重建", oldID)
	}

	// ========== 【阶段2：开始重建】（持有连接锁） ==========
	// 目的：设置重建标记（rebuilding=true），防止并发重建
	// 锁顺序：只获取连接锁，不获取池锁
	if !oldConn.beginRebuild() {
		return "", fmt.Errorf("无法开始重建: id=%s", oldID)
	}

	// 声明错误变量（用于defer）
	var err error
	// 确保最终清除重建标记（即使panic也会执行）
	defer func() {
		if r := recover(); r != nil {
			ylog.Errorf("performCoreRebuild", "panic: %v, id=%s, stack:\n%s", r, oldID, debug.Stack())
			err = fmt.Errorf("panic during rebuild")
		}
		oldConn.completeRebuild(err == nil)
	}()

	// ========== 【阶段3：关闭旧连接】 ==========

	// 3.1 开始关闭（持有连接锁）
	// 目的：将连接状态转换为 StateClosing
	// 注意：正常情况下 beginClose() 应该成功，因为前面已经检查过状态
	// 如果失败，说明有竞态条件或状态异常
	if !oldConn.beginClose() {
		// beginClose() 失败，检查当前状态决定如何处理
		state, _ := oldConn.getStatus()

		if state == StateClosed {
			// 已经是关闭状态（终态），无需重复关闭
			ylog.Infof("connection_pool", "连接已关闭，跳过关闭流程: id=%s", oldID)
		} else if state == StateClosing {
			// 正在关闭中，无需重复操作
			ylog.Infof("connection_pool", "连接正在关闭中，跳过关闭流程: id=%s", oldID)
		} else {
			// 其他状态异常（可能是竞态导致的），记录错误并退出
			ylog.Errorf("connection_pool", "无法正常关闭连接（状态异常）: id=%s, state=%s, rebuilding=%v",
				oldID, state, oldConn.isRebuilding())
			return "", fmt.Errorf("连接状态异常，无法关闭: id=%s, state=%s", oldID, state)
		}
	}

	// 3.2 关闭driver（无锁，快速操作）
	// 目的：释放底层网络连接
	if oldConn.driver != nil {
		oldConn.driver.Close()
		oldConn.driver = nil
	}

	// 3.3 从池中删除（持有池锁）← 【关键点1】
	// 锁顺序：只获取池锁（遵循"先池锁后连接锁"规则，但此时没有连接锁）
	// 目的：从连接池中移除旧连接
	// 竞态：此时可能有其他goroutine在Get()，但它们会获取连接的其他副本
	driverPool.mu.Lock()
	delete(driverPool.connections, oldID)
	atomic.AddInt64(&driverPool.stats.IdleConnections, -1)
	driverPool.mu.Unlock() // ⚠️ 释放池锁，准备创建新连接

	// 3.4 完成关闭（持有连接锁）
	oldConn.completeClose()

	// ========== 【阶段4：创建新连接】（无锁，耗时5-10秒） ==========
	// ⚠️ 重要：此时旧连接已从池删除，新连接未加入
	// 可能的竞态：
	//   1. 其他goroutine调用Get()发现可用连接减少
	//   2. 可能触发创建新连接（如果连接数不足）
	//   3. 但影响很小，因为：
	//      - 重建通常是低频操作（5分钟一次）
	//      - 池中通常有多个连接
	//      - 即使短暂减少连接数也不会导致请求失败
	//
	// 不持有锁的原因：创建连接需要5-10秒（网络IO、认证等）
	//                  如果持有池锁会阻塞所有Get()/Release()操作

	newConn, err := p.createReplacementConnection(ctx, driverPool, oldConn)
	if err != nil {
		// 创建新连接失败，旧连接已从池删除且已关闭
		// 连接池暂时减少了一个连接，但 GetWithContext() 会通过 tryCreateNewConnection() 自动补充
		// 无需额外恢复逻辑，连接池具备自动恢复能力
		ylog.Errorf("connection_pool", "重建失败：已删除旧连接但无法创建新连接（连接池会自动恢复）, id=%s, error=%v", oldID, err)
		return "", fmt.Errorf("创建新连接失败: %w", err)
	}

	// ========== 【阶段5：添加新连接】（持有池锁） ==========
	// ⚠️ 重要：重新获取池锁
	// 可能的竞态：与Get()等其他操作竞争池锁
	// 但由于时间很短（<1ms），实际影响很小

	driverPool.mu.Lock()
	// 快速操作（不要在锁内做耗时操作）
	driverPool.connections[newConn.id] = newConn
	atomic.AddInt64(&driverPool.stats.CreatedConnections, 1)
	atomic.AddInt64(&driverPool.stats.IdleConnections, 1)
	driverPool.mu.Unlock()

	// ========== 【阶段6：完成重建】（无锁） ==========
	// 目的：发送事件，更新指标
	// 不持有锁：避免在事件处理中死锁
	p.completeRebuild(driverPool, oldID, oldConn, newConn)

	ylog.Infof("connection_pool", "先关后建成功: 旧连接=%s (已关闭), 新连接=%s", oldID, newConn.id)

	return newConn.id, nil
}

// canStartRebuild 检查是否可以开始重建（快速检查，不持有锁）
func (p *EnhancedConnectionPool) canStartRebuild(oldConn *EnhancedPooledConnection, oldID string) bool {
	// 检查连接是否空闲（只检查状态，不检查健康状态）
	// 不健康的连接更应该重建，所以这里用 isIdle 而不是 isAvailable
	if !oldConn.isIdle() {
		ylog.Infof("connection_pool", "连接 %s 不空闲，跳过重建", oldID)
		return false
	}

	// 检查使用计数
	if oldConn.getUseCount() > 0 {
		ylog.Infof("connection_pool", "连接 %s 使用计数=%d，跳过重建", oldID, oldConn.getUseCount())
		return false
	}

	return true
}

// createReplacementConnection 创建替换连接（使用指定的 context）
// ctx: 用于控制创建连接操作的超时和取消
func (p *EnhancedConnectionPool) createReplacementConnection(ctx context.Context, pool *EnhancedDriverPool, oldConn *EnhancedPooledConnection) (*EnhancedPooledConnection, error) {
	ylog.Infof("connection_pool", "开始创建新连接以替换 %s", oldConn.id)
	newConn, err := p.createConnection(ctx, pool)
	if err != nil {
		return nil, err
	}
	newConn.setLastRebuiltAt(time.Now())
	return newConn, nil
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

	// 重建任务
	if p.config.SmartRebuildEnabled {
		// 检查重建间隔，最小5秒
		rebuildInterval := p.config.RebuildCheckInterval
		if rebuildInterval == 0 {
			rebuildInterval = 5 * time.Minute // 默认5分钟
		}

		if rebuildInterval < 5*time.Second {
			ylog.Warnf("pool", "重建间隔太短: %v (最小5秒)，重建任务未启动", rebuildInterval)
		} else {
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				p.rebuildTask()
			}()
			ylog.Infof("pool", "重建任务已启动，间隔: %v", rebuildInterval)
		}
	}

	// 空闲连接健康检查任务
	// go p.idleConnectionHealthTask() // 已删除：使用统一的 healthCheckTask 入口
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

	// 获取所有协议类型
	protocols := p.getAllProtocols()
	if len(protocols) == 0 {
		return
	}

	// 为每个协议执行健康检查（每个协议每次只检查一个连接）
	for _, proto := range protocols {
		go func(proto Protocol) {
			p.performHealthCheckForProtocol(proto)
		}(proto)
	}
}

// getAllProtocols 获取所有协议类型
func (p *EnhancedConnectionPool) getAllProtocols() []Protocol {
	p.mu.RLock()
	defer p.mu.RUnlock()

	protocols := make([]Protocol, 0, len(p.pools))
	for proto := range p.pools {
		protocols = append(protocols, proto)
	}
	return protocols
}

// performHealthCheckForProtocol 为特定协议执行健康检查
// 通过连接池获取一个空闲连接，然后执行健康检查
//
// 参数:
//   - proto: 协议类型
func (p *EnhancedConnectionPool) performHealthCheckForProtocol(proto Protocol) {
	connID := "unknown"
	totalStart := time.Now()

	// 1. 通过连接池获取空闲连接（使用连接池的选择策略）
	// 使用配置的健康检查超时，而不是硬编码
	timeout := p.config.HealthCheckTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	driver, err := p.GetWithContext(ctx, proto)
	acquireDuration := time.Since(totalStart)

	if err != nil {
		// 获取连接失败，记录并跳过
		ylog.Infof("pool", "健康检查跳过: protocol=%s, 获取连接失败: %v, duration=%v",
			proto, err, acquireDuration)

		// 获取失败也算健康检查失败（记录指标）
		p.collector.IncrementHealthCheckFailed(proto)
		p.collector.RecordHealthCheckDuration(proto, acquireDuration)
		return
	}

	// 2. 获取连接对象
	monitoredDriver, ok := driver.(*MonitoredDriver)
	if !ok {
		ylog.Warnf("pool", "健康检查失败: protocol=%s, 驱动类型错误", proto)
		p.Release(driver)
		return
	}

	conn := monitoredDriver.conn
	connID = conn.id

	// 3. 确保连接被释放
	defer func() {
		if releaseErr := p.Release(driver); releaseErr != nil {
			ylog.Warnf("pool", "健康检查释放连接失败: protocol=%s, connection_id=%s, error=%v",
				proto, connID, releaseErr)
		}
		totalDuration := time.Since(totalStart)
		ylog.Debugf("pool", "健康检查完成: protocol=%s, connection_id=%s, total_duration=%v",
			proto, connID, totalDuration)
	}()

	// 4. 检查连接状态，确保适合做健康检查
	state, _ := conn.getStatus()
	if state != StateAcquired && state != StateIdle {
		ylog.Infof("pool", "跳过健康检查: protocol=%s, connection_id=%s, state=%s",
			proto, connID, state)
		return
	}

	// 5. 执行健康检查（调用纯粹的健康检查方法）
	// 创建用于健康检查的上下文
	checkCtx, checkCancel := context.WithTimeout(context.Background(), timeout)
	defer checkCancel()

	success, checkErr := p.checkConnectionHealthWithContext(checkCtx, conn)

	// 6. 记录结果日志
	if !success {
		ylog.Infof("pool", "健康检查完成: protocol=%s, connection_id=%s, result=fail, error=%v, acquire_duration=%v, total_duration=%v",
			proto, connID, checkErr, acquireDuration, time.Since(totalStart))
	} else {
		ylog.Debugf("pool", "健康检查成功: protocol=%s, connection_id=%s, acquire_duration=%v, total_duration=%v",
			proto, connID, acquireDuration, time.Since(totalStart))
	}
}

// checkConnectionHealthWithContext 检查单个连接的健康状态
// 这是一个纯粹的健康检查方法，不涉及连接的获取和释放
//
// 参数:
//   - ctx: 上下文（支持取消和超时）
//   - conn: 要检查的连接
//
// 返回:
//   - success: 健康检查是否成功
//   - err: 错误信息（如果失败）
func (p *EnhancedConnectionPool) checkConnectionHealthWithContext(
	ctx context.Context,
	conn *EnhancedPooledConnection,
) (success bool, err error) {
	proto := conn.protocol
	connID := conn.id

	// 1. 前置检查：连接年龄（15秒内的跳过）
	minAgeForHealthCheck := 15 * time.Second
	if time.Since(conn.getCreatedAt()) < minAgeForHealthCheck {
		return false, fmt.Errorf("连接太新: age=%v < %v",
			time.Since(conn.getCreatedAt()), minAgeForHealthCheck)
	}

	// 2. 开始健康检查：尝试转换到 StateChecking（由状态机决定是否允许）
	if !conn.beginHealthCheck() {
		state, _ := conn.getStatus()
		return false, fmt.Errorf("无法开始健康检查: state=%s", state)
	}

	// 4. 确保健康检查后状态恢复
	// recordHealthCheck 会处理状态恢复 StateChecking → StateAcquired
	defer func() {
		if !success {
			ylog.Warnf("pool", "健康检查失败: connection_id=%s, error=%v", connID, err)
		}
	}()

	// 5. 执行健康检查（直接使用 conn.driver，不重新获取）
	start := time.Now()
	checkErr := p.defaultHealthCheck(conn.driver)
	duration := time.Since(start)

	// 6. 记录结果（内部会 StateChecking → StateAcquired）
	conn.recordHealthCheck(checkErr == nil, checkErr)

	// 7. 记录指标和事件
	if checkErr != nil {
		p.collector.IncrementHealthCheckFailed(proto)
		errorType := classifyHealthCheckError(checkErr)
		state, _ := conn.getStatus()
		p.sendEvent(EventHealthCheckFailed, proto, map[string]interface{}{
			"connection_id": connID,
			"error":         checkErr.Error(),
			"error_type":    errorType.String(),
			"state":         state.String(),
			"duration":      duration.String(),
		})
		return false, checkErr
	}

	p.collector.IncrementHealthCheckSuccess(proto)
	p.collector.RecordHealthCheckDuration(proto, duration)
	ylog.Debugf("pool", "健康检查成功: connection_id=%s, duration=%v", connID, duration)

	return true, nil
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
				// 获取连接状态（用于更新统计）
				connState, _ := conn.getStatus()

				// 获取池写锁进行删除（时间短）
				pool.mu.Lock()
				// 再次检查连接是否还在
				if _, stillExists := pool.connections[id]; stillExists {
					delete(pool.connections, id)

					// 更新连接池统计（保持一致性）
					switch connState {
					case StateIdle:
						if pool.stats.IdleConnections > 0 {
							pool.stats.IdleConnections--
						}
					default:
						// StateAcquired, StateExecuting, StateConnecting, StateChecking, StateClosing 等
						if pool.stats.ActiveConnections > 0 {
							pool.stats.ActiveConnections--
						}
					}

					pool.mu.Unlock()

					// 异步强制关闭连接
					go func(c *EnhancedPooledConnection) {
						defer func() {
							if r := recover(); r != nil {
								ylog.Errorf("connection_pool", "清理连接时发生panic: %v", r)
							}
						}()

						// 使用 forceClose 强制关闭，不管连接是否正在使用
						if err := c.forceClose(); err != nil {
							ylog.Warnf("connection_pool", "强制关闭连接失败: id=%s, error=%v", c.id, err)
						}

						// 更新指标和事件
						atomic.AddInt64(&pool.stats.DestroyedConnections, 1)
						p.collector.IncrementConnectionsDestroyed(proto)
						p.sendEvent(EventConnectionDestroyed, proto, c)
						ylog.Debugf("connection_pool", "连接清理完成: id=%s, protocol=%s, state=%s", c.id, proto, connState)
					}(conn)
					removedCount++
				} else {
					pool.mu.Unlock()
				}
			}
		}

		if removedCount > 0 {
			ylog.Infof("connection_pool", "清理完成: 移除了 %d 个连接, protocol=%s", removedCount, proto)
		}
	}
}

// shouldCleanupConnection 判断是否应该清理连接
//
// 清理任务只负责三种清理：
// 1. 不健康连接 - 立即清理
// 2. 僵尸连接 - 超时清理（所有非Idle状态的超时检测）
// 3. 空闲超时 - 定期清理（只删除，不创建）
//
// 注意：生命周期超时、使用次数超限由重建任务负责（会创建新连接替换）
func (p *EnhancedConnectionPool) shouldCleanupConnection(conn *EnhancedPooledConnection, now time.Time) bool {
	// 前置检查：使用计数 > 0 的连接不应该清理
	if conn.getUseCount() > 0 {
		return false
	}

	state, health := conn.getStatus()
	lastUsed := conn.getLastUsed()

	// ========== 【第一优先级】不健康连接 - 立即清理 ==========
	if health == HealthStatusUnhealthy {
		ylog.Infof("connection_pool",
			"清理不健康连接: id=%s, state=%s, rebuilding=%v",
			conn.id, state, conn.rebuilding)
		return true // 即使 rebuilding=true，也立即清理
	}

	// ========== 【第二优先级】僵尸连接 - 状态超时检测 ==========
	// 检查所有非Idle状态（包括 rebuilding 的连接）
	if state != StateIdle {
		idleTime := now.Sub(lastUsed)

		// 根据状态设置不同超时
		var timeout time.Duration
		var reason string

		switch state {
		case StateClosing:
			timeout = p.config.StuckTimeoutClosing
			if timeout == 0 {
				timeout = 1 * time.Minute // 默认1分钟
			}
			reason = "关闭超时"

		case StateConnecting:
			timeout = p.config.StuckTimeoutConnecting
			if timeout == 0 {
				timeout = 30 * time.Second // 默认30秒
			}
			reason = "连接超时"

		case StateAcquired:
			timeout = p.config.StuckTimeoutAcquired
			if timeout == 0 {
				timeout = 10 * time.Minute // 默认10分钟
			}
			reason = "获取超时"

		case StateExecuting:
			timeout = p.config.StuckTimeoutExecuting
			if timeout == 0 {
				timeout = 10 * time.Minute // 默认10分钟
			}
			reason = "执行超时"

		case StateChecking:
			timeout = p.config.StuckTimeoutChecking
			if timeout == 0 {
				timeout = 2 * time.Minute // 默认2分钟
			}
			reason = "检查超时"

		case StateClosed:
			// 终态，立即清理
			ylog.Infof("connection_pool", "清理已关闭连接: id=%s", conn.id)
			return true

		default:
			timeout = 6 * time.Minute
			reason = "状态异常"
		}

		// 超时检测：即使 rebuilding=true，超时也强制清理
		if idleTime > timeout {
			ylog.Warnf("connection_pool",
				"清理%s连接: id=%s, state=%s, rebuilding=%v, idle=%v",
				reason, conn.id, state, conn.rebuilding, idleTime)
			return true // 强制清理，中断可能的重建
		}

		// 未超时的非Idle状态，不清理
		return false
	}

	// ========== 【第三优先级】Idle + rebuilding 异常 ==========
	// Idle 状态但在 rebuilding，且长时间未转换状态
	if state == StateIdle && conn.rebuilding {
		rebuildWaitTime := now.Sub(lastUsed)
		timeout := p.config.StuckTimeoutRebuildIdle
		if timeout == 0 {
			timeout = 2 * time.Minute // 默认2分钟
		}
		if rebuildWaitTime > timeout {
			ylog.Warnf("connection_pool",
				"清理重建异常的Idle连接: id=%s, rebuilding=%v, wait=%v",
				conn.id, conn.rebuilding, rebuildWaitTime)
			return true
		}
		// 正在重建，等待状态转换
		return false
	}

	// ========== 【第四优先级】空闲超时 - 只清理，不重建 ==========
	if state == StateIdle {
		// 只检查空闲超时（清理任务只删除，不创建）
		// 生命周期超时、使用次数超限由重建任务负责（会创建新连接）
		if now.Sub(lastUsed) > p.idleTimeout {
			ylog.Infof("connection_pool",
				"清理空闲超时连接: id=%s, idle=%v, timeout=%v",
				conn.id, now.Sub(lastUsed), p.idleTimeout)
			return true // 只删除，任务执行时自动创建新连接
		}
	}

	// ========== 不再处理：生命周期超时、使用次数超限 ==========
	// 这些条件由重建任务负责，会创建新连接替换

	return false
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
			if !conn.isIdle() {
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
	case EventRebuildMarked:
		// 标记需要重建
		if data, ok := event.Data.(map[string]interface{}); ok {
			eventStr = fmt.Sprintf("标记需要重建: protocol=%s, conn_id=%v, reason=%v",
				event.Protocol,
				data["connection_id"],
				data["reason"])
		} else {
			eventStr = fmt.Sprintf("标记需要重建: protocol=%s, data=%v", event.Protocol, event.Data)
		}
	case EventRebuildStarted:
		// 重建开始
		if data, ok := event.Data.(map[string]interface{}); ok {
			eventStr = fmt.Sprintf("重建开始: protocol=%s, conn_id=%v",
				event.Protocol,
				data["connection_id"])
		} else {
			eventStr = fmt.Sprintf("重建开始: protocol=%s, data=%v", event.Protocol, event.Data)
		}
	case EventRebuildCompleted:
		// 重建完成
		if data, ok := event.Data.(map[string]interface{}); ok {
			eventStr = fmt.Sprintf("重建完成: protocol=%s, conn_id=%v, duration=%v",
				event.Protocol,
				data["connection_id"],
				data["duration"])
		} else {
			eventStr = fmt.Sprintf("重建完成: protocol=%s, data=%v", event.Protocol, event.Data)
		}
	case EventConnectionsNeedRebuild:
		// 连接需要重建统计
		if data, ok := event.Data.(map[string]interface{}); ok {
			eventStr = fmt.Sprintf("连接需要重建统计: protocol=%s, count=%v",
				event.Protocol,
				data["count"])
		} else {
			eventStr = fmt.Sprintf("连接需要重建统计: protocol=%s, data=%v", event.Protocol, event.Data)
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
	if p.rebuildManager == nil {
		return ""
	}
	return p.rebuildManager.GetRebuildReason(conn)
}

// shouldRebuildConnection 判断连接是否需要重建（测试用包装函数）
func (p *EnhancedConnectionPool) shouldRebuildConnection(conn *EnhancedPooledConnection) bool {
	if p.rebuildManager == nil {
		return false
	}
	return p.rebuildManager.ShouldRebuild(conn)
}

// rebuildTask 定时重建任务
func (p *EnhancedConnectionPool) rebuildTask() {
	rebuildInterval := p.config.RebuildCheckInterval
	if rebuildInterval == 0 {
		rebuildInterval = 5 * time.Minute // 默认5分钟
	}

	ylog.Infof("pool", "重建任务启动，检查间隔: %v", rebuildInterval)

	ticker := time.NewTicker(rebuildInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.parentCtx.Done():
			ylog.Infof("pool", "重建任务停止（父级上下文取消）")
			return
		case <-p.ctx.Done():
			ylog.Infof("pool", "重建任务停止（上下文取消）")
			return
		case <-ticker.C:
			ylog.Debugf("pool", "定时重建检查触发")
			p.performRebuilds()
		}
	}
}

// performRebuilds 执行重建检查
func (p *EnhancedConnectionPool) performRebuilds() {
	if p.rebuildManager == nil || !p.config.SmartRebuildEnabled {
		return
	}

	protocols := p.getAllProtocols()
	if len(protocols) == 0 {
		return
	}

	// 为每个协议执行重建检查（并发控制）
	semaphore := make(chan struct{}, p.config.RebuildConcurrency)
	for _, proto := range protocols {
		go func(proto Protocol) {
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			// 直接调用批量重建API
			_, err := p.RebuildConnectionByProto(p.ctx, proto)
			if err != nil {
				ylog.Warnf("pool", "协议 %s 定时重建失败: %v", proto, err)
			}
		}(proto)
	}
}

// getAllConnectionsByProto 获取指定协议的所有连接
func (p *EnhancedConnectionPool) getAllConnectionsByProto(proto Protocol) []*EnhancedPooledConnection {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var conns []*EnhancedPooledConnection
	driverPool, exists := p.pools[proto]
	if !exists {
		return conns
	}

	driverPool.mu.RLock()
	defer driverPool.mu.RUnlock()

	conns = make([]*EnhancedPooledConnection, 0, len(driverPool.connections))
	for _, conn := range driverPool.connections {
		conns = append(conns, conn)
	}

	return conns
}

// getConnectionsForRebuild 获取需要重建的连接
func (p *EnhancedConnectionPool) getConnectionsForRebuild(proto Protocol) []*EnhancedPooledConnection {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var conns []*EnhancedPooledConnection
	driverPool, exists := p.pools[proto]
	if !exists {
		return conns
	}

	driverPool.mu.RLock()
	defer driverPool.mu.RUnlock()

	for _, conn := range driverPool.connections {
		if conn.isMarkedForRebuild() {
			conns = append(conns, conn)
		}
	}

	return conns
}

// findConnectionByID 根据连接ID查找连接
func (p *EnhancedConnectionPool) findConnectionByID(connID string) *EnhancedPooledConnection {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// 遍历所有协议的所有连接
	for _, driverPool := range p.pools {
		driverPool.mu.RLock()
		for _, conn := range driverPool.connections {
			if conn.id == connID {
				driverPool.mu.RUnlock()
				return conn
			}
		}
		driverPool.mu.RUnlock()
	}

	return nil
}

// replaceConnection 替换连接
func (p *EnhancedConnectionPool) replaceConnection(proto Protocol, connID string, newDriver ProtocolDriver) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	driverPool, exists := p.pools[proto]
	if !exists {
		return fmt.Errorf("协议不存在: %s", proto)
	}

	driverPool.mu.Lock()
	defer driverPool.mu.Unlock()

	// 查找旧连接
	var oldConn *EnhancedPooledConnection
	for _, conn := range driverPool.connections {
		if conn.id == connID {
			oldConn = conn
			break
		}
	}

	if oldConn == nil {
		return fmt.Errorf("连接不存在: %s", connID)
	}

	// 创建新连接对象
	newConn := &EnhancedPooledConnection{
		driver:       newDriver,
		id:           fmt.Sprintf("%s|%s|%d", p.config.Host, proto, time.Now().UnixNano()),
		protocol:     proto,
		createdAt:    time.Now(),
		state:        StateIdle,
		healthStatus: HealthStatusHealthy,
		labels:       oldConn.labels,
		metadata:     oldConn.metadata,
	}

	// 执行替换
	delete(driverPool.connections, oldConn.id)
	driverPool.connections[newConn.id] = newConn

	// 更新统计
	p.collector.IncrementConnectionsCreated(proto)

	ylog.Infof("pool", "替换连接: %s→%s", connID, newConn.id)
	return nil
}

// rebuildConnection 重建单个连接
// 返回值: (新连接ID, error)
func (p *EnhancedConnectionPool) rebuildConnection(ctx context.Context, conn *EnhancedPooledConnection) (string, error) {
	// 从连接对象获取信息
	connID := conn.id
	proto := conn.protocol

	// 获取重建原因（仅用于日志）
	// 重建原因应由上层调用者（健康检查、重建任务、手动API）设置
	rebuildReason := conn.getRebuildReason()
	if rebuildReason == "" {
		rebuildReason = "manual_rebuild" // 默认原因
	}

	ylog.Infof("pool", "开始重建连接: id=%s, protocol=%s, reason=%s", connID, proto, rebuildReason)

	// 发送重建开始事件并收集指标
	if p.rebuildManager != nil {
		p.rebuildManager.sendRebuildStartedEvent(conn)
	}

	startTime := time.Now()

	// 调用核心重建函数（内部会获取驱动池）
	newConnID, err := p.performCoreRebuild(ctx, conn)

	// 记录指标和事件
	duration := time.Since(startTime)

	if err != nil {
		ylog.Errorf("pool", "连接重建失败: id=%s, protocol=%s, error=%v, duration=%v",
			connID, proto, err, duration)

		// 发送重建失败事件并收集指标
		if p.rebuildManager != nil {
			p.rebuildManager.sendRebuildCompletedEvent(conn, duration, false)
		}

		p.sendEvent(EventRebuildFailed, proto, map[string]interface{}{
			"connection_id": connID,
			"error":         err.Error(),
			"reason":        rebuildReason,
			"duration":      duration.Seconds(),
		})
		return "", fmt.Errorf("连接重建失败: %w", err)
	}

	// 发送重建完成事件并收集指标
	if p.rebuildManager != nil {
		p.rebuildManager.sendRebuildCompletedEvent(conn, duration, true)
	}

	ylog.Infof("pool", "连接重建完成: old_id=%s, new_id=%s, protocol=%s, reason=%s, duration=%v",
		connID, newConnID, proto, rebuildReason, duration)

	return newConnID, nil
}

// RebuildResult 单个连接重建结果
type RebuildResult struct {
	Protocol  string        `json:"protocol"`
	Success   bool          `json:"success"`
	OldConnID string        `json:"old_conn_id"`
	NewConnID string        `json:"new_conn_id,omitempty"`
	Duration  time.Duration `json:"duration"`
	Reason    string        `json:"reason"`
	Error     string        `json:"error,omitempty"`
	Timestamp time.Time     `json:"timestamp"`
}

// RebuildErrorCode 重建错误码
type RebuildErrorCode int

const (
	// RebuildErrorCodeNotFound 连接不存在
	RebuildErrorCodeNotFound RebuildErrorCode = iota + 1
	// RebuildErrorCodeInvalidState 连接状态不适合重建
	RebuildErrorCodeInvalidState
	// RebuildErrorCodeCreateFailed 创建新连接失败
	RebuildErrorCodeCreateFailed
	// RebuildErrorCodeReplaceFailed 替换连接失败
	RebuildErrorCodeReplaceFailed
	// RebuildErrorCodeTimeout 重建超时
	RebuildErrorCodeTimeout
	// RebuildErrorCodeCancelled 重建被取消
	RebuildErrorCodeCancelled
	// RebuildErrorCodePoolClosed 连接池已关闭
	RebuildErrorCodePoolClosed
)

// RebuildError 重建错误
type RebuildError struct {
	Code          RebuildErrorCode       `json:"code"`
	Message       string                 `json:"message"`
	Details       map[string]interface{} `json:"details,omitempty"`
	Retryable     bool                   `json:"retryable"`
	SuggestedFix  string                 `json:"suggested_fix,omitempty"`
	OriginalError error                  `json:"-"`
}

// Error 实现error接口
func (e *RebuildError) Error() string {
	return fmt.Sprintf("重建错误[%d]: %s", e.Code, e.Message)
}

// Unwrap 支持错误链
func (e *RebuildError) Unwrap() error {
	return e.OriginalError
}

// BatchRebuildResult 批量重建结果
type BatchRebuildResult struct {
	Total       int              `json:"total"`
	Success     int              `json:"success"`
	Failed      int              `json:"failed"`
	Results     []*RebuildResult `json:"results"`
	StartTime   time.Time        `json:"start_time"`
	EndTime     time.Time        `json:"end_time"`
	Duration    time.Duration    `json:"duration"`
	Errors      []string         `json:"errors,omitempty"`
	Cancelled   bool             `json:"cancelled,omitempty"`
	CancelError string           `json:"cancel_error,omitempty"`
}

// RebuildConnectionByProto 重建指定协议下的所有需要重建的连接
// RebuildConnectionByProto 重建指定协议下的所有需要重建的连接（带上下文）
func (p *EnhancedConnectionPool) RebuildConnectionByProto(ctx context.Context, proto Protocol) (*BatchRebuildResult, error) {
	// 参数验证
	if proto == "" {
		return nil, &RebuildError{
			Code:         RebuildErrorCodeInvalidState,
			Message:      "协议类型不能为空",
			Retryable:    false,
			SuggestedFix: "请提供有效的协议类型",
		}
	}

	// 检查协议是否存在
	p.mu.RLock()
	_, exists := p.pools[proto]
	p.mu.RUnlock()
	if !exists {
		return nil, &RebuildError{
			Code:         RebuildErrorCodeInvalidState,
			Message:      fmt.Sprintf("不支持的协议类型: %s", proto),
			Retryable:    false,
			SuggestedFix: "请提供支持的协议类型",
		}
	}

	// 评估并标记需要重建的连接（智能重建）
	if p.config.SmartRebuildEnabled && p.rebuildManager != nil {
		allConns := p.getAllConnectionsByProto(proto)
		markedCount := 0
		for _, conn := range allConns {
			if p.rebuildManager.ShouldRebuild(conn) {
				markedCount++
			}
		}
		ylog.Debugf("pool", "协议 %s 重建评估完成: 总连接=%d, 标记=%d",
			proto, len(allConns), markedCount)
	}

	// 1. 获取需要重建的连接（只查询已标记的连接）
	conns := p.getConnectionsForRebuild(proto)
	if len(conns) == 0 {
		return &BatchRebuildResult{
			Total:     0,
			Success:   0,
			Failed:    0,
			Results:   []*RebuildResult{},
			StartTime: time.Now(),
			EndTime:   time.Now(),
			Duration:  0,
		}, nil
	}

	// 2. 执行批量重建（使用 worker pool 并发控制）
	startTime := time.Now()

	// 配置并发参数
	concurrency := p.config.RebuildConcurrency
	if concurrency <= 0 {
		concurrency = 3 // 默认并发数
	}

	batchSize := p.config.RebuildBatchSize
	if batchSize <= 0 {
		batchSize = 5 // 默认批量大小
	}

	// 创建任务通道和结果通道
	taskChan := make(chan *EnhancedPooledConnection, len(conns))
	resultChan := make(chan *RebuildResult, len(conns))

	// 启动 worker pool
	var wg sync.WaitGroup
	workerCount := concurrency
	if workerCount > len(conns) {
		workerCount = len(conns)
	}

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for conn := range taskChan {
				// 检查上下文是否已取消
				select {
				case <-ctx.Done():
					resultChan <- &RebuildResult{
						Protocol:  string(proto),
						Success:   false,
						OldConnID: conn.id,
						Error:     ctx.Err().Error(),
						Timestamp: time.Now(),
					}
					return
				default:
				}

				connStartTime := time.Now()
				rebuildResult, err := p.RebuildConnectionByID(ctx, conn.id)
				connDuration := time.Since(connStartTime)

				result := &RebuildResult{
					Protocol:  string(proto),
					OldConnID: conn.id,
					Duration:  connDuration,
					Reason:    conn.getRebuildReason(),
					Timestamp: time.Now(),
				}

				if err != nil {
					result.Success = false
					result.Error = err.Error()
					ylog.Warnf("pool", "重建连接失败: id=%s, error=%v", conn.id, err)
				} else if rebuildResult != nil {
					result.Success = rebuildResult.Success
					result.NewConnID = rebuildResult.NewConnID
					if rebuildResult.Reason != "" {
						result.Reason = rebuildResult.Reason
					}
				} else {
					// 理论上不应到达这里，作为防御性编程
					result.Success = false
					result.Error = "internal error: nil result without error"
					ylog.Errorf("pool", "异常状态: err=nil but rebuildResult=nil, conn_id=%s", conn.id)
				}

				resultChan <- result
			}
		}()
	}

	// 按批次分发任务，避免资源冲击
	for i := 0; i < len(conns); i += batchSize {
		end := i + batchSize
		if end > len(conns) {
			end = len(conns)
		}

		// 发送批次任务
		for _, conn := range conns[i:end] {
			select {
			case taskChan <- conn:
			case <-ctx.Done():
				break
			}
		}

		// 批次间短暂延迟，避免资源冲击
		if i+batchSize < len(conns) {
			select {
			case <-time.After(100 * time.Millisecond):
			case <-ctx.Done():
				break
			}
		}
	}

	close(taskChan)
	wg.Wait()
	close(resultChan)

	// 3. 收集结果
	results := make([]*RebuildResult, 0, len(conns))
	successCount := 0
	failedCount := 0
	var errors []string

	for result := range resultChan {
		results = append(results, result)
		if result.Success {
			successCount++
		} else {
			failedCount++
			if result.Error != "" {
				errors = append(errors, fmt.Sprintf("连接 %s: %s", result.OldConnID, result.Error))
			}
		}
	}

	// 4. 记录结果
	endTime := time.Now()
	duration := endTime.Sub(startTime)

	ylog.Infof("pool", "协议 %s 重建完成: 成功 %d/%d, 失败 %d, 耗时 %v, 并发度 %d",
		proto, successCount, len(conns), failedCount, duration, workerCount)

	batchResult := &BatchRebuildResult{
		Total:     len(conns),
		Success:   successCount,
		Failed:    failedCount,
		Results:   results,
		StartTime: startTime,
		EndTime:   endTime,
		Duration:  duration,
	}

	if failedCount > 0 {
		batchResult.Errors = errors
		if successCount == 0 {
			return batchResult, fmt.Errorf("所有连接重建失败: %s", strings.Join(errors, "; "))
		}
	}

	return batchResult, nil
}

// RebuildConnectionByProtoAsync 异步重建指定协议的所有需要重建的连接
// RebuildConnections 重建所有需要重建的连接（带上下文）
// RebuildConnections 重建所有需要重建的连接（带上下文）
func (p *EnhancedConnectionPool) RebuildConnections(ctx context.Context) (*BatchRebuildResult, error) {
	protocols := p.getAllProtocols()
	if len(protocols) == 0 {
		return &BatchRebuildResult{
			Total:     0,
			Success:   0,
			Failed:    0,
			Results:   []*RebuildResult{},
			StartTime: time.Now(),
			EndTime:   time.Now(),
			Duration:  0,
		}, nil
	}

	startTime := time.Now()
	var allResults []*RebuildResult
	var errors []string
	var mu sync.Mutex

	// 并发执行各协议的重建
	var wg sync.WaitGroup
	for _, proto := range protocols {
		wg.Add(1)
		go func(proto Protocol) {
			defer wg.Done()

			result, err := p.RebuildConnectionByProto(ctx, proto)

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				errors = append(errors, fmt.Sprintf("协议 %s: %v", proto, err))
			}
			if result != nil && result.Results != nil {
				// 将该协议的所有结果添加到总结果中
				allResults = append(allResults, result.Results...)
			}
		}(proto)
	}

	wg.Wait()

	// 统计总数
	totalSuccess := 0
	totalFailed := 0
	for _, r := range allResults {
		if r.Success {
			totalSuccess++
		} else {
			totalFailed++
		}
	}

	// 汇总结果
	endTime := time.Now()
	duration := endTime.Sub(startTime)

	batchResult := &BatchRebuildResult{
		Total:     len(allResults),
		Success:   totalSuccess,
		Failed:    totalFailed,
		Results:   allResults,
		StartTime: startTime,
		EndTime:   endTime,
		Duration:  duration,
	}

	// 检查是否被取消
	select {
	case <-ctx.Done():
		batchResult.Cancelled = true
		batchResult.CancelError = ctx.Err().Error()
		return batchResult, ctx.Err()
	default:
		// 继续
	}

	// 汇总错误
	if len(errors) > 0 {
		batchResult.Errors = errors
		if totalSuccess == 0 {
			return batchResult, fmt.Errorf("所有连接重建失败: %s", strings.Join(errors, "; "))
		}
		return batchResult, fmt.Errorf("部分连接重建失败: %s", strings.Join(errors, "; "))
	}

	ylog.Infof("pool", "全量重建完成: 协议数 %d, 连接数 %d, 成功 %d, 失败 %d, 耗时 %v",
		len(protocols), len(allResults), totalSuccess, totalFailed, duration)

	return batchResult, nil
}

// RebuildConnectionsAsync 异步重建所有需要重建的连接
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
		// 注意：这里不转换状态，因为 tryAcquireForTask() 已经转换到StateAcquired
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
// 注意：连接已经在 getConnectionFromPool 中通过 tryAcquireForTask() 获取
//
// 重要：连接已经在 tryAcquireForTask() 中标记为 inUse=true 并更新了 lastUsed 和 usageCount
// 这里只需要更新池统计和创建监控包装器
func (p *EnhancedConnectionPool) activateConnection(conn *EnhancedPooledConnection) ProtocolDriver {

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

// RebuildConnectionByID 根据连接ID重建连接（带上下文）
func (p *EnhancedConnectionPool) RebuildConnectionByID(ctx context.Context, connID string) (*RebuildResult, error) {
	// 1. 根据connID查找连接
	conn := p.findConnectionByID(connID)
	if conn == nil {
		return nil, &RebuildError{
			Code:      RebuildErrorCodeNotFound,
			Message:   fmt.Sprintf("连接不存在: %s", connID),
			Retryable: false,
			SuggestedFix: "请检查：\n" +
				"1. 连接ID是否正确\n" +
				"2. 连接是否已被关闭或删除\n" +
				"3. 连接池是否包含该连接",
		}
	}

	// 获取协议类型
	proto := conn.protocol

	// 2. 获取重建原因（仅用于日志和结果）
	// 重建原因由健康检查或定期重建任务设置，手动API只需读取和打印
	rebuildReason := conn.getRebuildReason()
	if rebuildReason == "" {
		rebuildReason = "manual_rebuild" // 默认原因
	}

	// 3. 执行重建（同步）
	// 注意：状态检查由核心层 (canStartRebuild, beginRebuild) 处理
	// 避免重复检查，保持单一职责
	startTime := time.Now()
	newConnID, err := p.rebuildConnection(ctx, conn)
	duration := time.Since(startTime)

	result := &RebuildResult{
		Protocol:  string(proto),
		Success:   err == nil,
		OldConnID: connID,
		NewConnID: newConnID,
		Duration:  duration,
		Reason:    rebuildReason,
		Timestamp: time.Now(),
	}

	if err != nil {
		result.Error = err.Error()

		// 转换核心层的错误为 RebuildError
		if rebuildErr, ok := err.(*RebuildError); ok {
			// 已经是 RebuildError，直接返回
			return nil, rebuildErr
		}

		// 核心层返回的是普通错误，包装为 RebuildError
		// 通常表示状态不适合重建或创建失败
		return nil, &RebuildError{
			Code:          RebuildErrorCodeInvalidState,
			Message:       fmt.Sprintf("连接重建失败: %s", connID),
			OriginalError: err,
			Retryable:     true,
			Details: map[string]interface{}{
				"connection_id": connID,
				"protocol":      proto,
				"state":         func() ConnectionState { s, _ := conn.getStatus(); return s }(),
				"error":         err.Error(),
				"reason":        rebuildReason,
			},
			SuggestedFix: "请检查：\n" +
				"1. 连接是否正在使用中\n" +
				"2. 连接是否正在重建中\n" +
				"3. 目标设备是否可达\n" +
				"4. 认证信息是否正确",
		}
	}

	return result, nil
}

// RebuildConnectionByIDAsync 异步重建指定ID的连接
func (p *EnhancedConnectionPool) RebuildConnectionByIDAsync(ctx context.Context, connID string) (<-chan *RebuildResult, error) {
	// 参数验证
	if connID == "" {
		return nil, &RebuildError{
			Code:         RebuildErrorCodeInvalidState,
			Message:      "连接ID不能为空",
			Retryable:    false,
			SuggestedFix: "请提供有效的连接ID",
		}
	}

	// 创建带缓冲的结果通道
	resultChan := make(chan *RebuildResult, 1)

	// 启动异步重建
	go func() {
		defer func() {
			// 确保通道被关闭
			close(resultChan)

			// 恢复可能的panic
			if r := recover(); r != nil {
				ylog.Errorf("pool", "RebuildConnectionByIDAsync panic: %v", r)
			}
		}()

		// 调用同步API执行重建
		result, err := p.RebuildConnectionByID(ctx, connID)

		// 确保总是返回一个结果对象
		if err != nil {
			// 同步API返回错误，创建包含错误信息的结果
			if result == nil {
				result = &RebuildResult{
					Success:   false,
					OldConnID: connID,
					Error:     err.Error(),
					Timestamp: time.Now(),
					Reason:    "async_rebuild_failed",
				}
			}
			// 如果result不为nil，它已经包含了错误信息
		}

		// 发送结果到通道
		resultChan <- result
	}()

	return resultChan, nil
}

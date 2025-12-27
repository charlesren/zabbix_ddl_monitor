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
	EventRebuildFailed
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
// performCoreRebuild 核心重建函数，包含优化的锁策略和错误处理
func (p *EnhancedConnectionPool) performCoreRebuild(pool *EnhancedDriverPool, oldID string, oldConn *EnhancedPooledConnection) error {
	// 阶段1：快速检查（不持有任何锁）
	if !p.canStartRebuild(oldConn, oldID) {
		return fmt.Errorf("连接 %s 不适合重建", oldID)
	}

	// 阶段2：开始重建（设置 isRebuilding 标记）
	if !oldConn.beginRebuild() {
		return fmt.Errorf("无法开始重建: id=%s", oldID)
	}

	// 声明错误变量
	var err error
	// 确保最终清除重建标记
	defer oldConn.completeRebuild(err == nil)

	// 阶段3：先关：关闭旧连接
	// 3.1 开始关闭
	if !oldConn.beginClose() {
		ylog.Warnf("connection_pool", "无法正常开始关闭，强制关闭连接: id=%s", oldID)
		// 强制设置状态
		oldConn.mu.Lock()
		oldConn.state = StateClosing
		oldConn.mu.Unlock()
	}

	// 3.2 立即关闭driver
	if oldConn.driver != nil {
		oldConn.driver.Close()
		oldConn.driver = nil
	}

	// 3.3 从池中移除连接
	pool.mu.Lock()
	delete(pool.connections, oldID)
	atomic.AddInt64(&pool.stats.IdleConnections, -1)
	pool.mu.Unlock()

	// 3.4 完成关闭
	oldConn.completeClose()

	// 阶段4：后建：创建新连接
	newConn, err := p.createReplacementConnection(pool, oldConn)
	if err != nil {
		ylog.Warnf("connection_pool", "先关后建失败: 关闭成功但创建失败, id=%s, error=%v", oldID, err)
		return fmt.Errorf("创建新连接失败: %w", err)
	}

	// 阶段5：添加新连接到池中
	pool.mu.Lock()
	pool.connections[newConn.id] = newConn
	atomic.AddInt64(&pool.stats.CreatedConnections, 1)
	atomic.AddInt64(&pool.stats.IdleConnections, 1)
	pool.mu.Unlock()

	// 阶段6：完成重建操作
	p.completeRebuild(pool, oldID, oldConn, newConn)

	ylog.Infof("connection_pool", "先关后建成功: 旧连接=%s (已关闭), 新连接=%s", oldID, newConn.id)

	return nil
}

func (p *EnhancedConnectionPool) asyncRebuildConnection(pool *EnhancedDriverPool, oldID string, oldConn *EnhancedPooledConnection) {
	defer func() {
		if r := recover(); r != nil {
			ylog.Errorf("connection_pool", "async rebuild panic: %v", r)
		}
		oldConn.clearRebuildMark()
	}()

	// 调用核心重建函数
	if err := p.performCoreRebuild(pool, oldID, oldConn); err != nil {
		ylog.Debugf("connection_pool", "异步重建失败: id=%s, error=%v", oldID, err)
	}
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

	// 检查连接是否正在重建中
	if oldConn.isRebuilding() {
		ylog.Infof("connection_pool", "连接 %s 正在重建中，跳过", oldID)
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

	// 重建任务
	if p.config.SmartRebuildEnabled {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.rebuildTask()
		}()
	}

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
func (p *EnhancedConnectionPool) performHealthCheckForProtocol(proto Protocol) {
	// 1. 尝试获取连接（获取空闲连接）
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	driver, err := p.GetWithContext(ctx, proto)
	cancel()

	if err != nil {
		// 获取不到空闲连接，跳过
		ylog.Infof("pool", "健康检查跳过: protocol=%s, 无空闲连接, error=%v", proto, err)
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
	connID := conn.id

	// 3. 检查连接是否太新（与原有逻辑一致）
	minAgeForHealthCheck := 15 * time.Second
	now := time.Now()
	createdAt := conn.getCreatedAt()
	if now.Sub(createdAt) < minAgeForHealthCheck {
		ylog.Infof("pool", "跳过健康检查：连接 %s 太新（%v < %v）",
			connID, now.Sub(createdAt), minAgeForHealthCheck)
		p.Release(driver)
		return
	}

	// 4. 开始健康检查（设置Checking状态）
	if !conn.beginHealthCheck() {
		state, _ := conn.getStatus()
		ylog.Infof("pool", "无法开始健康检查: protocol=%s, connection_id=%s, state=%s",
			proto, connID, state)
		p.Release(driver)
		return
	}

	ylog.Infof("pool", "开始健康检查: protocol=%s, connection_id=%s", proto, connID)

	// 5. 执行健康检查
	start := time.Now()
	healthCheckErr := p.defaultHealthCheck(driver)
	duration := time.Since(start)

	// 6. 记录结果到连接对象
	conn.recordHealthCheck(healthCheckErr == nil, healthCheckErr)

	// 7. 释放连接
	releaseErr := p.Release(driver)
	if releaseErr != nil {
		ylog.Warnf("pool", "健康检查释放连接失败: protocol=%s, connection_id=%s, error=%v",
			proto, connID, releaseErr)
	}

	// 8. 记录指标和事件（与原有逻辑一致）
	if healthCheckErr != nil {
		// 错误分类日志
		errorType := classifyHealthCheckError(healthCheckErr)
		switch errorType {
		case HealthErrorTimeout:
			ylog.Warnf("pool", "connection %s health check timeout: %v", connID, healthCheckErr)
		case HealthErrorNetwork:
			ylog.Warnf("pool", "connection %s network error: %v", connID, healthCheckErr)
		case HealthErrorAuth:
			ylog.Errorf("pool", "connection %s authentication error: %v", connID, healthCheckErr)
		case HealthErrorCommand:
			ylog.Warnf("pool", "connection %s command error: %v", connID, healthCheckErr)
		case HealthErrorProtocol:
			ylog.Errorf("pool", "connection %s protocol error: %v", connID, healthCheckErr)
		default:
			ylog.Warnf("pool", "connection %s health check failed: %v", connID, healthCheckErr)
		}

		p.collector.IncrementHealthCheckFailed(proto)
		state, _ := conn.getStatus()
		p.sendEvent(EventHealthCheckFailed, proto, map[string]interface{}{
			"connection_id": connID,
			"error":         healthCheckErr.Error(),
			"error_type":    errorType.String(),
			"state":         state.String(),
			"duration":      duration.String(),
		})
	} else {
		ylog.Infof("pool", "健康检查成功: protocol=%s, connection_id=%s, duration=%v",
			proto, connID, duration)
		p.collector.IncrementHealthCheckSuccess(proto)
	}

	// 9. 记录健康检查时长指标（与原有逻辑一致）
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

	// 获取连接状态和健康状态
	state, health := conn.getStatus()

	// 特殊处理：不健康的连接应该被清理，无论状态如何
	// 这是为了避免连接卡在Checking状态
	if health == HealthStatusUnhealthy {
		ylog.Infof("connection_pool", "连接不健康需要清理：%s 状态=%s 健康=%s", conn.id, state, health)
		return true
	}

	// 第二层检查：以下状态的连接不应该被清理（健康的）
	nonCleanableStates := []ConnectionState{
		StateConnecting, // 连接中
		StateAcquired,   // 已获取
		StateExecuting,  // 执行中
		StateChecking,   // 检查中（健康的）
		StateClosing,    // 关闭中
	}

	for _, s := range nonCleanableStates {
		if state == s {
			ylog.Infof("connection_pool", "跳过清理：连接 %s 状态=%s", conn.id, state)
			return false
		}
	}

	// 只有 StateIdle 状态的连接可以被清理
	// 检查连接有效性（StateClosed 状态也应该被清理）
	valid := state != StateClosed && state != StateClosing
	if !valid {
		ylog.Infof("connection_pool", "连接无效需要清理：%s 状态=%s", conn.id, state)
		return true
	}

	// 检查空闲时间
	lastUsed := conn.getLastUsed()
	createdAt := conn.getCreatedAt()
	usageCount := conn.getUsageCount()

	if now.Sub(lastUsed) > p.idleTimeout {
		ylog.Infof("connection_pool", "连接空闲超时需要清理：%s 最后使用=%v 空闲超时=%v",
			conn.id, lastUsed, p.idleTimeout)
		return true
	}

	// 检查连接生命周期
	lifecycle := p.pools[conn.protocol].connectionLifecycle
	if now.Sub(createdAt) > lifecycle.maxLifetime {
		ylog.Infof("connection_pool", "连接生命周期超时需要清理：%s 创建时间=%v 最大生命周期=%v",
			conn.id, createdAt, lifecycle.maxLifetime)
		return true
	}

	// 检查使用次数
	if usageCount > lifecycle.maxUsageCount {
		ylog.Infof("connection_pool", "连接使用次数超限需要清理：%s 使用次数=%d 最大使用次数=%d",
			conn.id, usageCount, lifecycle.maxUsageCount)
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
	if conn.isInUse() || conn.isRebuilding() || state == StateClosing || state == StateClosed {
		ylog.Debugf("connection_pool", "连接状态不适合重建: id=%s, state=%s, isRebuilding=%v",
			conn.id, state, conn.isRebuilding())
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
			reason := fmt.Sprintf("usage_count_exceeded: usage=%d, max=%d", usageCount, p.config.RebuildMaxUsageCount)
			if conn.markForRebuildWithReason(reason) {
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
			reason := fmt.Sprintf("age_exceeded: age=%v, max=%v", age, p.config.RebuildMaxAge)
			if conn.markForRebuildWithReason(reason) {
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
					reason := fmt.Sprintf("error_rate_exceeded: requests=%d, errors=%d, rate=%.2f, max=%.2f",
						totalRequests, totalErrors, errorRate, p.config.RebuildMaxErrorRate)
					if conn.markForRebuildWithReason(reason) {
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
			reason := fmt.Sprintf("all_conditions_met: conditions=%d/3", conditions)
			if conn.markForRebuildWithReason(reason) {
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
			reason := fmt.Sprintf("usage_count_exceeded: usage=%d, max=%d", usageCount, p.config.RebuildMaxUsageCount)
			if conn.markForRebuildWithReason(reason) {
				return true
			} else {
				return false
			}
		}

		age := now.Sub(conn.getCreatedAt())
		if age >= p.config.RebuildMaxAge {
			// 使用原子操作确保只有一个goroutine能将连接标记为重建
			reason := fmt.Sprintf("age_exceeded: age=%v, max=%v", age, p.config.RebuildMaxAge)
			if conn.markForRebuildWithReason(reason) {
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
					reason := fmt.Sprintf("error_rate_exceeded: requests=%d, errors=%d, rate=%.2f, max=%.2f",
						totalRequests, totalErrors, errorRate, p.config.RebuildMaxErrorRate)
					if conn.markForRebuildWithReason(reason) {
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
			p.performRebuildForProtocol(proto)
		}(proto)
	}
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

// checkConnectionStateForRebuild 检查连接状态是否适合重建
func (p *EnhancedConnectionPool) checkConnectionStateForRebuild(conn *EnhancedPooledConnection) bool {
	// 获取连接状态
	state, health := conn.getStatus()

	// 连接必须处于空闲状态才能重建
	if state != StateIdle {
		ylog.Debugf("pool", "连接状态不适合重建: id=%s, state=%s (需要StateIdle)", conn.id, state)
		return false
	}

	// 连接不能已经标记为重建中
	if conn.isMarkedForRebuild() {
		ylog.Debugf("pool", "连接已标记为重建中: id=%s", conn.id)
		return false
	}

	// 连接不能处于关闭或关闭中状态
	if state == StateClosing || state == StateClosed {
		ylog.Debugf("pool", "连接正在关闭或已关闭: id=%s, state=%s", conn.id, state)
		return false
	}

	// 连接不能处于重建中状态
	if conn.isRebuilding() {
		ylog.Debugf("pool", "连接正在重建中: id=%s", conn.id)
		return false
	}

	// 连接健康状态检查（可选，根据配置决定）
	// 这里只是检查，实际是否重建由shouldRebuildConnection决定
	ylog.Debugf("pool", "连接状态适合重建: id=%s, state=%s, health=%s", conn.id, state, health)
	return true
}

// performRebuildForProtocol 为特定协议执行重建
func (p *EnhancedConnectionPool) performRebuildForProtocol(proto Protocol) {
	// 1. 获取该协议所有需要重建的连接
	conns := p.getConnectionsForRebuild(proto)
	if len(conns) == 0 {
		return
	}

	// 2. 批量处理（控制并发）
	batchSize := p.config.RebuildBatchSize
	if batchSize <= 0 {
		batchSize = 5 // 默认批量大小
	}

	for i := 0; i < len(conns); i += batchSize {
		end := i + batchSize
		if end > len(conns) {
			end = len(conns)
		}

		batch := conns[i:end]
		for _, conn := range batch {
			go p.rebuildConnection(proto, conn)
		}

		// 批次间延迟，避免资源冲击
		time.Sleep(100 * time.Millisecond)
	}
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
func (p *EnhancedConnectionPool) rebuildConnection(proto Protocol, conn *EnhancedPooledConnection) error {
	connID := conn.id

	// 检查连接是否标记为需要重建
	// 如果未标记（手动API场景），先标记连接
	var rebuildReason string
	if conn.isMarkedForRebuild() {
		rebuildReason = conn.getRebuildReason()
		ylog.Debugf("pool", "重建已标记的连接: id=%s, reason=%s", connID, rebuildReason)
	} else {
		// 手动API场景：连接未标记，需要先标记
		rebuildReason = "manual_rebuild"
		if !conn.markForRebuildWithReason(rebuildReason) {
			// 标记失败，可能已被其他goroutine标记
			ylog.Debugf("pool", "连接已被其他goroutine标记: id=%s", connID)
			rebuildReason = conn.getRebuildReason() // 获取新的重建原因
		}
	}

	ylog.Infof("pool", "开始重建连接: id=%s, protocol=%s, reason=%s", connID, proto, rebuildReason)

	startTime := time.Now()

	// 1. 获取驱动池
	driverPool := p.getDriverPool(proto)
	if driverPool == nil {
		ylog.Errorf("pool", "驱动池不存在: protocol=%s", proto)
		// 注意：这里不调用completeRebuild，因为连接还没有开始重建
		p.sendEvent(EventRebuildFailed, proto, map[string]interface{}{
			"connection_id": connID,
			"error":         "driver pool not found",
			"reason":        "pool_not_found",
		})
		return fmt.Errorf("驱动池不存在: %s", proto)
	}

	// 2. 调用核心重建函数
	err := p.performCoreRebuild(driverPool, connID, conn)

	// 3. 记录指标和事件
	duration := time.Since(startTime)

	if err != nil {
		ylog.Errorf("pool", "连接重建失败: id=%s, protocol=%s, error=%v, duration=%v",
			connID, proto, err, duration)
		// p.collector.IncrementRebuildFailed(proto) // TODO: 在第四阶段实现
		p.sendEvent(EventRebuildFailed, proto, map[string]interface{}{
			"connection_id": connID,
			"error":         err.Error(),
			"reason":        "rebuild_failed",
			"duration":      duration.Seconds(),
		})
		return fmt.Errorf("连接重建失败: %w", err)
	}

	ylog.Infof("pool", "连接重建完成: id=%s, protocol=%s, reason=%s, duration=%v",
		connID, proto, rebuildReason, duration)

	return nil
}

// RebuildResult 单个连接重建结果
type RebuildResult struct {
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
	Protocol    Protocol         `json:"protocol"`
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

// FullRebuildResult 全量重建结果
type FullRebuildResult struct {
	TotalProtocols   int                              `json:"total_protocols"`
	TotalConnections int                              `json:"total_connections"`
	Success          int                              `json:"success"`
	Failed           int                              `json:"failed"`
	ProtocolResults  map[Protocol]*BatchRebuildResult `json:"protocol_results"`
	StartTime        time.Time                        `json:"start_time"`
	EndTime          time.Time                        `json:"end_time"`
	Duration         time.Duration                    `json:"duration"`
	Errors           []string                         `json:"errors,omitempty"`
	Cancelled        bool                             `json:"cancelled,omitempty"`
	CancelError      string                           `json:"cancel_error,omitempty"`
}

// RebuildConnectionByProto 重建指定协议下的所有需要重建的连接
func (p *EnhancedConnectionPool) RebuildConnectionByProto(proto Protocol) (int, error) {
	result, err := p.RebuildConnectionByProtoWithContext(context.Background(), proto)
	if err != nil {
		return 0, err
	}
	return result.Success, nil
}

// RebuildConnectionByProtoWithContext 重建指定协议下的所有需要重建的连接（带上下文）
func (p *EnhancedConnectionPool) RebuildConnectionByProtoWithContext(ctx context.Context, proto Protocol) (*BatchRebuildResult, error) {
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

	// 1. 获取需要重建的连接
	conns := p.getConnectionsForRebuild(proto)
	if len(conns) == 0 {
		return &BatchRebuildResult{
			Protocol:  proto,
			Total:     0,
			Success:   0,
			Failed:    0,
			Results:   []*RebuildResult{},
			StartTime: time.Now(),
			EndTime:   time.Now(),
			Duration:  0,
		}, nil
	}

	// 2. 执行批量重建
	startTime := time.Now()
	results := make([]*RebuildResult, 0, len(conns))
	successCount := 0
	failedCount := 0
	var errors []string

	for _, conn := range conns {
		// 检查上下文是否已取消
		select {
		case <-ctx.Done():
			return &BatchRebuildResult{
				Protocol:    proto,
				Total:       len(conns),
				Success:     successCount,
				Failed:      failedCount,
				Results:     results,
				StartTime:   startTime,
				EndTime:     time.Now(),
				Duration:    time.Since(startTime),
				Cancelled:   true,
				CancelError: ctx.Err().Error(),
			}, ctx.Err()
		default:
			// 继续执行
		}

		connStartTime := time.Now()
		err := p.rebuildConnection(proto, conn)
		connDuration := time.Since(connStartTime)

		result := &RebuildResult{
			Success:   err == nil,
			OldConnID: conn.id,
			Duration:  connDuration,
			Reason:    conn.getRebuildReason(),
			Timestamp: time.Now(),
		}

		if err != nil {
			result.Error = err.Error()
			failedCount++
			errors = append(errors, fmt.Sprintf("连接 %s: %v", conn.id, err))
			ylog.Warnf("pool", "重建连接失败: id=%s, error=%v", conn.id, err)
		} else {
			result.NewConnID = conn.id
			successCount++
		}

		results = append(results, result)
	}

	// 3. 记录结果
	endTime := time.Now()
	duration := endTime.Sub(startTime)

	ylog.Infof("pool", "协议 %s 重建完成: 成功 %d/%d, 耗时 %v",
		proto, successCount, len(conns), duration)

	result := &BatchRebuildResult{
		Protocol:  proto,
		Total:     len(conns),
		Success:   successCount,
		Failed:    failedCount,
		Results:   results,
		StartTime: startTime,
		EndTime:   endTime,
		Duration:  duration,
	}

	if failedCount > 0 {
		result.Errors = errors
		if successCount == 0 {
			return result, fmt.Errorf("所有连接重建失败: %s", strings.Join(errors, "; "))
		}
	}

	return result, nil
}

// RebuildConnectionByProtoAsync 异步重建指定协议的所有需要重建的连接
func (p *EnhancedConnectionPool) RebuildConnectionByProtoAsync(ctx context.Context, proto Protocol) (<-chan *BatchRebuildResult, error) {
	// 参数验证
	if proto == "" {
		return nil, &RebuildError{
			Code:         RebuildErrorCodeInvalidState,
			Message:      "协议类型不能为空",
			Retryable:    false,
			SuggestedFix: "请提供有效的协议类型",
		}
	}

	// 创建带缓冲的结果通道，容量为10以支持进度更新
	resultChan := make(chan *BatchRebuildResult, 10)

	// 启动异步重建
	go func() {
		defer func() {
			// 确保通道被关闭
			close(resultChan)

			// 恢复可能的panic
			if r := recover(); r != nil {
				ylog.Errorf("pool", "RebuildConnectionByProtoAsync panic: %v", r)
			}
		}()

		// 先获取需要重建的连接列表
		conns := p.getConnectionsForRebuild(proto)
		total := len(conns)

		if total == 0 {
			// 没有需要重建的连接，立即返回空结果
			resultChan <- &BatchRebuildResult{
				Protocol:  proto,
				Total:     0,
				Success:   0,
				Failed:    0,
				Results:   []*RebuildResult{},
				StartTime: time.Now(),
				EndTime:   time.Now(),
				Duration:  0,
			}
			return
		}

		startTime := time.Now()
		results := make([]*RebuildResult, 0, total)
		successCount := 0
		failedCount := 0
		var errors []string

		// 分批处理，每完成一批发送进度更新
		for i, conn := range conns {
			// 检查上下文是否已取消
			select {
			case <-ctx.Done():
				// 上下文被取消，发送最终结果
				resultChan <- &BatchRebuildResult{
					Protocol:    proto,
					Total:       total,
					Success:     successCount,
					Failed:      failedCount,
					Results:     results,
					StartTime:   startTime,
					EndTime:     time.Now(),
					Duration:    time.Since(startTime),
					Cancelled:   true,
					CancelError: ctx.Err().Error(),
				}
				return
			default:
				// 继续执行
			}

			// 重建单个连接
			connStartTime := time.Now()
			err := p.rebuildConnection(proto, conn)
			connDuration := time.Since(connStartTime)

			result := &RebuildResult{
				Success:   err == nil,
				OldConnID: conn.id,
				Duration:  connDuration,
				Reason:    conn.getRebuildReason(),
				Timestamp: time.Now(),
			}

			if err != nil {
				result.Error = err.Error()
				failedCount++
				errors = append(errors, fmt.Sprintf("连接 %s: %v", conn.id, err))
			} else {
				result.NewConnID = conn.id
				successCount++
			}

			results = append(results, result)

			// 每完成10%或每10个连接发送进度更新
			if (i+1)%10 == 0 || (i+1) == total {
				batchResult := &BatchRebuildResult{
					Protocol:  proto,
					Total:     total,
					Success:   successCount,
					Failed:    failedCount,
					Results:   results,
					StartTime: startTime,
					EndTime:   time.Now(),
					Duration:  time.Since(startTime),
				}

				if failedCount > 0 {
					batchResult.Errors = errors
				}

				// 发送进度更新
				select {
				case resultChan <- batchResult:
					// 进度更新已发送
				default:
					// 通道已满，跳过本次更新（避免阻塞）
					ylog.Warnf("pool", "进度更新通道已满，跳过更新")
				}
			}
		}

		// 发送最终结果
		endTime := time.Now()
		duration := endTime.Sub(startTime)

		finalResult := &BatchRebuildResult{
			Protocol:  proto,
			Total:     total,
			Success:   successCount,
			Failed:    failedCount,
			Results:   results,
			StartTime: startTime,
			EndTime:   endTime,
			Duration:  duration,
		}

		if failedCount > 0 {
			finalResult.Errors = errors
		}

		resultChan <- finalResult
	}()

	return resultChan, nil
}

// RebuildConnections 重建所有需要重建的连接
func (p *EnhancedConnectionPool) RebuildConnections() (map[Protocol]int, error) {
	result, err := p.RebuildConnectionsWithContext(context.Background())
	if err != nil {
		return nil, err
	}

	// 转换为旧格式以保持兼容性
	legacyResults := make(map[Protocol]int)
	for proto, protoResult := range result.ProtocolResults {
		legacyResults[proto] = protoResult.Success
	}

	return legacyResults, nil
}

// RebuildConnectionsWithContext 重建所有需要重建的连接（带上下文）
func (p *EnhancedConnectionPool) RebuildConnectionsWithContext(ctx context.Context) (*FullRebuildResult, error) {
	protocols := p.getAllProtocols()
	if len(protocols) == 0 {
		return &FullRebuildResult{
			TotalProtocols:   0,
			TotalConnections: 0,
			Success:          0,
			Failed:           0,
			ProtocolResults:  make(map[Protocol]*BatchRebuildResult),
			StartTime:        time.Now(),
			EndTime:          time.Now(),
			Duration:         0,
		}, nil
	}

	startTime := time.Now()
	protocolResults := make(map[Protocol]*BatchRebuildResult)
	var errors []string
	totalSuccess := 0
	totalFailed := 0
	totalConnections := 0

	// 并发执行各协议的重建
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, proto := range protocols {
		wg.Add(1)
		go func(proto Protocol) {
			defer wg.Done()

			result, err := p.RebuildConnectionByProtoWithContext(ctx, proto)

			mu.Lock()
			protocolResults[proto] = result
			if err != nil {
				errors = append(errors, fmt.Sprintf("协议 %s: %v", proto, err))
			}
			if result != nil {
				totalSuccess += result.Success
				totalFailed += result.Failed
				totalConnections += result.Total
			}
			mu.Unlock()
		}(proto)
	}

	wg.Wait()

	// 汇总结果
	endTime := time.Now()
	duration := endTime.Sub(startTime)

	fullResult := &FullRebuildResult{
		TotalProtocols:   len(protocols),
		TotalConnections: totalConnections,
		Success:          totalSuccess,
		Failed:           totalFailed,
		ProtocolResults:  protocolResults,
		StartTime:        startTime,
		EndTime:          endTime,
		Duration:         duration,
	}

	// 检查是否被取消
	select {
	case <-ctx.Done():
		fullResult.Cancelled = true
		fullResult.CancelError = ctx.Err().Error()
		return fullResult, ctx.Err()
	default:
		// 继续
	}

	// 汇总错误
	if len(errors) > 0 {
		fullResult.Errors = errors
		if totalSuccess == 0 {
			return fullResult, fmt.Errorf("所有连接重建失败: %s", strings.Join(errors, "; "))
		}
		return fullResult, fmt.Errorf("部分连接重建失败: %s", strings.Join(errors, "; "))
	}

	ylog.Infof("pool", "全量重建完成: 协议数 %d, 连接数 %d, 成功 %d, 失败 %d, 耗时 %v",
		len(protocols), totalConnections, totalSuccess, totalFailed, duration)

	return fullResult, nil
}

// RebuildConnectionsAsync 异步重建所有需要重建的连接
func (p *EnhancedConnectionPool) RebuildConnectionsAsync(ctx context.Context) (<-chan *FullRebuildResult, error) {
	// 创建带缓冲的结果通道，容量为10以支持进度更新
	resultChan := make(chan *FullRebuildResult, 10)

	// 启动异步重建
	go func() {
		defer func() {
			// 确保通道被关闭
			close(resultChan)

			// 恢复可能的panic
			if r := recover(); r != nil {
				ylog.Errorf("pool", "RebuildConnectionsAsync panic: %v", r)
			}
		}()

		// 获取所有协议
		protocols := p.getAllProtocols()
		if len(protocols) == 0 {
			// 没有协议，立即返回空结果
			resultChan <- &FullRebuildResult{
				TotalProtocols:   0,
				TotalConnections: 0,
				Success:          0,
				Failed:           0,
				ProtocolResults:  make(map[Protocol]*BatchRebuildResult),
				StartTime:        time.Now(),
				EndTime:          time.Now(),
				Duration:         0,
			}
			return
		}

		startTime := time.Now()
		protocolResults := make(map[Protocol]*BatchRebuildResult)
		var errors []string
		totalSuccess := 0
		totalFailed := 0
		totalConnections := 0
		completedProtocols := 0

		// 使用waitgroup等待所有协议完成
		var wg sync.WaitGroup
		var mu sync.Mutex

		// 为每个协议启动异步重建
		for _, proto := range protocols {
			wg.Add(1)

			go func(proto Protocol) {
				defer wg.Done()

				// 为每个协议创建子上下文
				protoCtx, cancel := context.WithCancel(ctx)
				defer cancel()

				// 启动协议级别的异步重建
				protoChan, err := p.RebuildConnectionByProtoAsync(protoCtx, proto)
				if err != nil {
					mu.Lock()
					errors = append(errors, fmt.Sprintf("协议 %s 启动失败: %v", proto, err))
					mu.Unlock()
					return
				}

				// 收集协议结果
				var lastResult *BatchRebuildResult
				for protoResult := range protoChan {
					lastResult = protoResult

					// 更新全局进度
					mu.Lock()
					protocolResults[proto] = protoResult

					// 重新计算总数
					newTotalSuccess := 0
					newTotalFailed := 0
					newTotalConnections := 0
					for _, pr := range protocolResults {
						newTotalSuccess += pr.Success
						newTotalFailed += pr.Failed
						newTotalConnections += pr.Total
					}

					totalSuccess = newTotalSuccess
					totalFailed = newTotalFailed
					totalConnections = newTotalConnections
					mu.Unlock()

					// 发送进度更新
					progressResult := &FullRebuildResult{
						TotalProtocols:   len(protocols),
						TotalConnections: totalConnections,
						Success:          totalSuccess,
						Failed:           totalFailed,
						ProtocolResults:  copyProtocolResults(protocolResults),
						StartTime:        startTime,
						EndTime:          time.Now(),
						Duration:         time.Since(startTime),
					}

					select {
					case resultChan <- progressResult:
						// 进度更新已发送
					default:
						// 通道已满，跳过本次更新
						ylog.Warnf("pool", "全量重建进度更新通道已满，跳过更新")
					}
				}

				// 协议完成
				mu.Lock()
				completedProtocols++
				if lastResult != nil && lastResult.Errors != nil && len(lastResult.Errors) > 0 {
					errors = append(errors, fmt.Sprintf("协议 %s: %s", proto, strings.Join(lastResult.Errors, "; ")))
				}
				mu.Unlock()
			}(proto)
		}

		// 等待所有协议完成
		wg.Wait()

		// 发送最终结果
		endTime := time.Now()
		duration := endTime.Sub(startTime)

		finalResult := &FullRebuildResult{
			TotalProtocols:   len(protocols),
			TotalConnections: totalConnections,
			Success:          totalSuccess,
			Failed:           totalFailed,
			ProtocolResults:  protocolResults,
			StartTime:        startTime,
			EndTime:          endTime,
			Duration:         duration,
		}

		if len(errors) > 0 {
			finalResult.Errors = errors
		}

		// 检查是否被取消
		select {
		case <-ctx.Done():
			finalResult.Cancelled = true
			finalResult.CancelError = ctx.Err().Error()
		default:
			// 正常完成
		}

		resultChan <- finalResult
	}()

	return resultChan, nil
}

// copyProtocolResults 复制协议结果映射（用于进度更新）
func copyProtocolResults(src map[Protocol]*BatchRebuildResult) map[Protocol]*BatchRebuildResult {
	dst := make(map[Protocol]*BatchRebuildResult)
	for k, v := range src {
		// 创建浅拷贝（BatchRebuildResult本身是值类型）
		copyV := *v
		dst[k] = &copyV
	}
	return dst
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

// RebuildConnectionByID 根据连接ID重建连接
func (p *EnhancedConnectionPool) RebuildConnectionByID(connID string) error {
	_, err := p.RebuildConnectionByIDWithContext(context.Background(), connID)
	return err
}

// RebuildConnectionByIDWithContext 根据连接ID重建连接（带上下文）
func (p *EnhancedConnectionPool) RebuildConnectionByIDWithContext(ctx context.Context, connID string) (*RebuildResult, error) {
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

	// 2. 检查连接状态是否适合重建
	if !p.checkConnectionStateForRebuild(conn) {
		return nil, &RebuildError{
			Code:    RebuildErrorCodeInvalidState,
			Message: fmt.Sprintf("连接状态不适合重建: %s, state=%s", connID, conn.state),
			Details: map[string]interface{}{
				"connection_id": connID,
				"state":         conn.state,
			},
			Retryable: true, // 状态可能改变，可以重试
			SuggestedFix: "请等待连接状态变为可重建状态，或检查：\n" +
				"1. 连接是否正在使用中\n" +
				"2. 连接是否正在重建中\n" +
				"3. 连接是否已关闭",
		}
	}

	// 3. 执行重建（同步）
	startTime := time.Now()
	err := p.rebuildConnection(conn.protocol, conn)
	duration := time.Since(startTime)

	result := &RebuildResult{
		Success:   err == nil,
		OldConnID: connID,
		Duration:  duration,
		Reason:    "manual_rebuild",
		Timestamp: time.Now(),
	}

	if err != nil {
		result.Error = err.Error()

		// 包装错误
		if _, ok := err.(*RebuildError); !ok {
			err = &RebuildError{
				Code:          RebuildErrorCodeCreateFailed,
				Message:       fmt.Sprintf("重建失败: %v", err),
				Retryable:     true, // 创建失败通常可以重试
				OriginalError: err,
				SuggestedFix: "请检查：\n" +
					"1. 目标设备是否可达\n" +
					"2. 认证信息是否正确\n" +
					"3. 网络连接是否稳定",
			}
		}
		return result, err
	}

	// 获取新连接ID（如果重建成功）
	if conn != nil {
		result.NewConnID = conn.id
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
		result, err := p.RebuildConnectionByIDWithContext(ctx, connID)

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

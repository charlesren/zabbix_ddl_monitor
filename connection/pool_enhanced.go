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
	driver     ProtocolDriver
	id         string
	protocol   Protocol
	createdAt  time.Time
	lastUsed   time.Time
	usageCount int64
	valid      bool
	inUse      bool

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

// 健康状态
type HealthStatus int

const (
	HealthStatusUnknown HealthStatus = iota
	HealthStatusHealthy
	HealthStatusDegraded
	HealthStatusUnhealthy
)

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
		if !conn.inUse && conn.valid && conn.healthStatus == HealthStatusHealthy {
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
		if !conn.inUse && conn.valid && conn.healthStatus == HealthStatusHealthy {
			if conn.usageCount < minUsage {
				minUsage = conn.usageCount
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
		config:            *config,
		factories:         make(map[Protocol]ProtocolFactory),
		pools:             make(map[Protocol]*EnhancedDriverPool),
		parentCtx:         parentCtx,
		ctx:               ctx,
		cancel:            cancel,
		idleTimeout:       config.IdleTimeout,
		maxConnections:    config.MaxConnections,
		minConnections:    config.MinConnections,
		healthCheckTime:   config.HealthCheckTime,
		collector:         GetGlobalMetricsCollector(),
		resilientExecutor: NewResilientExecutor().WithRetrier(NewDefaultRetrier(30 * time.Second)), // 保留重试，移除断路器，减少超时时间
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
			interval:    120 * time.Second,
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
		p.collector.RecordOperationDuration(proto, "get_connection", time.Since(start))
	}()

	// 使用弹性执行器获取连接（保留重试，移除断路器）
	var conn *EnhancedPooledConnection
	var err error

	err = p.resilientExecutor.Execute(p.ctx, func() error {
		conn, err = p.getConnectionFromPool(pool)
		return err
	})

	if err != nil {
		p.collector.IncrementConnectionsFailed(proto)
		return nil, err
	}

	return p.activateConnection(conn), nil
}

// Get 获取连接（向后兼容版本）
func (p *EnhancedConnectionPool) Get(proto Protocol) (ProtocolDriver, error) {
	return p.GetWithContext(context.Background(), proto)
}

// getConnectionFromPool 从池中获取连接
func (p *EnhancedConnectionPool) getConnectionFromPool(pool *EnhancedDriverPool) (*EnhancedPooledConnection, error) {
	pool.mu.Lock()
	defer pool.mu.Unlock()

	// 调试：打印连接池状态
	if p.debugMode {
		ylog.Debugf("connection_pool", "getConnectionFromPool: total connections=%d, protocol=%s", len(pool.connections), pool.protocol)
		for id, conn := range pool.connections {
			ylog.Debugf("connection_pool", "  connection %s: inUse=%v, valid=%v, health=%v, usage=%d",
				id, conn.inUse, conn.valid, conn.healthStatus, conn.usageCount)
		}
	}

	// 第一优先级：健康且不需要重建的连接
	for _, conn := range pool.connections {
		if !conn.inUse && conn.valid && conn.healthStatus == HealthStatusHealthy {
			// 检查是否需要重建
			if !p.shouldRebuildConnection(conn) {
				// 实时健康检查
				if err := p.defaultHealthCheck(conn.driver); err != nil {
					ylog.Debugf("pool", "connection %s health check failed: %v", conn.id, err)
					conn.valid = false
					conn.healthStatus = HealthStatusUnhealthy
					continue // 跳过不健康的连接
				}
				ylog.Debugf("connection_pool", "重用健康连接: id=%s, usage=%d", conn.id, conn.usageCount)
				p.collector.IncrementConnectionsReused(pool.protocol)
				return conn, nil
			}
		}
	}

	// 第二优先级：需要重建的连接 → 立即替换
	for id, conn := range pool.connections {
		if !conn.inUse && conn.valid {
			// 检查是否需要重建，并且在这个过程中会原子性地标记连接为正在重建
			if p.shouldRebuildConnection(conn) {
				ylog.Debugf("connection_pool", "发现需要重建的连接: id=%s, usage=%d, age=%v",
					conn.id, conn.usageCount, time.Since(conn.createdAt))

				// 尝试创建新连接替代，最多重试3次
				var newConn *EnhancedPooledConnection
				var err error
				for attempt := 0; attempt < 3; attempt++ {
					newConn, err = p.createConnection(pool)
					if err == nil {
						break
					}
					// 重试前等待一段时间
					if attempt < 2 { // 不在最后一次尝试后等待
						waitTime := time.Duration(attempt+1) * time.Second
						ylog.Debugf("connection_pool", "重建连接第%d次尝试失败，%v后重试: %v", attempt+1, waitTime, err)
						time.Sleep(waitTime)
					}
				}
				if err != nil {
					// 创建失败，清除重建标记并降级使用旧连接
					atomic.StoreInt32(&conn.markedForRebuild, 0)
					ylog.Warnf("connection_pool", "创建新连接失败，降级使用旧连接: %v", err)
					conn.healthStatus = HealthStatusDegraded
					p.collector.IncrementConnectionsReused(pool.protocol)
					// 记录重建错误
					atomic.AddInt64(&pool.stats.RebuildErrors, 1)
					return conn, nil
				}

				// 设置重建时间
				newConn.lastRebuiltAt = time.Now()

				// 执行替换
				delete(pool.connections, id)
				pool.connections[newConn.id] = newConn

				// 确保新连接不会被标记为正在重建（createConnection已经初始化为0，这里是额外保障）
				atomic.StoreInt32(&newConn.markedForRebuild, 0)
				// 更新统计
				atomic.AddInt64(&pool.stats.CreatedConnections, 1)
				atomic.AddInt64(&pool.stats.RebuiltConnections, 1)
				// IdleConnections不变（替换，不是新增）
				p.collector.IncrementConnectionsCreated(pool.protocol)

				ylog.Infof("connection_pool", "立即替换连接: %s→%s, usage=%d",
					conn.id, newConn.id, conn.usageCount)

				// 发送重建事件
				p.sendEvent(EventConnectionRebuilt, conn.protocol, map[string]interface{}{
					"old_connection_id": conn.id,
					"new_connection_id": newConn.id,
					"old_usage_count":   atomic.LoadInt64(&conn.usageCount),
					"old_age":           time.Since(conn.createdAt).String(),
					"reason":            p.getRebuildReason(conn),
				})

				// 异步关闭旧连接，带超时保护
				go func(oldConn *EnhancedPooledConnection) {
					// 创建带超时的上下文
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()

					// 在单独的goroutine中执行关闭操作
					done := make(chan error, 1)
					go func() {
						done <- oldConn.driver.Close()
					}()

					// 等待关闭完成或超时
					select {
					case err := <-done:
						if err != nil {
							ylog.Warnf("connection_pool", "关闭旧连接失败: %v", err)
						} else {
							ylog.Debugf("connection_pool", "旧连接成功关闭: %s", oldConn.id)
						}
					case <-ctx.Done():
						ylog.Warnf("connection_pool", "关闭旧连接超时: %s", oldConn.id)
					}

					atomic.AddInt64(&pool.stats.DestroyedConnections, 1)
				}(conn)

				return newConn, nil
			}
		}
	}

	// 第三优先级：连接池未满时创建新连接
	if len(pool.connections) < p.maxConnections {
		newConn, err := p.createConnection(pool)
		if err != nil {
			return nil, err
		}

		ylog.Debugf("connection_pool", "创建新连接: id=%s, protocol=%s", newConn.id, pool.protocol)
		pool.connections[newConn.id] = newConn
		atomic.AddInt64(&pool.stats.CreatedConnections, 1)
		atomic.AddInt64(&pool.stats.IdleConnections, 1)
		p.collector.IncrementConnectionsCreated(pool.protocol)

		return newConn, nil
	}

	// 第四优先级：返回任何可用连接（降级）
	for _, conn := range pool.connections {
		if !conn.inUse && conn.valid {
			ylog.Debugf("connection_pool", "降级使用连接: id=%s, health=%v", conn.id, conn.healthStatus)
			conn.healthStatus = HealthStatusDegraded
			p.collector.IncrementConnectionsReused(pool.protocol)
			return conn, nil
		}
	}

	// 连接池已满且没有可用连接
	return nil, fmt.Errorf("connection pool exhausted for protocol %s", pool.protocol)
}

// createConnection 创建新连接
func (p *EnhancedConnectionPool) createConnection(pool *EnhancedDriverPool) (*EnhancedPooledConnection, error) {
	driver, err := pool.factory.Create(p.config)
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
		valid:            true,
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

	conn := monitored.conn

	// 正常放回连接池
	conn.inUse = false
	conn.lastUsed = time.Now()

	// 注意：usageCount已经在activateConnection中增加，这里不再重复增加
	// 只记录当前使用次数用于调试
	currentUsageCount := atomic.LoadInt64(&conn.usageCount)
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

	pool.mu.Unlock()
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

				newConn, err := p.createConnection(pool)
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
	go p.healthCheckTask()

	// 连接清理任务
	go p.cleanupTask()

	// 指标收集任务
	go p.metricsTask()

	// 事件处理任务
	go p.eventHandlerTask()

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
			if !conn.inUse && now.Sub(conn.lastHealthCheck) > p.healthCheckTime/2 {
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
	for proto, pool := range p.pools {
		pool.mu.RLock()
		connections := make([]*EnhancedPooledConnection, 0, len(pool.connections))
		for _, conn := range pool.connections {
			if !conn.inUse {
				connections = append(connections, conn)
			}
		}
		pool.mu.RUnlock()

		// 并发进行健康检查
		for _, conn := range connections {
			go p.checkConnectionHealth(proto, conn)
		}
	}
}

// checkConnectionHealth 检查单个连接健康状态
func (p *EnhancedConnectionPool) checkConnectionHealth(proto Protocol, conn *EnhancedPooledConnection) {
	start := time.Now()
	err := p.defaultHealthCheck(conn.driver)
	duration := time.Since(start)

	conn.lastHealthCheck = time.Now()

	if err != nil {
		conn.consecutiveFailures++
		if conn.consecutiveFailures >= 3 {
			conn.healthStatus = HealthStatusUnhealthy
			conn.valid = false // 关键：标记为无效
			ylog.Warnf("pool", "connection %s marked as invalid after %d failures", conn.id, conn.consecutiveFailures)
		} else {
			conn.healthStatus = HealthStatusDegraded
			ylog.Debugf("pool", "connection %s degraded (%d/%d failures)", conn.id, conn.consecutiveFailures, 3)
		}

		p.collector.IncrementHealthCheckFailed(proto)
		p.sendEvent(EventHealthCheckFailed, proto, map[string]interface{}{
			"connection_id": conn.id,
			"error":         err.Error(),
			"failures":      conn.consecutiveFailures,
		})
	} else {
		conn.consecutiveFailures = 0
		conn.healthStatus = HealthStatusHealthy
		conn.valid = true // 确保健康连接标记为有效
		p.collector.IncrementHealthCheckSuccess(proto)
	}

	p.collector.RecordHealthCheckDuration(proto, duration)
}

// defaultHealthCheck 默认健康检查
func (p *EnhancedConnectionPool) defaultHealthCheck(driver ProtocolDriver) error {
	_, err := driver.Execute(context.Background(), &ProtocolRequest{
		CommandType: CommandTypeCommands,
		Payload:     []string{"echo healthcheck"},
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
		pool.mu.Lock()

		toRemove := make([]string, 0)
		for id, conn := range pool.connections {
			if p.shouldCleanupConnection(conn, now) {
				toRemove = append(toRemove, id)
			}
		}

		// 移除需要清理的连接
		for _, id := range toRemove {
			conn := pool.connections[id]
			delete(pool.connections, id)

			go func(c *EnhancedPooledConnection) {
				c.driver.Close()
				atomic.AddInt64(&pool.stats.DestroyedConnections, 1)
				p.collector.IncrementConnectionsDestroyed(proto)
				p.sendEvent(EventConnectionDestroyed, proto, c)
			}(conn)
		}

		pool.mu.Unlock()
	}
}

// shouldCleanupConnection 判断是否应该清理连接
func (p *EnhancedConnectionPool) shouldCleanupConnection(conn *EnhancedPooledConnection, now time.Time) bool {
	if conn.inUse {
		return false
	}

	// 检查空闲时间
	if now.Sub(conn.lastUsed) > p.idleTimeout {
		return true
	}

	// 检查连接生命周期
	lifecycle := p.pools[conn.protocol].connectionLifecycle
	if now.Sub(conn.createdAt) > lifecycle.maxLifetime {
		return true
	}

	// 检查使用次数
	if conn.usageCount > lifecycle.maxUsageCount {
		return true
	}

	// 检查健康状态
	if conn.healthStatus == HealthStatusUnhealthy {
		return true
	}

	return false
}

// shouldRebuildConnection 判断连接是否需要智能重建
func (p *EnhancedConnectionPool) shouldRebuildConnection(conn *EnhancedPooledConnection) bool {
	if !p.config.SmartRebuildEnabled {
		return false
	}

	now := time.Now()

	// 避免频繁重建：检查最小重建间隔
	// 使用lastRebuiltAt（如果存在）或createdAt作为"当前版本开始时间"
	var lastRebuildOrCreate time.Time
	if !conn.lastRebuiltAt.IsZero() {
		lastRebuildOrCreate = conn.lastRebuiltAt
	} else {
		lastRebuildOrCreate = conn.createdAt
	}

	if now.Sub(lastRebuildOrCreate) < p.config.RebuildMinInterval {
		ylog.Debugf("connection_pool", "重建间隔太短: id=%s, interval=%v, min=%v",
			conn.id, now.Sub(lastRebuildOrCreate), p.config.RebuildMinInterval)
		return false
	}

	// 检查连接是否正在使用，避免重建正在使用的连接
	if conn.inUse {
		ylog.Debugf("connection_pool", "连接正在使用，跳过重建: id=%s", conn.id)
		return false
	}

	// 检查是否已经标记为正在重建，防止并发重建
	// 使用原子操作确保只有一个goroutine能将连接标记为重建
	if atomic.LoadInt32(&conn.markedForRebuild) != 0 {
		ylog.Debugf("connection_pool", "连接已标记为正在重建: id=%s", conn.id)
		return false
	}

	// 调试：打印连接详细信息
	usageCount := atomic.LoadInt64(&conn.usageCount)
	ylog.Debugf("connection_pool", "检查连接重建条件: id=%s, inUse=%v, valid=%v, health=%v, usage=%d, createdAt=%v, lastRebuiltAt=%v",
		conn.id, conn.inUse, conn.valid, conn.healthStatus, usageCount, conn.createdAt, conn.lastRebuiltAt)

	// 根据策略判断
	switch p.config.RebuildStrategy {
	case "usage":
		// 仅基于使用次数
		usageCount := atomic.LoadInt64(&conn.usageCount)
		shouldRebuild := usageCount >= p.config.RebuildMaxUsageCount
		if shouldRebuild {
			// 使用原子操作确保只有一个goroutine能将连接标记为重建
			if atomic.CompareAndSwapInt32(&conn.markedForRebuild, 0, 1) {
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
		age := now.Sub(conn.createdAt)
		shouldRebuild := age >= p.config.RebuildMaxAge
		if shouldRebuild {
			// 使用原子操作确保只有一个goroutine能将连接标记为重建
			if atomic.CompareAndSwapInt32(&conn.markedForRebuild, 0, 1) {
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

		age := now.Sub(conn.createdAt)
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

		age := now.Sub(conn.createdAt)
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
			if conn.inUse {
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
		case <-p.parentCtx.Done(): // 监听父级上下文
			return
		case <-p.ctx.Done():
			return
		case event := <-p.eventChan:
			p.handleEvent(event)
		}
	}
}

// handleEvent 处理事件
func (p *EnhancedConnectionPool) handleEvent(event PoolEvent) {
	// 这里可以添加事件处理逻辑，如日志记录、告警等
	// 目前只是简单记录
	fmt.Printf("Pool event: %+v\n", event)
}

// sendEvent 发送事件
func (p *EnhancedConnectionPool) sendEvent(eventType PoolEventType, protocol Protocol, data interface{}) {
	select {
	case p.eventChan <- PoolEvent{
		Type:      eventType,
		Protocol:  protocol,
		Timestamp: time.Now(),
		Data:      data,
	}:
	default:
		// 事件通道满了，丢弃事件
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

	age := now.Sub(conn.createdAt)
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

// Close 关闭连接池
func (p *EnhancedConnectionPool) Close() error {
	atomic.StoreInt32(&p.isShuttingDown, 1)
	p.cancel()

	p.sendEvent(EventPoolShutdown, "", nil)

	p.mu.Lock()
	defer p.mu.Unlock()

	var errors []error
	for proto, pool := range p.pools {
		pool.mu.Lock()
		for _, conn := range pool.connections {
			if err := conn.driver.Close(); err != nil {
				errors = append(errors, fmt.Errorf("protocol %s: %w", proto, err))
			}
		}
		pool.connections = make(map[string]*EnhancedPooledConnection)
		pool.mu.Unlock()
	}

	close(p.eventChan)

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
}

func (md *MonitoredDriver) Execute(ctx context.Context, req *ProtocolRequest) (*ProtocolResponse, error) {
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
func (p *EnhancedConnectionPool) activateConnection(conn *EnhancedPooledConnection) ProtocolDriver {
	conn.inUse = true
	conn.lastUsed = time.Now()
	atomic.AddInt64(&conn.usageCount, 1)

	// 更新统计信息
	pool := p.getDriverPool(conn.protocol)
	if pool != nil {
		atomic.AddInt64(&pool.stats.ActiveConnections, 1)
		atomic.AddInt64(&pool.stats.ReuseCount, 1)
	}

	// 检查连接是否有效
	if !conn.valid || conn.healthStatus == HealthStatusUnhealthy {
		// 连接无效，返回错误
		return nil
	}

	// 标记连接为正在使用
	conn.inUse = true

	// 更新统计信息
	atomic.AddInt64(&pool.stats.ActiveConnections, 1)
	atomic.AddInt64(&pool.stats.IdleConnections, -1)

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

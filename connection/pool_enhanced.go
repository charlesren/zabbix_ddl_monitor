package connection

import (
	"context"
	"fmt"
	"runtime/debug"
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
	ctx    context.Context
	cancel context.CancelFunc

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
func NewEnhancedConnectionPool(config *EnhancedConnectionConfig) *EnhancedConnectionPool {
	ctx, cancel := context.WithCancel(context.Background())

	pool := &EnhancedConnectionPool{
		config:            *config,
		factories:         make(map[Protocol]ProtocolFactory),
		pools:             make(map[Protocol]*EnhancedDriverPool),
		ctx:               ctx,
		cancel:            cancel,
		idleTimeout:       config.IdleTimeout,
		maxConnections:    config.MaxConnections,
		minConnections:    config.MinConnections,
		healthCheckTime:   config.HealthCheckTime,
		collector:         GetGlobalMetricsCollector(),
		resilientExecutor: NewDefaultResilientExecutor(),
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
			interval:    30 * time.Second,
			timeout:     10 * time.Second,
			maxFailures: 3,
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

// Get 获取连接
func (p *EnhancedConnectionPool) Get(proto Protocol) (ProtocolDriver, error) {
	if atomic.LoadInt32(&p.isShuttingDown) != 0 {
		return nil, fmt.Errorf("connection pool is shutting down")
	}

	pool := p.getDriverPool(proto)
	if pool == nil {
		return nil, fmt.Errorf("unsupported protocol: %s", proto)
	}

	start := time.Now()
	defer func() {
		p.collector.RecordOperationDuration(proto, "get_connection", time.Since(start))
	}()

	// 使用弹性执行器获取连接
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

// getConnectionFromPool 从池中获取连接
func (p *EnhancedConnectionPool) getConnectionFromPool(pool *EnhancedDriverPool) (*EnhancedPooledConnection, error) {
	pool.mu.Lock()
	defer pool.mu.Unlock()

	// 过滤健康且可用的连接
	healthyConnections := make([]*EnhancedPooledConnection, 0, len(pool.connections))
	for _, conn := range pool.connections {
		if !conn.inUse && conn.valid && conn.healthStatus == HealthStatusHealthy {
			// 实时健康检查（重要！）
			if err := p.defaultHealthCheck(conn.driver); err != nil {
				ylog.Debugf("pool", "connection %s health check failed: %v", conn.id, err)
				conn.valid = false
				conn.healthStatus = HealthStatusUnhealthy
				continue // 跳过不健康的连接
			}
			healthyConnections = append(healthyConnections, conn)
		}
	}

	// 使用负载均衡器选择健康连接
	if len(healthyConnections) > 0 {
		conn := pool.loadBalancer.SelectConnection(healthyConnections)
		if conn != nil {
			p.collector.IncrementConnectionsReused(pool.protocol)
			return conn, nil
		}
	}

	// 检查是否可以创建新连接
	if len(pool.connections) >= p.maxConnections {
		return nil, fmt.Errorf("connection pool exhausted for protocol %s", pool.protocol)
	}

	// 创建新连接
	newConn, err := p.createConnection(pool)
	if err != nil {
		return nil, err
	}

	pool.connections[newConn.id] = newConn
	atomic.AddInt64(&pool.stats.CreatedConnections, 1)
	p.collector.IncrementConnectionsCreated(pool.protocol)

	return newConn, nil
}

// createConnection 创建新连接
func (p *EnhancedConnectionPool) createConnection(pool *EnhancedDriverPool) (*EnhancedPooledConnection, error) {
	driver, err := pool.factory.Create(p.config)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection: %w", err)
	}

	conn := &EnhancedPooledConnection{
		driver:       driver,
		id:           fmt.Sprintf("%s|%s|%d", p.config.Host, pool.protocol, time.Now().UnixNano()),
		protocol:     pool.protocol,
		createdAt:    time.Now(),
		lastUsed:     time.Now(),
		valid:        true,
		healthStatus: HealthStatusUnknown,
		labels:       make(map[string]string),
		metadata:     make(map[string]interface{}),
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
	defer pool.mu.Unlock()

	conn := monitored.conn
	conn.inUse = false
	conn.lastUsed = time.Now()

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

	// 并发创建连接
	for i := 0; i < targetCount; i++ {
		go func() {
			_, err := p.createConnection(pool)
			if err != nil {
				atomic.AddInt64(&warmup.Failed, 1)
			} else {
				atomic.AddInt64(&warmup.Success, 1)
			}
			errChan <- err
		}()
	}

	// 等待所有连接创建完成
	var errors []error
	for i := 0; i < targetCount; i++ {
		if err := <-errChan; err != nil {
			errors = append(errors, err)
		}
	}

	p.mu.Lock()
	warmup.EndTime = time.Now()
	if len(errors) == 0 {
		warmup.Status = WarmupStateCompleted
	} else {
		warmup.Status = WarmupStateFailed
	}
	p.mu.Unlock()

	p.sendEvent(EventPoolWarmupCompleted, proto, warmup)

	if len(errors) > 0 {
		return fmt.Errorf("warmup failed: %d errors out of %d connections", len(errors), targetCount)
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
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
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

// metricsTask 指标收集任务
func (p *EnhancedConnectionPool) metricsTask() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
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

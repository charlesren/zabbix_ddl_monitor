package connection

import (
	"sync"
	"sync/atomic"
	"time"
)

// 指标收集器接口
type MetricsCollector interface {
	// 连接指标
	IncrementConnectionsCreated(protocol Protocol)
	IncrementConnectionsDestroyed(protocol Protocol)
	IncrementConnectionsReused(protocol Protocol)
	IncrementConnectionsFailed(protocol Protocol)

	// 操作指标
	RecordOperationDuration(protocol Protocol, operation string, duration time.Duration)
	IncrementOperationCount(protocol Protocol, operation string)
	IncrementOperationErrors(protocol Protocol, operation string)

	// 连接池指标
	SetActiveConnections(protocol Protocol, count int64)
	SetIdleConnections(protocol Protocol, count int64)
	SetPoolSize(protocol Protocol, count int64)

	// 健康检查指标
	IncrementHealthCheckSuccess(protocol Protocol)
	IncrementHealthCheckFailed(protocol Protocol)
	RecordHealthCheckDuration(protocol Protocol, duration time.Duration)

	// 重建指标
	IncrementConnectionsNeedingRebuild(protocol Protocol)
	IncrementRebuildMarked(protocol Protocol, reason string)
	IncrementRebuildStarted(protocol Protocol)
	IncrementRebuildCompleted(protocol Protocol)
	IncrementRebuildFailed(protocol Protocol)
	RecordRebuildDuration(protocol Protocol, duration time.Duration)
	SetRebuildingConnections(protocol Protocol, count int64)
	IncrementRebuildingConnections(protocol Protocol)
	DecrementRebuildingConnections(protocol Protocol)

	// 获取指标快照
	GetMetrics() *MetricsSnapshot
	Reset()
}

// 指标快照
type MetricsSnapshot struct {
	Timestamp time.Time `json:"timestamp"`

	// 连接指标
	ConnectionMetrics map[Protocol]*ConnectionMetrics `json:"connection_metrics"`

	// 操作指标
	OperationMetrics map[Protocol]map[string]*OperationMetrics `json:"operation_metrics"`

	// 系统指标
	SystemMetrics *SystemMetrics `json:"system_metrics"`
}

// 连接指标
type ConnectionMetrics struct {
	Protocol Protocol `json:"protocol"`

	// 计数器
	Created   int64 `json:"created"`
	Destroyed int64 `json:"destroyed"`
	Reused    int64 `json:"reused"`
	Failed    int64 `json:"failed"`

	// 当前状态
	Active int64 `json:"active"`
	Idle   int64 `json:"idle"`
	Total  int64 `json:"total"`

	// 健康检查
	HealthCheckSuccess int64         `json:"health_check_success"`
	HealthCheckFailed  int64         `json:"health_check_failed"`
	HealthCheckLatency time.Duration `json:"health_check_latency"`

	// 重建指标
	ConnectionsNeedingRebuild int64            `json:"connections_needing_rebuild"`
	RebuildMarkedTotal        int64            `json:"rebuild_marked_total"`
	RebuildMarkedByReason     map[string]int64 `json:"rebuild_marked_by_reason"`
	RebuildStarted            int64            `json:"rebuild_started"`
	RebuildCompleted          int64            `json:"rebuild_completed"`
	RebuildFailed             int64            `json:"rebuild_failed"`
	RebuildDuration           time.Duration    `json:"rebuild_duration"`
	RebuildingConnections     int64            `json:"rebuilding_connections"`

	// 统计信息
	CreationRate    float64       `json:"creation_rate"`    // 每秒创建连接数
	DestructionRate float64       `json:"destruction_rate"` // 每秒销毁连接数
	ReuseRate       float64       `json:"reuse_rate"`       // 连接复用率
	FailureRate     float64       `json:"failure_rate"`     // 连接失败率
	AverageLifetime time.Duration `json:"average_lifetime"` // 平均连接存活时间
}

// 操作指标
type OperationMetrics struct {
	Operation string `json:"operation"`

	// 计数器
	Count  int64 `json:"count"`
	Errors int64 `json:"errors"`

	// 延迟统计
	TotalDuration time.Duration `json:"total_duration"`
	MinDuration   time.Duration `json:"min_duration"`
	MaxDuration   time.Duration `json:"max_duration"`
	AvgDuration   time.Duration `json:"avg_duration"`

	// 成功率
	SuccessRate float64 `json:"success_rate"`
	ErrorRate   float64 `json:"error_rate"`

	// 吞吐量
	Throughput float64 `json:"throughput"` // 每秒操作数
}

// 系统指标
type SystemMetrics struct {
	// 内存使用
	MemoryUsage int64 `json:"memory_usage"`

	// Goroutine数量
	GoroutineCount int `json:"goroutine_count"`

	// GC统计
	GCPauseTotal time.Duration `json:"gc_pause_total"`
	GCCount      uint32        `json:"gc_count"`

	// 系统负载
	CPUUsage float64 `json:"cpu_usage"`

	// 启动时间
	Uptime time.Duration `json:"uptime"`
}

// 默认指标收集器实现
type DefaultMetricsCollector struct {
	mu sync.RWMutex

	// 连接指标
	connectionMetrics map[Protocol]*atomicConnectionMetrics

	// 操作指标
	operationMetrics map[Protocol]map[string]*atomicOperationMetrics

	// 重建原因统计（需要锁保护）
	rebuildMarkedByReason map[Protocol]map[string]int64

	// 开始时间
	startTime time.Time

	// 最后更新时间
	lastUpdate time.Time
}

// 原子连接指标
type atomicConnectionMetrics struct {
	created   int64
	destroyed int64
	reused    int64
	failed    int64
	active    int64
	idle      int64
	total     int64

	healthCheckSuccess int64
	healthCheckFailed  int64
	healthCheckLatency int64 // nanoseconds

	// 重建指标
	connectionsNeedingRebuild int64
	rebuildMarkedTotal        int64
	rebuildStarted            int64
	rebuildCompleted          int64
	rebuildFailed             int64
	rebuildDuration           int64 // nanoseconds
	rebuildingConnections     int64

	// 用于计算平均值的累计值
	totalLifetime int64 // nanoseconds
	lifetimeCount int64
}

// 原子操作指标
type atomicOperationMetrics struct {
	count         int64
	errors        int64
	totalDuration int64 // nanoseconds
	minDuration   int64 // nanoseconds
	maxDuration   int64 // nanoseconds
}

// NewDefaultMetricsCollector 创建默认指标收集器
func NewDefaultMetricsCollector() *DefaultMetricsCollector {
	return &DefaultMetricsCollector{
		connectionMetrics:     make(map[Protocol]*atomicConnectionMetrics),
		operationMetrics:      make(map[Protocol]map[string]*atomicOperationMetrics),
		rebuildMarkedByReason: make(map[Protocol]map[string]int64),
		startTime:             time.Now(),
		lastUpdate:            time.Now(),
	}
}

// IncrementConnectionsCreated 增加创建连接数
func (c *DefaultMetricsCollector) IncrementConnectionsCreated(protocol Protocol) {
	metrics := c.getConnectionMetrics(protocol)
	atomic.AddInt64(&metrics.created, 1)
	atomic.AddInt64(&metrics.total, 1)
}

// IncrementConnectionsDestroyed 增加销毁连接数
func (c *DefaultMetricsCollector) IncrementConnectionsDestroyed(protocol Protocol) {
	metrics := c.getConnectionMetrics(protocol)
	atomic.AddInt64(&metrics.destroyed, 1)
	atomic.AddInt64(&metrics.total, -1)
}

// IncrementConnectionsReused 增加复用连接数
func (c *DefaultMetricsCollector) IncrementConnectionsReused(protocol Protocol) {
	metrics := c.getConnectionMetrics(protocol)
	atomic.AddInt64(&metrics.reused, 1)
}

// IncrementConnectionsFailed 增加失败连接数
func (c *DefaultMetricsCollector) IncrementConnectionsFailed(protocol Protocol) {
	metrics := c.getConnectionMetrics(protocol)
	atomic.AddInt64(&metrics.failed, 1)
}

// RecordOperationDuration 记录操作耗时
func (c *DefaultMetricsCollector) RecordOperationDuration(protocol Protocol, operation string, duration time.Duration) {
	metrics := c.getOperationMetrics(protocol, operation)
	nanos := duration.Nanoseconds()

	atomic.AddInt64(&metrics.totalDuration, nanos)

	// 更新最小值
	for {
		current := atomic.LoadInt64(&metrics.minDuration)
		if current == 0 || nanos < current {
			if atomic.CompareAndSwapInt64(&metrics.minDuration, current, nanos) {
				break
			}
		} else {
			break
		}
	}

	// 更新最大值
	for {
		current := atomic.LoadInt64(&metrics.maxDuration)
		if nanos > current {
			if atomic.CompareAndSwapInt64(&metrics.maxDuration, current, nanos) {
				break
			}
		} else {
			break
		}
	}
}

// IncrementOperationCount 增加操作计数
func (c *DefaultMetricsCollector) IncrementOperationCount(protocol Protocol, operation string) {
	metrics := c.getOperationMetrics(protocol, operation)
	atomic.AddInt64(&metrics.count, 1)
}

// IncrementOperationErrors 增加操作错误数
func (c *DefaultMetricsCollector) IncrementOperationErrors(protocol Protocol, operation string) {
	metrics := c.getOperationMetrics(protocol, operation)
	atomic.AddInt64(&metrics.errors, 1)
}

// SetActiveConnections 设置活跃连接数
func (c *DefaultMetricsCollector) SetActiveConnections(protocol Protocol, count int64) {
	metrics := c.getConnectionMetrics(protocol)
	atomic.StoreInt64(&metrics.active, count)
}

// SetIdleConnections 设置空闲连接数
func (c *DefaultMetricsCollector) SetIdleConnections(protocol Protocol, count int64) {
	metrics := c.getConnectionMetrics(protocol)
	atomic.StoreInt64(&metrics.idle, count)
}

// SetPoolSize 设置连接池大小
func (c *DefaultMetricsCollector) SetPoolSize(protocol Protocol, count int64) {
	metrics := c.getConnectionMetrics(protocol)
	atomic.StoreInt64(&metrics.total, count)
}

// IncrementHealthCheckSuccess 增加健康检查成功数
func (c *DefaultMetricsCollector) IncrementHealthCheckSuccess(protocol Protocol) {
	metrics := c.getConnectionMetrics(protocol)
	atomic.AddInt64(&metrics.healthCheckSuccess, 1)
}

// IncrementHealthCheckFailed 增加健康检查失败数
func (c *DefaultMetricsCollector) IncrementHealthCheckFailed(protocol Protocol) {
	metrics := c.getConnectionMetrics(protocol)
	atomic.AddInt64(&metrics.healthCheckFailed, 1)
}

// RecordHealthCheckDuration 记录健康检查耗时
func (c *DefaultMetricsCollector) RecordHealthCheckDuration(protocol Protocol, duration time.Duration) {
	metrics := c.getConnectionMetrics(protocol)
	atomic.StoreInt64(&metrics.healthCheckLatency, duration.Nanoseconds())
}

// GetMetrics 获取指标快照
func (c *DefaultMetricsCollector) GetMetrics() *MetricsSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()

	now := time.Now()
	uptime := now.Sub(c.startTime)
	timeDelta := now.Sub(c.lastUpdate).Seconds()

	snapshot := &MetricsSnapshot{
		Timestamp:         now,
		ConnectionMetrics: make(map[Protocol]*ConnectionMetrics),
		OperationMetrics:  make(map[Protocol]map[string]*OperationMetrics),
		SystemMetrics: &SystemMetrics{
			Uptime: uptime,
		},
	}

	// 收集连接指标
	for protocol, atomicMetrics := range c.connectionMetrics {
		created := atomic.LoadInt64(&atomicMetrics.created)
		destroyed := atomic.LoadInt64(&atomicMetrics.destroyed)
		reused := atomic.LoadInt64(&atomicMetrics.reused)
		failed := atomic.LoadInt64(&atomicMetrics.failed)
		active := atomic.LoadInt64(&atomicMetrics.active)
		idle := atomic.LoadInt64(&atomicMetrics.idle)
		total := atomic.LoadInt64(&atomicMetrics.total)
		healthSuccess := atomic.LoadInt64(&atomicMetrics.healthCheckSuccess)
		healthFailed := atomic.LoadInt64(&atomicMetrics.healthCheckFailed)
		healthLatency := atomic.LoadInt64(&atomicMetrics.healthCheckLatency)
		totalLifetime := atomic.LoadInt64(&atomicMetrics.totalLifetime)
		lifetimeCount := atomic.LoadInt64(&atomicMetrics.lifetimeCount)
		connectionsNeedingRebuild := atomic.LoadInt64(&atomicMetrics.connectionsNeedingRebuild)
		rebuildMarkedTotal := atomic.LoadInt64(&atomicMetrics.rebuildMarkedTotal)
		rebuildStarted := atomic.LoadInt64(&atomicMetrics.rebuildStarted)
		rebuildCompleted := atomic.LoadInt64(&atomicMetrics.rebuildCompleted)
		rebuildFailed := atomic.LoadInt64(&atomicMetrics.rebuildFailed)
		rebuildDuration := atomic.LoadInt64(&atomicMetrics.rebuildDuration)
		rebuildingConnections := atomic.LoadInt64(&atomicMetrics.rebuildingConnections)

		metrics := &ConnectionMetrics{
			Protocol:                  protocol,
			Created:                   created,
			Destroyed:                 destroyed,
			Reused:                    reused,
			Failed:                    failed,
			Active:                    active,
			Idle:                      idle,
			Total:                     total,
			HealthCheckSuccess:        healthSuccess,
			HealthCheckFailed:         healthFailed,
			HealthCheckLatency:        time.Duration(healthLatency),
			ConnectionsNeedingRebuild: connectionsNeedingRebuild,
			RebuildMarkedTotal:        rebuildMarkedTotal,
			RebuildStarted:            rebuildStarted,
			RebuildCompleted:          rebuildCompleted,
			RebuildFailed:             rebuildFailed,
			RebuildDuration:           time.Duration(rebuildDuration),
			RebuildingConnections:     rebuildingConnections,
		}

		// 计算速率
		if timeDelta > 0 {
			metrics.CreationRate = float64(created) / uptime.Seconds()
			metrics.DestructionRate = float64(destroyed) / uptime.Seconds()
		}

		// 计算复用率
		totalConnUsage := created + reused
		if totalConnUsage > 0 {
			metrics.ReuseRate = float64(reused) / float64(totalConnUsage)
		}

		// 计算失败率
		totalConnAttempts := created + failed
		if totalConnAttempts > 0 {
			metrics.FailureRate = float64(failed) / float64(totalConnAttempts)
		}

		// 计算平均连接存活时间
		if lifetimeCount > 0 {
			metrics.AverageLifetime = time.Duration(totalLifetime / lifetimeCount)
		}

		// 复制重建原因统计
		c.mu.RLock()
		if reasons, exists := c.rebuildMarkedByReason[protocol]; exists {
			metrics.RebuildMarkedByReason = make(map[string]int64, len(reasons))
			for reason, count := range reasons {
				metrics.RebuildMarkedByReason[reason] = count
			}
		}
		c.mu.RUnlock()

		snapshot.ConnectionMetrics[protocol] = metrics
	}

	// 收集操作指标
	for protocol, operations := range c.operationMetrics {
		snapshot.OperationMetrics[protocol] = make(map[string]*OperationMetrics)

		for operation, atomicMetrics := range operations {
			count := atomic.LoadInt64(&atomicMetrics.count)
			errors := atomic.LoadInt64(&atomicMetrics.errors)
			totalDuration := atomic.LoadInt64(&atomicMetrics.totalDuration)
			minDuration := atomic.LoadInt64(&atomicMetrics.minDuration)
			maxDuration := atomic.LoadInt64(&atomicMetrics.maxDuration)

			metrics := &OperationMetrics{
				Operation:     operation,
				Count:         count,
				Errors:        errors,
				TotalDuration: time.Duration(totalDuration),
				MinDuration:   time.Duration(minDuration),
				MaxDuration:   time.Duration(maxDuration),
			}

			// 计算平均耗时
			if count > 0 {
				metrics.AvgDuration = time.Duration(totalDuration / count)
			}

			// 计算成功率
			if count > 0 {
				metrics.SuccessRate = float64(count-errors) / float64(count)
				metrics.ErrorRate = float64(errors) / float64(count)
			}

			// 计算吞吐量
			if timeDelta > 0 {
				metrics.Throughput = float64(count) / uptime.Seconds()
			}

			snapshot.OperationMetrics[protocol][operation] = metrics
		}
	}

	c.lastUpdate = now
	return snapshot
}

// Reset 重置指标
func (c *DefaultMetricsCollector) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.connectionMetrics = make(map[Protocol]*atomicConnectionMetrics)
	c.operationMetrics = make(map[Protocol]map[string]*atomicOperationMetrics)
	c.rebuildMarkedByReason = make(map[Protocol]map[string]int64)
	c.startTime = time.Now()
	c.lastUpdate = time.Now()
}

// getConnectionMetrics 获取或创建连接指标
func (c *DefaultMetricsCollector) getConnectionMetrics(protocol Protocol) *atomicConnectionMetrics {
	c.mu.Lock()
	defer c.mu.Unlock()

	if metrics, exists := c.connectionMetrics[protocol]; exists {
		return metrics
	}

	metrics := &atomicConnectionMetrics{}
	c.connectionMetrics[protocol] = metrics
	return metrics
}

// getOperationMetrics 获取或创建操作指标
func (c *DefaultMetricsCollector) getOperationMetrics(protocol Protocol, operation string) *atomicOperationMetrics {
	c.mu.Lock()
	defer c.mu.Unlock()

	if operations, exists := c.operationMetrics[protocol]; exists {
		if metrics, exists := operations[operation]; exists {
			return metrics
		}
	} else {
		c.operationMetrics[protocol] = make(map[string]*atomicOperationMetrics)
	}

	metrics := &atomicOperationMetrics{}
	c.operationMetrics[protocol][operation] = metrics
	return metrics
}

// RecordConnectionLifetime 记录连接存活时间
func (c *DefaultMetricsCollector) RecordConnectionLifetime(protocol Protocol, lifetime time.Duration) {
	metrics := c.getConnectionMetrics(protocol)
	atomic.AddInt64(&metrics.totalLifetime, lifetime.Nanoseconds())
	atomic.AddInt64(&metrics.lifetimeCount, 1)
}

// 指标导出器接口
type MetricsExporter interface {
	Export(snapshot *MetricsSnapshot) error
}

// Prometheus导出器
type PrometheusExporter struct {
	namespace string
	subsystem string
}

// NewPrometheusExporter 创建Prometheus导出器
func NewPrometheusExporter(namespace, subsystem string) *PrometheusExporter {
	return &PrometheusExporter{
		namespace: namespace,
		subsystem: subsystem,
	}
}

// Export 导出到Prometheus
func (e *PrometheusExporter) Export(snapshot *MetricsSnapshot) error {
	// TODO: 实现Prometheus指标导出
	// 这里需要集成prometheus客户端库
	return nil
}

// JSON导出器
type JSONExporter struct {
	filePath string
}

// NewJSONExporter 创建JSON导出器
func NewJSONExporter(filePath string) *JSONExporter {
	return &JSONExporter{
		filePath: filePath,
	}
}

// Export 导出到JSON文件
func (e *JSONExporter) Export(snapshot *MetricsSnapshot) error {
	// TODO: 实现JSON文件导出
	return nil
}

// 指标中间件
type MetricsMiddleware struct {
	collector MetricsCollector
	protocol  Protocol
}

// NewMetricsMiddleware 创建指标中间件
func NewMetricsMiddleware(collector MetricsCollector, protocol Protocol) *MetricsMiddleware {
	return &MetricsMiddleware{
		collector: collector,
		protocol:  protocol,
	}
}

// WrapOperation 包装操作以收集指标
func (m *MetricsMiddleware) WrapOperation(operation string, fn func() error) error {
	start := time.Now()

	m.collector.IncrementOperationCount(m.protocol, operation)

	err := fn()

	duration := time.Since(start)
	m.collector.RecordOperationDuration(m.protocol, operation, duration)

	if err != nil {
		m.collector.IncrementOperationErrors(m.protocol, operation)
	}

	return err
}

// 全局指标收集器实例
var (
	globalMetricsCollector MetricsCollector = NewDefaultMetricsCollector()
	metricsOnce            sync.Once
)

// GetGlobalMetricsCollector 获取全局指标收集器
func GetGlobalMetricsCollector() MetricsCollector {
	return globalMetricsCollector
}

// SetGlobalMetricsCollector 设置全局指标收集器
func SetGlobalMetricsCollector(collector MetricsCollector) {
	metricsOnce.Do(func() {
		globalMetricsCollector = collector
	})
}

// ==================== 重建指标方法 ====================

// IncrementConnectionsNeedingRebuild 增加需要重建的连接数
func (c *DefaultMetricsCollector) IncrementConnectionsNeedingRebuild(protocol Protocol) {
	metrics := c.getConnectionMetrics(protocol)
	atomic.AddInt64(&metrics.connectionsNeedingRebuild, 1)
}

// IncrementRebuildMarked 增加已标记的重建连接数
func (c *DefaultMetricsCollector) IncrementRebuildMarked(protocol Protocol, reason string) {
	metrics := c.getConnectionMetrics(protocol)
	atomic.AddInt64(&metrics.rebuildMarkedTotal, 1)

	// 记录原因统计（需要锁保护）
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.rebuildMarkedByReason[protocol]; !exists {
		c.rebuildMarkedByReason[protocol] = make(map[string]int64)
	}
	c.rebuildMarkedByReason[protocol][reason]++
}

// IncrementRebuildStarted 增加已开始的重建数
func (c *DefaultMetricsCollector) IncrementRebuildStarted(protocol Protocol) {
	metrics := c.getConnectionMetrics(protocol)
	atomic.AddInt64(&metrics.rebuildStarted, 1)
	atomic.AddInt64(&metrics.rebuildingConnections, 1)
}

// IncrementRebuildCompleted 增加已完成的重建数
func (c *DefaultMetricsCollector) IncrementRebuildCompleted(protocol Protocol) {
	metrics := c.getConnectionMetrics(protocol)
	atomic.AddInt64(&metrics.rebuildCompleted, 1)
	atomic.AddInt64(&metrics.rebuildingConnections, -1)
}

// IncrementRebuildFailed 增加失败的重建数
func (c *DefaultMetricsCollector) IncrementRebuildFailed(protocol Protocol) {
	metrics := c.getConnectionMetrics(protocol)
	atomic.AddInt64(&metrics.rebuildFailed, 1)
	atomic.AddInt64(&metrics.rebuildingConnections, -1)
}

// RecordRebuildDuration 记录重建耗时
func (c *DefaultMetricsCollector) RecordRebuildDuration(protocol Protocol, duration time.Duration) {
	metrics := c.getConnectionMetrics(protocol)
	atomic.StoreInt64(&metrics.rebuildDuration, duration.Nanoseconds())
}

// SetRebuildingConnections 设置正在重建的连接数
func (c *DefaultMetricsCollector) SetRebuildingConnections(protocol Protocol, count int64) {
	metrics := c.getConnectionMetrics(protocol)
	atomic.StoreInt64(&metrics.rebuildingConnections, count)
}

// IncrementRebuildingConnections 增加正在重建的连接数
func (c *DefaultMetricsCollector) IncrementRebuildingConnections(protocol Protocol) {
	metrics := c.getConnectionMetrics(protocol)
	atomic.AddInt64(&metrics.rebuildingConnections, 1)
}

// DecrementRebuildingConnections 减少正在重建的连接数
func (c *DefaultMetricsCollector) DecrementRebuildingConnections(protocol Protocol) {
	metrics := c.getConnectionMetrics(protocol)
	atomic.AddInt64(&metrics.rebuildingConnections, -1)
}

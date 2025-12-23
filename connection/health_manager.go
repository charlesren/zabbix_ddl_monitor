package connection

import (
	"context"
	"strings"
	"time"

	"github.com/charlesren/ylog"
)

// HealthStatus 健康状态枚举
type HealthStatus int

const (
	HealthStatusUnknown HealthStatus = iota
	HealthStatusHealthy
	HealthStatusDegraded
	HealthStatusUnhealthy
)

// String 返回健康状态的字符串表示
func (hs HealthStatus) String() string {
	switch hs {
	case HealthStatusHealthy:
		return "healthy"
	case HealthStatusDegraded:
		return "degraded"
	case HealthStatusUnhealthy:
		return "unhealthy"
	default:
		return "unknown"
	}
}

// HealthCheckErrorType 健康检查错误类型
type HealthCheckErrorType int

const (
	HealthErrorUnknown HealthCheckErrorType = iota
	HealthErrorTimeout
	HealthErrorNetwork
	HealthErrorAuth
	HealthErrorCommand
	HealthErrorProtocol
)

// String 返回错误类型的字符串表示
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

// HealthManager 健康管理器
type HealthManager struct {
	// 配置参数
	healthCheckTime     time.Duration
	healthCheckTimeout  time.Duration
	maxConcurrentChecks int

	// 指标收集器
	collector MetricsCollector

	// 事件通道
	eventChan chan<- PoolEvent
}

// NewHealthManager 创建新的健康管理器
func NewHealthManager(healthCheckTime, healthCheckTimeout time.Duration, collector MetricsCollector, eventChan chan<- PoolEvent) *HealthManager {
	return &HealthManager{
		healthCheckTime:     healthCheckTime,
		healthCheckTimeout:  healthCheckTimeout,
		maxConcurrentChecks: 10, // 默认最多10个并发检查
		collector:           collector,
		eventChan:           eventChan,
	}
}

// WithMaxConcurrentChecks 设置最大并发检查数
func (hm *HealthManager) WithMaxConcurrentChecks(max int) *HealthManager {
	if max > 0 {
		hm.maxConcurrentChecks = max
	}
	return hm
}

// PerformHealthChecks 执行健康检查
func (hm *HealthManager) PerformHealthChecks(pools map[Protocol]*EnhancedDriverPool) {
	if len(pools) == 0 {
		return
	}

	// 限制并发检查数量
	sem := make(chan struct{}, hm.maxConcurrentChecks)

	for proto, pool := range pools {
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

		ylog.Debugf("health_manager", "准备健康检查 %d 个连接: protocol=%s", len(connections), proto)

		// 并发进行健康检查，但有限制
		for _, conn := range connections {
			sem <- struct{}{}
			go func(proto Protocol, conn *EnhancedPooledConnection) {
				defer func() { <-sem }()
				hm.CheckConnectionHealth(proto, conn)
			}(proto, conn)
		}
	}
}

// CheckConnectionHealth 检查单个连接健康状态
func (hm *HealthManager) CheckConnectionHealth(proto Protocol, conn *EnhancedPooledConnection) {
	// 检查连接是否正在使用，跳过正在使用的连接
	if conn.isInUse() {
		return
	}

	// 开始健康检查（设置状态为检查中）
	if !conn.beginHealthCheck() {
		state, _ := conn.getStatus()
		ylog.Debugf("health_manager", "无法开始健康检查: connection %s state=%s", conn.id, state)
		return
	}

	start := time.Now()
	err := hm.DefaultHealthCheck(conn.driver)
	duration := time.Since(start)

	success := err == nil
	conn.recordHealthCheck(success, err)

	// 记录指标和事件
	if !success {
		errorType := hm.ClassifyHealthCheckError(err)

		// 根据错误类型记录不同级别的日志
		switch errorType {
		case HealthErrorTimeout:
			ylog.Warnf("health_manager", "connection %s health check timeout: %v", conn.id, err)
		case HealthErrorNetwork:
			ylog.Warnf("health_manager", "connection %s network error: %v", conn.id, err)
		case HealthErrorAuth:
			ylog.Errorf("health_manager", "connection %s authentication error: %v", conn.id, err)
		case HealthErrorCommand:
			ylog.Warnf("health_manager", "connection %s command error: %v", conn.id, err)
		case HealthErrorProtocol:
			ylog.Errorf("health_manager", "connection %s protocol error: %v", conn.id, err)
		default:
			ylog.Warnf("health_manager", "connection %s health check failed: %v", conn.id, err)
		}

		hm.collector.IncrementHealthCheckFailed(proto)
		state, _ := conn.getStatus()
		hm.sendEvent(EventHealthCheckFailed, proto, map[string]interface{}{
			"connection_id": conn.id,
			"error":         err.Error(),
			"error_type":    errorType.String(),
			"state":         state.String(),
		})
	} else {
		hm.collector.IncrementHealthCheckSuccess(proto)
	}

	hm.collector.RecordHealthCheckDuration(proto, duration)
}

// DefaultHealthCheck 默认健康检查
func (hm *HealthManager) DefaultHealthCheck(driver ProtocolDriver) error {
	// 使用配置的超时时间，默认5秒
	timeout := hm.healthCheckTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// 尝试执行简单的命令来检查连接是否健康
	// 优先使用GetPrompt方法，如果不可用则使用Execute方法
	if driverWithPrompt, ok := driver.(interface {
		GetPrompt(ctx context.Context) (string, error)
	}); ok {
		_, err := driverWithPrompt.GetPrompt(ctx)
		return err
	}

	// 回退到Execute方法
	_, err := driver.Execute(ctx, &ProtocolRequest{
		CommandType: CommandTypeCommands,
		Payload:     []string{"show clock"},
	})
	return err
}

// ClassifyHealthCheckError 分类健康检查错误
func (hm *HealthManager) ClassifyHealthCheckError(err error) HealthCheckErrorType {
	return ClassifyHealthCheckError(err)
}

// ClassifyHealthCheckError 包级别的健康检查错误分类函数
func ClassifyHealthCheckError(err error) HealthCheckErrorType {
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

// sendEvent 发送事件（内部方法）
func (hm *HealthManager) sendEvent(eventType PoolEventType, protocol Protocol, data map[string]interface{}) {
	if hm.eventChan == nil {
		return
	}

	select {
	case hm.eventChan <- PoolEvent{
		Type:      eventType,
		Protocol:  protocol,
		Timestamp: time.Now(),
		Data:      data,
	}:
	default:
		// 事件通道已满，丢弃事件
		ylog.Debugf("health_manager", "event channel full, dropping event: %d", eventType)
	}
}

// GetHealthCheckTime 获取健康检查间隔时间
func (hm *HealthManager) GetHealthCheckTime() time.Duration {
	return hm.healthCheckTime
}

// GetHealthCheckTimeout 获取健康检查超时时间
func (hm *HealthManager) GetHealthCheckTimeout() time.Duration {
	return hm.healthCheckTimeout
}

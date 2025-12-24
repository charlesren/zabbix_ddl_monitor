package connection

import (
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

// GetHealthCheckTime 获取健康检查间隔时间
func (hm *HealthManager) GetHealthCheckTime() time.Duration {
	return hm.healthCheckTime
}

// GetHealthCheckTimeout 获取健康检查超时时间
func (hm *HealthManager) GetHealthCheckTimeout() time.Duration {
	return hm.healthCheckTimeout
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

	// 使用recover避免在通道关闭时panic
	defer func() {
		if r := recover(); r != nil {
			ylog.Debugf("health_manager", "发送事件时发生panic（可能通道已关闭）: %v, event_type=%d", r, eventType)
		}
	}()

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

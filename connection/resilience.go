package connection

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

var (
	ErrCircuitBreakerOpen    = errors.New("circuit breaker is open")
	ErrMaxRetriesExceeded    = errors.New("maximum retries exceeded")
	ErrRetryContextCancelled = errors.New("retry context cancelled")
	ErrRetryTimeout          = errors.New("retry timeout exceeded")
)

// 重试策略接口
type RetryPolicy interface {
	// ShouldRetry 判断是否应该重试
	ShouldRetry(attempt int, err error) bool
	// NextDelay 计算下次重试的延迟
	NextDelay(attempt int) time.Duration
	// GetMaxAttempts 返回最大重试次数
	GetMaxAttempts() int
}

// 指数退避重试策略
type ExponentialBackoffPolicy struct {
	BaseDelay       time.Duration
	MaxDelay        time.Duration
	BackoffRate     float64
	MaxAttempts     int
	Jitter          bool
	RetryableErrors []error
}

func (p *ExponentialBackoffPolicy) ShouldRetry(attempt int, err error) bool {
	if attempt >= p.MaxAttempts {
		return false
	}

	// 检查错误是否可重试
	if len(p.RetryableErrors) > 0 {
		for _, retryableErr := range p.RetryableErrors {
			if errors.Is(err, retryableErr) {
				return true
			}
		}
		return false
	}

	// 默认认为所有错误都可重试
	return true
}

func (p *ExponentialBackoffPolicy) NextDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}

	delay := p.BaseDelay
	for i := 1; i < attempt; i++ {
		delay = time.Duration(float64(delay) * p.BackoffRate)
		if delay > p.MaxDelay {
			delay = p.MaxDelay
			break
		}
	}

	// 添加抖动避免惊群效应
	if p.Jitter {
		jitter := time.Duration(float64(delay) * 0.1 * (0.5 - rand.Float64())) // ±10%
		delay += jitter
	}

	return delay
}

func (p *ExponentialBackoffPolicy) GetMaxAttempts() int {
	return p.MaxAttempts
}

// 固定间隔重试策略
type FixedIntervalPolicy struct {
	Interval        time.Duration
	MaxAttempts     int
	RetryableErrors []error
}

func (p *FixedIntervalPolicy) ShouldRetry(attempt int, err error) bool {
	if attempt >= p.MaxAttempts {
		return false
	}

	if len(p.RetryableErrors) > 0 {
		for _, retryableErr := range p.RetryableErrors {
			if errors.Is(err, retryableErr) {
				return true
			}
		}
		return false
	}

	return true
}

func (p *FixedIntervalPolicy) NextDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	return p.Interval
}

func (p *FixedIntervalPolicy) GetMaxAttempts() int {
	return p.MaxAttempts
}

// 重试器
type Retrier struct {
	policy    RetryPolicy
	timeout   time.Duration
	onRetry   func(attempt int, err error)
	collector MetricsCollector
}

func NewRetrier(policy RetryPolicy, timeout time.Duration) *Retrier {
	return &Retrier{
		policy:  policy,
		timeout: timeout,
	}
}

func (r *Retrier) WithMetrics(collector MetricsCollector) *Retrier {
	r.collector = collector
	return r
}

func (r *Retrier) WithRetryCallback(callback func(attempt int, err error)) *Retrier {
	r.onRetry = callback
	return r
}

// Execute 执行操作并自动重试
func (r *Retrier) Execute(ctx context.Context, operation func() error) error {
	if r.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}

	var lastErr error
	for attempt := 0; attempt < r.policy.GetMaxAttempts(); attempt++ {
		select {
		case <-ctx.Done():
			return ErrRetryContextCancelled
		default:
		}

		lastErr = operation()
		if lastErr == nil {
			return nil
		}

		if !r.policy.ShouldRetry(attempt+1, lastErr) {
			break
		}

		// 调用重试回调
		if r.onRetry != nil {
			r.onRetry(attempt+1, lastErr)
		}

		// 记录重试指标
		if r.collector != nil {
			r.collector.IncrementOperationCount("", "retry")
		}

		// 等待重试间隔
		delay := r.policy.NextDelay(attempt + 1)
		if delay > 0 {
			select {
			case <-ctx.Done():
				return ErrRetryContextCancelled
			case <-time.After(delay):
			}
		}
	}

	return fmt.Errorf("%w: %v", ErrMaxRetriesExceeded, lastErr)
}

// 熔断器状态
type CircuitBreakerState int

const (
	CircuitBreakerClosed CircuitBreakerState = iota
	CircuitBreakerOpen
	CircuitBreakerHalfOpen
)

func (s CircuitBreakerState) String() string {
	switch s {
	case CircuitBreakerClosed:
		return "closed"
	case CircuitBreakerOpen:
		return "open"
	case CircuitBreakerHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// 熔断器配置
type CircuitBreakerConfig struct {
	MaxFailures      int           // 最大失败次数
	ResetTimeout     time.Duration // 重置超时时间
	FailureThreshold float64       // 失败率阈值
	MinRequests      int           // 最小请求数（用于计算失败率）
	MaxRequests      int           // 半开状态下的最大请求数
}

// 熔断器
type CircuitBreaker struct {
	config CircuitBreakerConfig
	mu     sync.RWMutex

	state         CircuitBreakerState
	failures      int
	requests      int
	successes     int
	lastFailTime  time.Time
	nextRetryTime time.Time

	// 半开状态下的统计
	halfOpenRequests int
	halfOpenFailures int

	// 回调函数
	onStateChange func(from, to CircuitBreakerState)
}

func NewCircuitBreaker(config CircuitBreakerConfig) *CircuitBreaker {
	return &CircuitBreaker{
		config: config,
		state:  CircuitBreakerClosed,
	}
}

func (cb *CircuitBreaker) WithStateChangeCallback(callback func(from, to CircuitBreakerState)) *CircuitBreaker {
	cb.onStateChange = callback
	return cb
}

// Execute 通过熔断器执行操作
func (cb *CircuitBreaker) Execute(operation func() error) error {
	if !cb.allowRequest() {
		return ErrCircuitBreakerOpen
	}

	err := operation()
	cb.recordResult(err == nil)
	return err
}

// allowRequest 判断是否允许请求
func (cb *CircuitBreaker) allowRequest() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := time.Now()

	switch cb.state {
	case CircuitBreakerClosed:
		return true

	case CircuitBreakerOpen:
		if now.After(cb.nextRetryTime) {
			cb.setState(CircuitBreakerHalfOpen)
			cb.halfOpenRequests = 0
			cb.halfOpenFailures = 0
			return true
		}
		return false

	case CircuitBreakerHalfOpen:
		return cb.halfOpenRequests < cb.config.MaxRequests

	default:
		return false
	}
}

// recordResult 记录请求结果
func (cb *CircuitBreaker) recordResult(success bool) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := time.Now()

	switch cb.state {
	case CircuitBreakerClosed:
		cb.requests++
		if success {
			cb.successes++
			cb.failures = 0
		} else {
			cb.failures++
			cb.lastFailTime = now

			// 检查是否需要打开熔断器
			if cb.shouldTrip() {
				cb.setState(CircuitBreakerOpen)
				cb.nextRetryTime = now.Add(cb.config.ResetTimeout)
			}
		}

	case CircuitBreakerHalfOpen:
		cb.halfOpenRequests++
		if success {
			// 半开状态下成功，可能关闭熔断器
			if cb.halfOpenRequests >= cb.config.MinRequests {
				cb.setState(CircuitBreakerClosed)
				cb.reset()
			}
		} else {
			// 半开状态下失败，立即打开熔断器
			cb.halfOpenFailures++
			cb.setState(CircuitBreakerOpen)
			cb.nextRetryTime = now.Add(cb.config.ResetTimeout)
		}
	}
}

// shouldTrip 判断是否应该触发熔断
func (cb *CircuitBreaker) shouldTrip() bool {
	// 基于连续失败次数
	if cb.failures >= cb.config.MaxFailures {
		return true
	}

	// 基于失败率
	if cb.requests >= cb.config.MinRequests {
		failureRate := float64(cb.requests-cb.successes) / float64(cb.requests)
		return failureRate >= cb.config.FailureThreshold
	}

	return false
}

// setState 设置状态并触发回调
func (cb *CircuitBreaker) setState(newState CircuitBreakerState) {
	oldState := cb.state
	cb.state = newState

	if cb.onStateChange != nil && oldState != newState {
		go cb.onStateChange(oldState, newState)
	}
}

// reset 重置统计信息
func (cb *CircuitBreaker) reset() {
	cb.requests = 0
	cb.successes = 0
	cb.failures = 0
}

// GetState 获取当前状态
func (cb *CircuitBreaker) GetState() CircuitBreakerState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// GetStats 获取统计信息
func (cb *CircuitBreaker) GetStats() CircuitBreakerStats {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	return CircuitBreakerStats{
		State:            cb.state,
		Requests:         cb.requests,
		Successes:        cb.successes,
		Failures:         cb.failures,
		LastFailTime:     cb.lastFailTime,
		NextRetryTime:    cb.nextRetryTime,
		HalfOpenRequests: cb.halfOpenRequests,
		HalfOpenFailures: cb.halfOpenFailures,
	}
}

// 熔断器统计信息
type CircuitBreakerStats struct {
	State            CircuitBreakerState `json:"state"`
	Requests         int                 `json:"requests"`
	Successes        int                 `json:"successes"`
	Failures         int                 `json:"failures"`
	LastFailTime     time.Time           `json:"last_fail_time"`
	NextRetryTime    time.Time           `json:"next_retry_time"`
	HalfOpenRequests int                 `json:"half_open_requests"`
	HalfOpenFailures int                 `json:"half_open_failures"`
}

// 弹性执行器 - 结合重试和熔断器
type ResilientExecutor struct {
	retrier         *Retrier
	circuitBreaker  *CircuitBreaker
	fallbackHandler func(error) error
	collector       MetricsCollector
}

func NewResilientExecutor() *ResilientExecutor {
	return &ResilientExecutor{}
}

func (re *ResilientExecutor) WithRetrier(retrier *Retrier) *ResilientExecutor {
	re.retrier = retrier
	return re
}

func (re *ResilientExecutor) WithCircuitBreaker(cb *CircuitBreaker) *ResilientExecutor {
	re.circuitBreaker = cb
	return re
}

func (re *ResilientExecutor) WithFallback(handler func(error) error) *ResilientExecutor {
	re.fallbackHandler = handler
	return re
}

func (re *ResilientExecutor) WithMetrics(collector MetricsCollector) *ResilientExecutor {
	re.collector = collector
	return re
}

// Execute 弹性执行操作
func (re *ResilientExecutor) Execute(ctx context.Context, operation func() error) error {
	wrappedOperation := operation

	// 包装熔断器
	if re.circuitBreaker != nil {
		wrappedOperation = func() error {
			return re.circuitBreaker.Execute(operation)
		}
	}

	var err error

	// 使用重试器执行
	if re.retrier != nil {
		err = re.retrier.Execute(ctx, wrappedOperation)
	} else {
		err = wrappedOperation()
	}

	// 如果有错误且配置了降级处理器
	if err != nil && re.fallbackHandler != nil {
		if re.collector != nil {
			re.collector.IncrementOperationCount("", "fallback")
		}
		return re.fallbackHandler(err)
	}

	return err
}

// 预定义的重试策略
var (
	// 默认指数退避策略
	DefaultExponentialBackoff = &ExponentialBackoffPolicy{
		BaseDelay:   2 * time.Second,
		MaxDelay:    10 * time.Second,
		BackoffRate: 1.5,
		MaxAttempts: 2,
		Jitter:      true,
	}

	// 默认固定间隔策略
	DefaultFixedInterval = &FixedIntervalPolicy{
		Interval:    1 * time.Second,
		MaxAttempts: 3,
	}

	// 默认熔断器配置
	DefaultCircuitBreakerConfig = CircuitBreakerConfig{
		MaxFailures:      20,               // 最大失败次数：20次（考虑200专线规模）
		ResetTimeout:     30 * time.Second, // 重置超时：30秒（快速恢复）
		FailureThreshold: 0.7,              // 失败阈值：70%
		MinRequests:      30,               // 最小请求数：30次（开始计算失败率）
		MaxRequests:      5,                // 半开状态最大请求：5次（测试连接恢复）
	}
)

// 工厂函数
func NewDefaultRetrier(timeout time.Duration) *Retrier {
	return NewRetrier(DefaultExponentialBackoff, timeout)
}

func NewDefaultCircuitBreaker() *CircuitBreaker {
	return NewCircuitBreaker(DefaultCircuitBreakerConfig)
}

func NewDefaultResilientExecutor() *ResilientExecutor {
	return NewResilientExecutor().
		WithRetrier(NewDefaultRetrier(60 * time.Second))
	// 移除断路器，因为专线监控场景不适合使用断路器
	// 在200专线并发场景下，断路器容易误触发
}

// 便利函数
func ExecuteWithRetry(ctx context.Context, operation func() error, policy RetryPolicy) error {
	retrier := NewRetrier(policy, 0)
	return retrier.Execute(ctx, operation)
}

func ExecuteWithCircuitBreaker(operation func() error, config CircuitBreakerConfig) error {
	cb := NewCircuitBreaker(config)
	return cb.Execute(operation)
}

func ExecuteResilient(ctx context.Context, operation func() error) error {
	executor := NewDefaultResilientExecutor()
	return executor.Execute(ctx, operation)
}

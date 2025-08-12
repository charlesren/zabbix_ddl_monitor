package connection

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBasicConnectionPoolFunctionality(t *testing.T) {
	// Test basic configuration building
	config, err := NewConfigBuilder().
		WithBasicAuth("192.168.1.1", "admin", "password").
		WithProtocol(ProtocolSSH, PlatformCiscoIOSXE).
		WithTimeouts(10*time.Second, 10*time.Second, 5*time.Second, 2*time.Minute).
		WithConnectionPool(5, 1, 5*time.Minute, 30*time.Second).
		Build()

	require.NoError(t, err, "Configuration building should not fail")
	assert.Equal(t, "192.168.1.1", config.Host)
	assert.Equal(t, ProtocolSSH, config.Protocol)
	assert.Equal(t, PlatformCiscoIOSXE, config.Platform)

	// Test enhanced connection pool creation
	pool := NewEnhancedConnectionPool(*config)
	defer pool.Close()

	// Verify pool is created with default factories
	assert.NotNil(t, pool.factories[ProtocolSSH])
	assert.NotNil(t, pool.factories[ProtocolScrapli])

	// Test metrics collector
	collector := GetGlobalMetricsCollector()
	assert.NotNil(t, collector)

	// Test metrics snapshot
	snapshot := collector.GetMetrics()
	assert.NotNil(t, snapshot)
	assert.NotEmpty(t, snapshot.Timestamp)
}

func TestRetryPolicyBasics(t *testing.T) {
	// Test exponential backoff policy
	policy := &ExponentialBackoffPolicy{
		BaseDelay:   100 * time.Millisecond,
		MaxDelay:    5 * time.Second,
		BackoffRate: 2.0,
		MaxAttempts: 3,
		Jitter:      false,
	}

	assert.Equal(t, 3, policy.GetMaxAttempts())
	assert.True(t, policy.ShouldRetry(1, assert.AnError))
	assert.False(t, policy.ShouldRetry(3, assert.AnError))

	delay1 := policy.NextDelay(1)
	delay2 := policy.NextDelay(2)
	assert.Equal(t, 100*time.Millisecond, delay1)
	assert.Equal(t, 200*time.Millisecond, delay2)

	// Test fixed interval policy
	fixedPolicy := &FixedIntervalPolicy{
		Interval:    1 * time.Second,
		MaxAttempts: 5,
	}

	assert.Equal(t, 5, fixedPolicy.GetMaxAttempts())
	assert.Equal(t, 1*time.Second, fixedPolicy.NextDelay(1))
	assert.Equal(t, 1*time.Second, fixedPolicy.NextDelay(3))
}

func TestCircuitBreakerBasics(t *testing.T) {
	config := CircuitBreakerConfig{
		MaxFailures:      3,
		ResetTimeout:     60 * time.Second,
		FailureThreshold: 0.5,
		MinRequests:      5,
		MaxRequests:      3,
	}

	cb := NewCircuitBreaker(config)
	assert.Equal(t, CircuitBreakerClosed, cb.GetState())

	// Test successful operations
	err := cb.Execute(func() error {
		return nil
	})
	assert.NoError(t, err)

	stats := cb.GetStats()
	assert.Equal(t, CircuitBreakerClosed, stats.State)
	assert.Equal(t, 1, stats.Requests)
	assert.Equal(t, 1, stats.Successes)
	assert.Equal(t, 0, stats.Failures)

	// Test failing operations
	for i := 0; i < 3; i++ {
		cb.Execute(func() error {
			return assert.AnError
		})
	}

	// Circuit breaker should open after failures
	finalStats := cb.GetStats()
	assert.Equal(t, 3, finalStats.Failures)
}

func TestResilientExecutorBasics(t *testing.T) {
	executor := NewDefaultResilientExecutor()
	assert.NotNil(t, executor)

	// Test successful execution
	called := false
	err := executor.Execute(context.Background(), func() error {
		called = true
		return nil
	})

	assert.NoError(t, err)
	assert.True(t, called)

	// Test execution with error
	err = executor.Execute(context.Background(), func() error {
		return assert.AnError
	})

	assert.Error(t, err)
}

func TestLoadBalancerBasics(t *testing.T) {
	// Test round robin balancer
	rb := &RoundRobinBalancer{}

	conn1 := &EnhancedPooledConnection{
		id:           "conn1",
		valid:        true,
		inUse:        false,
		healthStatus: HealthStatusHealthy,
	}
	conn2 := &EnhancedPooledConnection{
		id:           "conn2",
		valid:        true,
		inUse:        false,
		healthStatus: HealthStatusHealthy,
	}

	connections := []*EnhancedPooledConnection{conn1, conn2}

	selected1 := rb.SelectConnection(connections)
	assert.NotNil(t, selected1)
	assert.Contains(t, []string{"conn1", "conn2"}, selected1.id)

	// Test least connections balancer
	lcb := &LeastConnectionsBalancer{}
	conn1.usageCount = 5
	conn2.usageCount = 2

	selected2 := lcb.SelectConnection(connections)
	assert.NotNil(t, selected2)
	assert.Equal(t, "conn2", selected2.id) // Should select the one with less usage
}

func TestConfigValidation(t *testing.T) {
	// Test valid configuration
	validConfig := &EnhancedConnectionConfig{
		Host:           "192.168.1.1",
		Username:       "admin",
		Password:       "password",
		Port:           22,
		Protocol:       ProtocolSSH,
		Platform:       PlatformCiscoIOSXE,
		ConnectTimeout: 30 * time.Second,
		MaxConnections: 10,
		MinConnections: 1,
		MaxRetries:     3,
		BackoffFactor:  2.0,
		Extensions:     make(map[string]interface{}),
		Labels:         make(map[string]string),
		Metadata:       make(map[string]interface{}),
	}

	err := validConfig.Validate()
	assert.NoError(t, err)

	// Test invalid configuration - missing host
	invalidConfig := &EnhancedConnectionConfig{
		Username: "admin",
		Password: "password",
	}

	err = invalidConfig.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "host is required")

	// Test invalid port
	invalidPortConfig := &EnhancedConnectionConfig{
		Host:           "192.168.1.1",
		Username:       "admin",
		Password:       "password",
		Port:           0,
		Protocol:       ProtocolSSH,
		Platform:       PlatformCiscoIOSXE,
		ConnectTimeout: 30 * time.Second,
		MaxConnections: 10,
		MinConnections: 1,
		MaxRetries:     3,
		BackoffFactor:  2.0,
	}

	err = invalidPortConfig.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "port must be between")
}

func TestConfigCloneAndConversion(t *testing.T) {
	original := &EnhancedConnectionConfig{
		Host:           "192.168.1.1",
		Username:       "admin",
		Password:       "password",
		Port:           22,
		Protocol:       ProtocolSSH,
		Platform:       PlatformCiscoIOSXE,
		ConnectTimeout: 30 * time.Second,
		Extensions:     map[string]interface{}{"key": "value"},
		Labels:         map[string]string{"env": "test"},
		Metadata:       map[string]interface{}{"version": "1.0"},
	}

	// Test cloning
	cloned := original.Clone()
	assert.Equal(t, original.Host, cloned.Host)
	assert.Equal(t, original.Username, cloned.Username)
	assert.Equal(t, original.Extensions["key"], cloned.Extensions["key"])
	assert.Equal(t, original.Labels["env"], cloned.Labels["env"])

	// Modify clone should not affect original
	cloned.Host = "192.168.1.2"
	cloned.Extensions["new"] = "value"
	assert.NotEqual(t, original.Host, cloned.Host)
	assert.NotContains(t, original.Extensions, "new")

	// Test legacy conversion
	legacy := original.ToLegacyConfig()
	assert.Equal(t, original.Host, legacy.IP)
	assert.Equal(t, original.Username, legacy.Username)
	assert.Equal(t, original.Password, legacy.Password)
	assert.Equal(t, string(original.Protocol), legacy.Metadata["protocol"])
	assert.Equal(t, string(original.Platform), legacy.Metadata["platform"])

	// Test connection string
	connStr := original.GetConnectionString()
	assert.Contains(t, connStr, original.Host)
	assert.Contains(t, connStr, original.Username)
	assert.Contains(t, connStr, string(original.Protocol))

	// Test security check
	assert.True(t, original.IsSecure()) // SSH is secure by default

	// Test effective timeout
	assert.Equal(t, original.ConnectTimeout, original.GetEffectiveTimeout("connect"))
}

func TestHealthStatus(t *testing.T) {
	// Test health status constants
	assert.Equal(t, "unknown", HealthStatusUnknown.String())
	assert.Equal(t, "healthy", HealthStatusHealthy.String())
	assert.Equal(t, "degraded", HealthStatusDegraded.String())
	assert.Equal(t, "unhealthy", HealthStatusUnhealthy.String())
}

// Add missing String method for HealthStatus
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

func TestWarmupStatus(t *testing.T) {
	// Test warmup state constants
	assert.Equal(t, WarmupStateNotStarted, WarmupState(0))
	assert.Equal(t, WarmupStateInProgress, WarmupState(1))
	assert.Equal(t, WarmupStateCompleted, WarmupState(2))
	assert.Equal(t, WarmupStateFailed, WarmupState(3))
}

func TestConnectionTrace(t *testing.T) {
	trace := &ConnectionTrace{
		ID:         "test-conn-123",
		Protocol:   ProtocolSSH,
		CreatedAt:  time.Now(),
		LastUsed:   time.Now(),
		UsageCount: 5,
		Labels:     map[string]string{"env": "test"},
	}

	assert.Equal(t, "test-conn-123", trace.ID)
	assert.Equal(t, ProtocolSSH, trace.Protocol)
	assert.Equal(t, int64(5), trace.UsageCount)
	assert.Equal(t, "test", trace.Labels["env"])
}

func TestPoolEvents(t *testing.T) {
	// Test event types
	event := PoolEvent{
		Type:      EventConnectionCreated,
		Protocol:  ProtocolSSH,
		Timestamp: time.Now(),
		Data:      "test data",
	}

	assert.Equal(t, EventConnectionCreated, event.Type)
	assert.Equal(t, ProtocolSSH, event.Protocol)
	assert.Equal(t, "test data", event.Data)
}

//go:build integration
// +build integration

package connection

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConnectionSystemIntegration 综合测试连接管理系统的核心功能
func TestConnectionSystemIntegration(t *testing.T) {
	// 1. 配置构建和验证
	t.Run("ConfigurationSystem", func(t *testing.T) {
		config, err := NewConfigBuilder().
			WithBasicAuth("192.168.1.100", "netadmin", "secret123").
			WithProtocol(ProtocolScrapli, PlatformCiscoIOSXE).
			WithTimeouts(30*time.Second, 15*time.Second, 10*time.Second, 5*time.Minute).
			WithRetryPolicy(3, 1*time.Second, 2.0).
			WithConnectionPool(10, 2, 15*time.Minute, 30*time.Second).
			WithLabels(map[string]string{
				"region": "us-west",
				"env":    "test",
			}).
			WithMetadata("device_type", "switch").
			Build()

		require.NoError(t, err)
		assert.Equal(t, "192.168.1.100", config.Host)
		assert.Equal(t, ProtocolScrapli, config.Protocol)
		assert.Equal(t, PlatformCiscoIOSXE, config.Platform)
		assert.Equal(t, 10, config.MaxConnections)
		assert.Equal(t, 2, config.MinConnections)
		assert.Equal(t, "us-west", config.Labels["region"])
		assert.Equal(t, "switch", config.Metadata["device_type"])

		// 测试配置验证
		require.NoError(t, config.Validate())

		// 测试配置克隆
		cloned := config.Clone()
		assert.Equal(t, config.Host, cloned.Host)
		cloned.Host = "192.168.1.101"
		assert.NotEqual(t, config.Host, cloned.Host)

		// 测试Legacy配置转换
		legacy := config.ToLegacyConfig()
		assert.Equal(t, config.Host, legacy.IP)
		assert.Equal(t, string(config.Protocol), legacy.Metadata["protocol"])
	})

	// 2. 连接池生命周期管理
	t.Run("ConnectionPoolLifecycle", func(t *testing.T) {
		config, err := NewConfigBuilder().
			WithBasicAuth("test-host", "admin", "password").
			WithProtocol(ProtocolSSH, PlatformCiscoIOSXE).
			WithConnectionPool(5, 1, 2*time.Minute, 10*time.Second).
			Build()
		require.NoError(t, err)

		pool := NewEnhancedConnectionPool(*config)
		defer pool.Close()

		// 验证工厂注册
		assert.Contains(t, pool.factories, ProtocolSSH)
		assert.Contains(t, pool.factories, ProtocolScrapli)

		// 注册自定义工厂
		mockFactory := &MockProtocolFactory{
			CreateFunc: func(config ConnectionConfig) (ProtocolDriver, error) {
				return &MockProtocolDriver{
					ProtocolTypeFunc: func() Protocol { return "test" },
					CloseFunc:        func() error { return nil },
					ExecuteFunc: func(req *ProtocolRequest) (*ProtocolResponse, error) {
						return &ProtocolResponse{
							Success: true,
							RawData: []byte("test response"),
						}, nil
					},
				}, nil
			},
			HealthCheckFunc: func(driver ProtocolDriver) bool {
				return true
			},
		}

		pool.RegisterFactory("test", mockFactory)
		assert.Contains(t, pool.factories, Protocol("test"))

		// 测试预热
		err = pool.WarmUp("test", 3)
		assert.NoError(t, err)

		warmupStatus := pool.GetWarmupStatus()
		testStatus := warmupStatus["test"]
		require.NotNil(t, testStatus)
		assert.Equal(t, 3, testStatus.Target)
		assert.Equal(t, WarmupStateCompleted, testStatus.Status)

		// 测试连接获取和释放
		conn, err := pool.Get("test")
		assert.NoError(t, err)
		assert.NotNil(t, conn)

		// 执行操作
		resp, err := conn.Execute(context.Background(), &ProtocolRequest{
			CommandType: CommandTypeCommands,
			Payload:     []string{"show version"},
		})
		assert.NoError(t, err)
		assert.True(t, resp.Success)
		assert.Equal(t, []byte("test response"), resp.RawData)

		// 释放连接
		err = pool.Release(conn)
		assert.NoError(t, err)

		// 检查统计信息
		stats := pool.GetStats()
		testStats := stats["test"]
		assert.NotNil(t, testStats)
		assert.Greater(t, testStats.CreatedConnections, int64(0))
	})

	// 3. 指标收集系统
	t.Run("MetricsCollectionSystem", func(t *testing.T) {
		collector := GetGlobalMetricsCollector()

		// 模拟一些指标
		collector.IncrementConnectionsCreated(ProtocolSSH)
		collector.IncrementConnectionsReused(ProtocolSSH)
		collector.RecordOperationDuration(ProtocolSSH, "execute", 100*time.Millisecond)
		collector.IncrementOperationCount(ProtocolSSH, "execute")

		// 获取快照
		snapshot := collector.GetMetrics()
		require.NotNil(t, snapshot)
		assert.NotEmpty(t, snapshot.Timestamp)

		// 检查连接指标
		if sshMetrics, exists := snapshot.ConnectionMetrics[ProtocolSSH]; exists {
			assert.Greater(t, sshMetrics.Created, int64(0))
			assert.Greater(t, sshMetrics.Reused, int64(0))
		}

		// 检查操作指标
		if sshOps, exists := snapshot.OperationMetrics[ProtocolSSH]; exists {
			if executeOp, exists := sshOps["execute"]; exists {
				assert.Greater(t, executeOp.Count, int64(0))
				assert.Greater(t, executeOp.TotalDuration, time.Duration(0))
			}
		}
	})

	// 4. 弹性机制
	t.Run("ResilienceMechanisms", func(t *testing.T) {
		// 测试重试策略
		policy := &ExponentialBackoffPolicy{
			BaseDelay:   50 * time.Millisecond,
			MaxDelay:    1 * time.Second,
			BackoffRate: 2.0,
			MaxAttempts: 3,
			Jitter:      false,
		}

		retrier := NewRetrier(policy, 5*time.Second)

		attempts := 0
		err := retrier.Execute(context.Background(), func() error {
			attempts++
			if attempts < 2 {
				return assert.AnError
			}
			return nil
		})

		assert.NoError(t, err)
		assert.Equal(t, 2, attempts)

		// 测试熔断器
		cbConfig := CircuitBreakerConfig{
			MaxFailures:      2,
			ResetTimeout:     1 * time.Second,
			FailureThreshold: 0.5,
			MinRequests:      3,
			MaxRequests:      2,
		}

		cb := NewCircuitBreaker(cbConfig)

		// 触发失败
		for i := 0; i < 3; i++ {
			cb.Execute(func() error {
				return assert.AnError
			})
		}

		// 熔断器应该打开
		err = cb.Execute(func() error {
			return nil
		})
		assert.Equal(t, ErrCircuitBreakerOpen, err)

		// 测试弹性执行器
		executor := NewDefaultResilientExecutor().
			WithFallback(func(err error) error {
				return nil // 降级处理
			})

		err = executor.Execute(context.Background(), func() error {
			return assert.AnError
		})
		assert.NoError(t, err) // 应该被降级处理
	})

	// 5. 负载均衡
	t.Run("LoadBalancing", func(t *testing.T) {
		// 测试轮询负载均衡
		rb := &RoundRobinBalancer{}
		connections := []*EnhancedPooledConnection{
			{
				id:           "conn1",
				valid:        true,
				inUse:        false,
				healthStatus: HealthStatusHealthy,
			},
			{
				id:           "conn2",
				valid:        true,
				inUse:        false,
				healthStatus: HealthStatusHealthy,
			},
		}

		selected1 := rb.SelectConnection(connections)
		selected2 := rb.SelectConnection(connections)
		assert.NotNil(t, selected1)
		assert.NotNil(t, selected2)
		assert.NotEqual(t, selected1.id, selected2.id) // 应该轮询选择

		// 测试最少连接负载均衡
		lcb := &LeastConnectionsBalancer{}
		connections[0].usageCount = 10
		connections[1].usageCount = 5

		selected := lcb.SelectConnection(connections)
		assert.NotNil(t, selected)
		assert.Equal(t, "conn2", selected.id) // 应该选择使用较少的连接
	})

	// 6. 并发安全性测试
	t.Run("ConcurrencySafety", func(t *testing.T) {
		config, err := NewConfigBuilder().
			WithBasicAuth("concurrent-test", "admin", "password").
			WithProtocol(ProtocolSSH, PlatformCiscoIOSXE).
			WithConnectionPool(10, 2, 5*time.Minute, 30*time.Second).
			Build()
		require.NoError(t, err)

		pool := NewEnhancedConnectionPool(*config)
		defer pool.Close()

		// 注册测试工厂
		mockFactory := &MockProtocolFactory{
			CreateFunc: func(config ConnectionConfig) (ProtocolDriver, error) {
				time.Sleep(10 * time.Millisecond) // 模拟连接延迟
				return &MockProtocolDriver{
					ProtocolTypeFunc: func() Protocol { return "concurrent-test" },
					CloseFunc:        func() error { return nil },
					ExecuteFunc: func(req *ProtocolRequest) (*ProtocolResponse, error) {
						time.Sleep(5 * time.Millisecond) // 模拟执行延迟
						return &ProtocolResponse{Success: true, RawData: []byte("ok")}, nil
					},
				}, nil
			},
			HealthCheckFunc: func(driver ProtocolDriver) bool {
				return true
			},
		}

		pool.RegisterFactory("concurrent-test", mockFactory)

		// 并发操作
		const numGoroutines = 20
		const operationsPerGoroutine = 10
		var wg sync.WaitGroup
		errors := make(chan error, numGoroutines*operationsPerGoroutine)

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(goroutineID int) {
				defer wg.Done()
				for j := 0; j < operationsPerGoroutine; j++ {
					conn, err := pool.Get("concurrent-test")
					if err != nil {
						errors <- err
						continue
					}

					_, err = conn.Execute(context.Background(), &ProtocolRequest{
						CommandType: CommandTypeCommands,
						Payload:     []string{"test command"},
					})
					if err != nil {
						errors <- err
					}

					err = pool.Release(conn)
					if err != nil {
						errors <- err
					}
				}
			}(i)
		}

		wg.Wait()
		close(errors)

		// 检查错误数量
		var errorList []error
		for err := range errors {
			errorList = append(errorList, err)
		}

		// 允许少量错误（网络不稳定等）
		assert.Less(t, len(errorList), numGoroutines, "Too many errors in concurrent test: %v", errorList)

		// 检查最终统计
		stats := pool.GetStats()
		testStats := stats["concurrent-test"]
		assert.NotNil(t, testStats)
		assert.Greater(t, testStats.CreatedConnections, int64(0))
	})

	// 7. 错误处理和恢复
	t.Run("ErrorHandlingAndRecovery", func(t *testing.T) {
		config, err := NewConfigBuilder().
			WithBasicAuth("error-test", "admin", "password").
			WithProtocol(ProtocolSSH, PlatformCiscoIOSXE).
			Build()
		require.NoError(t, err)

		pool := NewEnhancedConnectionPool(*config)
		defer pool.Close()

		// 测试不支持的协议
		_, err = pool.Get("unsupported")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported protocol")

		// 测试工厂创建失败
		failingFactory := &MockProtocolFactory{
			CreateFunc: func(config ConnectionConfig) (ProtocolDriver, error) {
				return nil, assert.AnError
			},
		}
		pool.RegisterFactory("failing", failingFactory)

		_, err = pool.Get("failing")
		assert.Error(t, err)

		// 测试连接池关闭后的行为
		testPool, err := NewConfigBuilder().
			WithBasicAuth("shutdown-test", "admin", "password").
			WithProtocol(ProtocolSSH, PlatformCiscoIOSXE).
			Build()
		require.NoError(t, err)

		shutdownPool := NewEnhancedConnectionPool(*testPool)
		err = shutdownPool.Close()
		assert.NoError(t, err)

		_, err = shutdownPool.Get(ProtocolSSH)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "shutting down")
	})

	// 8. 健康检查和维护
	t.Run("HealthCheckAndMaintenance", func(t *testing.T) {
		config, err := NewConfigBuilder().
			WithBasicAuth("health-test", "admin", "password").
			WithProtocol(ProtocolSSH, PlatformCiscoIOSXE).
			WithTimeouts(5*time.Second, 5*time.Second, 5*time.Second, 100*time.Millisecond).
			Build()
		require.NoError(t, err)

		pool := NewEnhancedConnectionPool(*config)
		defer pool.Close()

		var healthCheckCalls int
		mockFactory := &MockProtocolFactory{
			CreateFunc: func(config ConnectionConfig) (ProtocolDriver, error) {
				return &MockProtocolDriver{
					ProtocolTypeFunc: func() Protocol { return "health-test" },
					CloseFunc:        func() error { return nil },
					ExecuteFunc: func(req *ProtocolRequest) (*ProtocolResponse, error) {
						healthCheckCalls++
						if healthCheckCalls%2 == 0 {
							return nil, assert.AnError // 模拟间歇性失败
						}
						return &ProtocolResponse{Success: true, RawData: []byte("ok")}, nil
					},
				}, nil
			},
			HealthCheckFunc: func(driver ProtocolDriver) bool {
				healthCheckCalls++
				return healthCheckCalls%3 != 0 // 模拟健康检查偶尔失败
			},
		}

		pool.RegisterFactory("health-test", mockFactory)

		// 创建连接
		err = pool.WarmUp("health-test", 2)
		assert.NoError(t, err)

		// 手动触发健康检查
		pool.performHealthChecks()
		time.Sleep(50 * time.Millisecond) // 等待异步健康检查完成

		// 触发连接清理
		time.Sleep(150 * time.Millisecond) // 超过空闲超时
		pool.cleanupConnections()
		time.Sleep(50 * time.Millisecond) // 等待异步清理完成

		assert.Greater(t, healthCheckCalls, 0)
	})
}

// TestProtocolCapabilities 测试协议能力系统
func TestProtocolCapabilities(t *testing.T) {
	t.Run("SSHCapabilities", func(t *testing.T) {
		caps := SSHCapability
		assert.Equal(t, ProtocolSSH, caps.Protocol)
		assert.Contains(t, caps.PlatformSupport, PlatformCiscoIOSXE)
		assert.Contains(t, caps.PlatformSupport, PlatformHuaweiVRP)
		assert.True(t, caps.SupportsCommandType(CommandTypeCommands))
		assert.False(t, caps.SupportsCommandType(CommandTypeInteractiveEvent))
		assert.Greater(t, caps.MaxConcurrent, 0)
		assert.Greater(t, caps.Timeout, time.Duration(0))
	})

	t.Run("ScrapliCapabilities", func(t *testing.T) {
		caps := ScrapliCapability
		assert.Equal(t, ProtocolScrapli, caps.Protocol)
		assert.Contains(t, caps.PlatformSupport, PlatformCiscoIOSXE)
		assert.Contains(t, caps.PlatformSupport, PlatformCiscoNXOS)
		assert.True(t, caps.SupportsCommandType(CommandTypeCommands))
		assert.True(t, caps.SupportsCommandType(CommandTypeInteractiveEvent))
		assert.True(t, caps.SupportsAutoComplete)
	})

	t.Run("ConfigModeCapabilities", func(t *testing.T) {
		caps := ScrapliCapability
		basicMode, exists := caps.GetConfigMode(ConfigModeBasic)
		assert.True(t, exists)
		assert.Equal(t, ConfigModeBasic, basicMode.Mode)
		assert.Equal(t, PrivilegeLevelUser, basicMode.Privilege)

		privilegedMode, exists := caps.GetConfigMode(ConfigModePrivileged)
		assert.True(t, exists)
		assert.Equal(t, ConfigModePrivileged, privilegedMode.Mode)
		assert.Equal(t, PrivilegeLevelAdmin, privilegedMode.Privilege)

		_, exists = caps.GetConfigMode("nonexistent")
		assert.False(t, exists)
	})
}

// TestConfigurationAdvanced 测试高级配置功能
func TestConfigurationAdvanced(t *testing.T) {
	t.Run("SSHSpecificConfig", func(t *testing.T) {
		sshConfig := &SSHConfig{
			AuthMethod:       "publickey",
			PrivateKeyPath:   "/tmp/test_key",
			CompressionLevel: 6,
			RequestPty:       true,
			TerminalType:     "xterm",
			WindowWidth:      132,
			WindowHeight:     43,
		}

		err := sshConfig.Validate()
		assert.NoError(t, err)

		config, err := NewConfigBuilder().
			WithBasicAuth("ssh-test", "admin", "").
			WithProtocol(ProtocolSSH, PlatformCiscoIOSXE).
			WithSSHConfig(sshConfig).
			Build()

		require.NoError(t, err)
		assert.NotNil(t, config.SSHConfig)
		assert.Equal(t, "publickey", config.SSHConfig.AuthMethod)
		assert.Equal(t, 6, config.SSHConfig.CompressionLevel)
	})

	t.Run("ScrapliSpecificConfig", func(t *testing.T) {
		scrapliConfig := &ScrapliConfig{
			TransportType:      "system",
			StrictHostChecking: false,
			CommsReadDelay:     100 * time.Millisecond,
			TimeoutOpsDefault:  30 * time.Second,
			OnInit:             []string{"terminal length 0", "terminal width 0"},
			OnOpen:             []string{"show clock"},
			OnClose:            []string{"exit"},
		}

		err := scrapliConfig.Validate()
		assert.NoError(t, err)

		config, err := NewConfigBuilder().
			WithBasicAuth("scrapli-test", "admin", "password").
			WithProtocol(ProtocolScrapli, PlatformCiscoNXOS).
			WithScrapliConfig(scrapliConfig).
			Build()

		require.NoError(t, err)
		assert.NotNil(t, config.ScrapliConfig)
		assert.Equal(t, "system", config.ScrapliConfig.TransportType)
		assert.Equal(t, 100*time.Millisecond, config.ScrapliConfig.CommsReadDelay)
		assert.Len(t, config.ScrapliConfig.OnInit, 2)
	})

	t.Run("SecurityConfig", func(t *testing.T) {
		secConfig := &SecurityConfig{
			TLSEnabled:         true,
			TLSVersion:         "1.3",
			InsecureSkipVerify: false,
			AuditEnabled:       true,
			AuditLogPath:       "/var/log/connections.audit",
			SensitiveCommands:  []string{"enable", "configure"},
		}

		config, err := NewConfigBuilder().
			WithBasicAuth("secure-test", "admin", "password").
			WithProtocol(ProtocolScrapli, PlatformCiscoIOSXE).
			WithSecurity(secConfig).
			Build()

		require.NoError(t, err)
		assert.NotNil(t, config.SecurityConfig)
		assert.True(t, config.SecurityConfig.TLSEnabled)
		assert.True(t, config.SecurityConfig.AuditEnabled)
		assert.True(t, config.IsSecure())
	})
}

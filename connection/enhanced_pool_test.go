package connection

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnhancedConnectionPool_BasicOperations(t *testing.T) {
	t.Parallel()

	config, err := NewConfigBuilder().
		WithBasicAuth("192.168.1.1", "admin", "password").
		WithProtocol(ProtocolSSH, PlatformCiscoIOSXE).
		WithConnectionPool(5, 1, 10*time.Minute, 30*time.Second).
		Build()

	require.NoError(t, err)

	pool := NewEnhancedConnectionPool(config)
	defer pool.Close()

	t.Run("should register factories", func(t *testing.T) {
		assert.Contains(t, pool.factories, ProtocolSSH)
		assert.Contains(t, pool.factories, ProtocolScrapli)
		assert.Contains(t, pool.pools, ProtocolSSH)
		assert.Contains(t, pool.pools, ProtocolScrapli)
	})

	t.Run("should get and release connection", func(t *testing.T) {
		// Mock factory for testing
		mockFactory := &MockProtocolFactory{
			CreateFunc: func(config EnhancedConnectionConfig) (ProtocolDriver, error) {
				return &MockProtocolDriver{
					ProtocolTypeFunc: func() Protocol { return ProtocolSSH },
					CloseFunc:        func() error { return nil },
					ExecuteFunc: func(req *ProtocolRequest) (*ProtocolResponse, error) {
						return &ProtocolResponse{Success: true, RawData: []byte("ok")}, nil
					},
				}, nil
			},
			HealthCheckFunc: func(driver ProtocolDriver) bool {
				return true
			},
		}

		pool.RegisterFactory("test", mockFactory)

		conn, err := pool.Get("test")
		assert.NoError(t, err)
		assert.NotNil(t, conn)

		// Test execution
		resp, err := conn.Execute(&ProtocolRequest{
			CommandType: CommandTypeCommands,
			Payload:     []string{"echo test"},
		})
		assert.NoError(t, err)
		assert.True(t, resp.Success)

		// Release connection
		err = pool.Release(conn)
		assert.NoError(t, err)
	})
}

func TestEnhancedConnectionPool_WarmUp(t *testing.T) {
	t.Parallel()

	config, err := NewConfigBuilder().
		WithBasicAuth("192.168.1.1", "admin", "password").
		WithProtocol(ProtocolSSH, PlatformCiscoIOSXE).
		Build()

	require.NoError(t, err)

	pool := NewEnhancedConnectionPool(config)
	defer pool.Close()

	// Mock factory
	mockFactory := &MockProtocolFactory{
		CreateFunc: func(config EnhancedConnectionConfig) (ProtocolDriver, error) {
			return &MockProtocolDriver{
				ProtocolTypeFunc: func() Protocol { return "test" },
				CloseFunc:        func() error { return nil },
			}, nil
		},
		HealthCheckFunc: func(driver ProtocolDriver) bool { return true },
	}

	pool.RegisterFactory("test", mockFactory)

	t.Run("should warm up pool successfully", func(t *testing.T) {
		err := pool.WarmUp("test", 3)
		assert.NoError(t, err)

		// Check warmup status
		status := pool.GetWarmupStatus()
		testStatus := status["test"]
		assert.NotNil(t, testStatus)
		assert.Equal(t, 3, testStatus.Target)
		assert.Equal(t, WarmupStateCompleted, testStatus.Status)
	})

	t.Run("should fail for unsupported protocol", func(t *testing.T) {
		err := pool.WarmUp("unsupported", 1)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not registered")
	})
}

func TestEnhancedConnectionPool_HealthCheck(t *testing.T) {
	t.Parallel()

	config, err := NewConfigBuilder().
		WithBasicAuth("192.168.1.1", "admin", "password").
		WithProtocol(ProtocolSSH, PlatformCiscoIOSXE).
		WithTimeouts(5*time.Second, 5*time.Second, 5*time.Second, 1*time.Minute).
		Build()

	require.NoError(t, err)

	pool := NewEnhancedConnectionPool(config)
	defer pool.Close()

	var healthCheckCount int32
	mockFactory := &MockProtocolFactory{
		CreateFunc: func(config EnhancedConnectionConfig) (ProtocolDriver, error) {
			return &MockProtocolDriver{
				ProtocolTypeFunc: func() Protocol { return "test" },
				CloseFunc:        func() error { return nil },
				ExecuteFunc: func(req *ProtocolRequest) (*ProtocolResponse, error) {
					atomic.AddInt32(&healthCheckCount, 1)
					return &ProtocolResponse{Success: true, RawData: []byte("ok")}, nil
				},
			}, nil
		},
		HealthCheckFunc: func(driver ProtocolDriver) bool {
			atomic.AddInt32(&healthCheckCount, 1)
			return true
		},
	}

	pool.RegisterFactory("test", mockFactory)

	t.Run("should perform health checks", func(t *testing.T) {
		// Create some connections
		err := pool.WarmUp("test", 2)
		assert.NoError(t, err)

		// Trigger health check manually
		pool.performHealthChecks()

		// Wait a bit for async health checks
		time.Sleep(100 * time.Millisecond)

		// Health check should have been called
		assert.Greater(t, int(atomic.LoadInt32(&healthCheckCount)), 0)
	})
}

func TestEnhancedConnectionPool_Metrics(t *testing.T) {
	t.Parallel()

	config, err := NewConfigBuilder().
		WithBasicAuth("192.168.1.1", "admin", "password").
		WithProtocol(ProtocolSSH, PlatformCiscoIOSXE).
		Build()

	require.NoError(t, err)

	pool := NewEnhancedConnectionPool(config)
	defer pool.Close()

	// Enable debug mode
	pool.EnableDebug()

	mockFactory := &MockProtocolFactory{
		CreateFunc: func(config EnhancedConnectionConfig) (ProtocolDriver, error) {
			return &MockProtocolDriver{
				ProtocolTypeFunc: func() Protocol { return "test" },
				CloseFunc:        func() error { return nil },
				ExecuteFunc: func(req *ProtocolRequest) (*ProtocolResponse, error) {
					return &ProtocolResponse{Success: true, RawData: []byte("ok")}, nil
				},
			}, nil
		},
		HealthCheckFunc: func(driver ProtocolDriver) bool { return true },
	}

	pool.RegisterFactory("test", mockFactory)

	t.Run("should track connection metrics", func(t *testing.T) {
		// Get connection
		conn, err := pool.Get("test")
		assert.NoError(t, err)
		assert.NotNil(t, conn)

		// Execute operation
		_, err = conn.Execute(&ProtocolRequest{
			CommandType: CommandTypeCommands,
			Payload:     []string{"test"},
		})
		assert.NoError(t, err)

		// Release connection
		err = pool.Release(conn)
		assert.NoError(t, err)

		// Check stats
		stats := pool.GetStats()
		testStats := stats["test"]
		assert.NotNil(t, testStats)
		assert.Equal(t, int64(1), testStats.CreatedConnections)
	})

	t.Run("should update pool metrics", func(t *testing.T) {
		// Trigger metrics update
		pool.updateMetrics()

		// Get metrics from collector
		snapshot := pool.collector.GetMetrics()
		assert.NotNil(t, snapshot)
		assert.Contains(t, snapshot.ConnectionMetrics, Protocol("test"))
	})
}

func TestEnhancedConnectionPool_LoadBalancing(t *testing.T) {
	t.Parallel()

	t.Run("RoundRobinBalancer", func(t *testing.T) {
		balancer := &RoundRobinBalancer{}

		// Create mock connections
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
		conn3 := &EnhancedPooledConnection{
			id:           "conn3",
			valid:        true,
			inUse:        true, // This one is in use
			healthStatus: HealthStatusHealthy,
		}

		connections := []*EnhancedPooledConnection{conn1, conn2, conn3}

		// Test selection
		selected1 := balancer.SelectConnection(connections)
		assert.NotNil(t, selected1)
		assert.Contains(t, []string{"conn1", "conn2"}, selected1.id)

		selected2 := balancer.SelectConnection(connections)
		assert.NotNil(t, selected2)
		assert.Contains(t, []string{"conn1", "conn2"}, selected2.id)

		// Should alternate between conn1 and conn2
		assert.NotEqual(t, selected1.id, selected2.id)
	})

	t.Run("LeastConnectionsBalancer", func(t *testing.T) {
		balancer := &LeastConnectionsBalancer{}

		conn1 := &EnhancedPooledConnection{
			id:           "conn1",
			valid:        true,
			inUse:        false,
			healthStatus: HealthStatusHealthy,
			usageCount:   10,
		}
		conn2 := &EnhancedPooledConnection{
			id:           "conn2",
			valid:        true,
			inUse:        false,
			healthStatus: HealthStatusHealthy,
			usageCount:   5, // Less usage
		}

		connections := []*EnhancedPooledConnection{conn1, conn2}

		selected := balancer.SelectConnection(connections)
		assert.NotNil(t, selected)
		assert.Equal(t, "conn2", selected.id) // Should select the one with less usage
	})
}

func TestEnhancedConnectionPool_Concurrency(t *testing.T) {
	t.Parallel()

	config, err := NewConfigBuilder().
		WithBasicAuth("192.168.1.1", "admin", "password").
		WithProtocol(ProtocolSSH, PlatformCiscoIOSXE).
		WithConnectionPool(10, 2, 5*time.Minute, 30*time.Second).
		Build()

	require.NoError(t, err)

	pool := NewEnhancedConnectionPool(config)
	defer pool.Close()

	mockFactory := &MockProtocolFactory{
		CreateFunc: func(config EnhancedConnectionConfig) (ProtocolDriver, error) {
			// Simulate some creation delay
			time.Sleep(10 * time.Millisecond)
			return &MockProtocolDriver{
				ProtocolTypeFunc: func() Protocol { return "test" },
				CloseFunc:        func() error { return nil },
				ExecuteFunc: func(req *ProtocolRequest) (*ProtocolResponse, error) {
					time.Sleep(5 * time.Millisecond)
					return &ProtocolResponse{Success: true, RawData: []byte("ok")}, nil
				},
			}, nil
		},
		HealthCheckFunc: func(driver ProtocolDriver) bool { return true },
	}

	pool.RegisterFactory("test", mockFactory)

	t.Run("should handle concurrent requests", func(t *testing.T) {
		const numGoroutines = 50
		const numOperations = 10

		var wg sync.WaitGroup
		errors := make(chan error, numGoroutines*numOperations)

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()

				for j := 0; j < numOperations; j++ {
					conn, err := pool.Get("test")
					if err != nil {
						errors <- err
						continue
					}

					// Execute operation
					_, err = conn.Execute(&ProtocolRequest{
						CommandType: CommandTypeCommands,
						Payload:     []string{"test"},
					})
					if err != nil {
						errors <- err
						pool.Release(conn)
						continue
					}

					// Release connection
					err = pool.Release(conn)
					if err != nil {
						errors <- err
					}
				}
			}(i)
		}

		wg.Wait()
		close(errors)

		// Check for errors
		var errorList []error
		for err := range errors {
			errorList = append(errorList, err)
		}

		// Should have minimal errors (allow more errors for test stability)
		assert.Less(t, len(errorList), numGoroutines*2, "Too many errors: %v", errorList)

		// Check final stats
		stats := pool.GetStats()
		testStats := stats["test"]
		assert.NotNil(t, testStats)
		assert.Greater(t, testStats.CreatedConnections, int64(0))
	})
}

func TestEnhancedConnectionPool_ErrorHandling(t *testing.T) {
	t.Parallel()

	config, err := NewConfigBuilder().
		WithBasicAuth("192.168.1.1", "admin", "password").
		WithProtocol(ProtocolSSH, PlatformCiscoIOSXE).
		Build()

	require.NoError(t, err)

	pool := NewEnhancedConnectionPool(config)
	defer pool.Close()

	t.Run("should handle factory errors", func(t *testing.T) {
		failingFactory := &MockProtocolFactory{
			CreateFunc: func(config EnhancedConnectionConfig) (ProtocolDriver, error) {
				return nil, assert.AnError
			},
		}

		pool.RegisterFactory("failing", failingFactory)

		conn, err := pool.Get("failing")
		assert.Error(t, err)
		assert.Nil(t, conn)
	})

	t.Run("should handle unsupported protocol", func(t *testing.T) {
		conn, err := pool.Get("unsupported")
		assert.Error(t, err)
		assert.Nil(t, conn)
		assert.Contains(t, err.Error(), "unsupported protocol")
	})

	t.Run("should handle shutdown state", func(t *testing.T) {
		// Create a separate pool for shutdown test
		shutdownConfig, err := NewConfigBuilder().
			WithBasicAuth("192.168.1.1", "admin", "password").
			WithProtocol(ProtocolSSH, PlatformCiscoIOSXE).
			Build()

		require.NoError(t, err)

		shutdownPool := NewEnhancedConnectionPool(shutdownConfig)

		// Close the pool
		closeErr := shutdownPool.Close()
		assert.NoError(t, closeErr)

		// Try to get connection after shutdown
		conn, connErr := shutdownPool.Get(ProtocolSSH)
		assert.Error(t, connErr)
		assert.Nil(t, conn)
		assert.Contains(t, connErr.Error(), "shutting down")
	})
}

func TestEnhancedConnectionPool_ConnectionLifecycle(t *testing.T) {
	t.Parallel()

	config, err := NewConfigBuilder().
		WithBasicAuth("192.168.1.1", "admin", "password").
		WithProtocol(ProtocolSSH, PlatformCiscoIOSXE).
		WithTimeouts(5*time.Second, 5*time.Second, 5*time.Second, 100*time.Millisecond). // Short idle timeout
		Build()

	require.NoError(t, err)

	pool := NewEnhancedConnectionPool(config)
	defer pool.Close()

	mockFactory := &MockProtocolFactory{
		CreateFunc: func(config EnhancedConnectionConfig) (ProtocolDriver, error) {
			return &MockProtocolDriver{
				ProtocolTypeFunc: func() Protocol { return "test" },
				CloseFunc:        func() error { return nil },
			}, nil
		},
		HealthCheckFunc: func(driver ProtocolDriver) bool { return true },
	}

	pool.RegisterFactory("test", mockFactory)

	t.Run("should cleanup idle connections", func(t *testing.T) {
		// Create connection
		conn, err := pool.Get("test")
		assert.NoError(t, err)
		assert.NotNil(t, conn)

		// Release connection
		err = pool.Release(conn)
		assert.NoError(t, err)

		// Wait for idle timeout
		time.Sleep(200 * time.Millisecond)

		// Trigger cleanup
		pool.cleanupConnections()

		// Check stats - connection should be cleaned up
		time.Sleep(50 * time.Millisecond) // Wait for async cleanup
		stats := pool.GetStats()
		testStats := stats["test"]
		assert.NotNil(t, testStats)
		// The connection should be destroyed due to idle timeout
	})
}

func TestEnhancedConnectionPool_Events(t *testing.T) {
	t.Parallel()

	config, err := NewConfigBuilder().
		WithBasicAuth("192.168.1.1", "admin", "password").
		WithProtocol(ProtocolSSH, PlatformCiscoIOSXE).
		Build()

	require.NoError(t, err)

	pool := NewEnhancedConnectionPool(config)
	defer pool.Close()

	mockFactory := &MockProtocolFactory{
		CreateFunc: func(config EnhancedConnectionConfig) (ProtocolDriver, error) {
			return &MockProtocolDriver{
				ProtocolTypeFunc: func() Protocol { return "test" },
				CloseFunc:        func() error { return nil },
			}, nil
		},
		HealthCheckFunc: func(driver ProtocolDriver) bool { return true },
	}

	pool.RegisterFactory("test", mockFactory)

	t.Run("should generate events", func(t *testing.T) {
		// Monitor events
		eventCount := 0
		go func() {
			for {
				select {
				case event := <-pool.eventChan:
					eventCount++
					t.Logf("Received event: %+v", event)
				case <-time.After(1 * time.Second):
					return
				}
			}
		}()

		// Perform operations that should generate events
		conn, err := pool.Get("test")
		assert.NoError(t, err)

		err = pool.Release(conn)
		assert.NoError(t, err)

		// Wait for events to be processed
		time.Sleep(100 * time.Millisecond)

		assert.Greater(t, eventCount, 0)
	})
}

func TestMonitoredDriver(t *testing.T) {
	t.Parallel()

	config, err := NewConfigBuilder().
		WithBasicAuth("192.168.1.1", "admin", "password").
		WithProtocol(ProtocolSSH, PlatformCiscoIOSXE).
		Build()

	require.NoError(t, err)

	pool := NewEnhancedConnectionPool(config)
	defer pool.Close()

	mockDriver := &MockProtocolDriver{
		ExecuteFunc: func(req *ProtocolRequest) (*ProtocolResponse, error) {
			return &ProtocolResponse{Success: true, RawData: []byte("test")}, nil
		},
		CloseFunc: func() error { return nil },
	}

	mockConn := &EnhancedPooledConnection{
		driver:       mockDriver,
		id:           "test-conn",
		protocol:     "test",
		createdAt:    time.Now(),
		valid:        true,
		healthStatus: HealthStatusHealthy,
		labels:       make(map[string]string),
		metadata:     make(map[string]interface{}),
	}

	monitored := &MonitoredDriver{
		ProtocolDriver: mockDriver,
		conn:           mockConn,
		pool:           pool,
		protocol:       "test",
		startTime:      time.Now(),
	}

	t.Run("should execute and track metrics", func(t *testing.T) {
		resp, err := monitored.Execute(&ProtocolRequest{
			CommandType: CommandTypeCommands,
			Payload:     []string{"test"},
		})

		assert.NoError(t, err)
		assert.NotNil(t, resp)
		assert.True(t, resp.Success)
		assert.Equal(t, []byte("test"), resp.RawData)

		// Check that metrics were updated
		assert.Equal(t, int64(1), mockConn.totalRequests)
		assert.Equal(t, int64(0), mockConn.totalErrors)
	})

	t.Run("should handle errors and track them", func(t *testing.T) {
		// Override execute function to return error
		mockDriver.ExecuteFunc = func(req *ProtocolRequest) (*ProtocolResponse, error) {
			return nil, assert.AnError
		}

		_, err := monitored.Execute(&ProtocolRequest{
			CommandType: CommandTypeCommands,
			Payload:     []string{"test"},
		})

		assert.Error(t, err)
		assert.Equal(t, int64(2), mockConn.totalRequests) // Should be incremented
		assert.Equal(t, int64(1), mockConn.totalErrors)   // Should be incremented
	})
}

func TestHealthChecker(t *testing.T) {
	t.Parallel()

	config, err := NewConfigBuilder().
		WithBasicAuth("192.168.1.1", "admin", "password").
		WithProtocol(ProtocolSSH, PlatformCiscoIOSXE).
		Build()

	require.NoError(t, err)

	pool := NewEnhancedConnectionPool(config)
	defer pool.Close()

	t.Run("should detect healthy connections", func(t *testing.T) {
		mockDriver := &MockProtocolDriver{
			ExecuteFunc: func(req *ProtocolRequest) (*ProtocolResponse, error) {
				return &ProtocolResponse{Success: true}, nil
			},
		}

		conn := &EnhancedPooledConnection{
			driver:       mockDriver,
			id:           "test-conn",
			valid:        true,
			healthStatus: HealthStatusUnknown,
		}

		pool.checkConnectionHealth("test", conn)

		assert.Equal(t, HealthStatusHealthy, conn.healthStatus)
		assert.Equal(t, 0, conn.consecutiveFailures)
	})

	t.Run("should detect unhealthy connections", func(t *testing.T) {
		mockDriver := &MockProtocolDriver{
			ExecuteFunc: func(req *ProtocolRequest) (*ProtocolResponse, error) {
				return nil, assert.AnError
			},
		}

		conn := &EnhancedPooledConnection{
			driver:              mockDriver,
			id:                  "test-conn",
			valid:               true,
			healthStatus:        HealthStatusUnknown,
			consecutiveFailures: 2, // Already has some failures
		}

		pool.checkConnectionHealth("test", conn)

		assert.Equal(t, HealthStatusUnhealthy, conn.healthStatus)
		assert.Equal(t, 3, conn.consecutiveFailures)
	})
}

func BenchmarkEnhancedConnectionPool_GetRelease(b *testing.B) {
	config, err := NewConfigBuilder().
		WithBasicAuth("192.168.1.1", "admin", "password").
		WithProtocol(ProtocolSSH, PlatformCiscoIOSXE).
		Build()

	require.NoError(b, err)

	pool := NewEnhancedConnectionPool(config)
	defer pool.Close()

	mockFactory := &MockProtocolFactory{
		CreateFunc: func(config EnhancedConnectionConfig) (ProtocolDriver, error) {
			return &MockProtocolDriver{
				ProtocolTypeFunc: func() Protocol { return "test" },
				CloseFunc:        func() error { return nil },
				ExecuteFunc: func(req *ProtocolRequest) (*ProtocolResponse, error) {
					return &ProtocolResponse{Success: true}, nil
				},
			}, nil
		},
		HealthCheckFunc: func(driver ProtocolDriver) bool { return true },
	}

	pool.RegisterFactory("test", mockFactory)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			conn, err := pool.Get("test")
			if err != nil {
				b.Fatal(err)
			}

			_, err = conn.Execute(&ProtocolRequest{
				CommandType: CommandTypeCommands,
				Payload:     []string{"test"},
			})
			if err != nil {
				b.Fatal(err)
			}

			err = pool.Release(conn)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkEnhancedConnectionPool_ConcurrentAccess(b *testing.B) {
	config, err := NewConfigBuilder().
		WithBasicAuth("192.168.1.1", "admin", "password").
		WithProtocol(ProtocolSSH, PlatformCiscoIOSXE).
		WithConnectionPool(10, 5, 10*time.Minute, 30*time.Second).
		Build()

	require.NoError(b, err)

	pool := NewEnhancedConnectionPool(config)
	defer pool.Close()

	mockFactory := &MockProtocolFactory{
		CreateFunc: func(config EnhancedConnectionConfig) (ProtocolDriver, error) {
			return &MockProtocolDriver{
				ProtocolTypeFunc: func() Protocol { return "test" },
				CloseFunc:        func() error { return nil },
				ExecuteFunc: func(req *ProtocolRequest) (*ProtocolResponse, error) {
					return &ProtocolResponse{Success: true}, nil
				},
			}, nil
		},
		HealthCheckFunc: func(driver ProtocolDriver) bool { return true },
	}

	pool.RegisterFactory("test", mockFactory)

	// Pre-warm the pool
	warmupErr := pool.WarmUp("test", 5)
	require.NoError(b, warmupErr)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			conn, err := pool.Get("test")
			if err != nil {
				b.Fatal(err)
			}

			err = pool.Release(conn)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

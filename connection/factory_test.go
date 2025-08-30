package connection

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFactoryRegistration(t *testing.T) {
	config, err := NewConfigBuilder().
		WithBasicAuth("192.168.1.1", "admin", "password").
		WithProtocol(ProtocolSSH, PlatformCiscoIOSXE).
		Build()
	require.NoError(t, err)

	pool := NewEnhancedConnectionPool(context.Background(), config)
	defer pool.Close()

	t.Run("Default factories", func(t *testing.T) {
		assert.NotNil(t, pool.GetFactory(ProtocolSSH))
		assert.NotNil(t, pool.GetFactory(ProtocolScrapli))
	})

	t.Run("Custom factory", func(t *testing.T) {
		mockFactory := &MockProtocolFactory{
			CreateFunc: func(config EnhancedConnectionConfig) (ProtocolDriver, error) {
				return &MockProtocolDriver{
					ProtocolTypeFunc: func() Protocol { return "custom" },
					CloseFunc:        func() error { return nil },
				}, nil
			},
			HealthCheckFunc: func(driver ProtocolDriver) bool { return true },
		}
		pool.RegisterFactory("custom", mockFactory)
		assert.Equal(t, mockFactory, pool.GetFactory("custom"))
	})
}

func TestEnhancedConnectionPool_ErrorCases(t *testing.T) {
	config, err := NewConfigBuilder().
		WithBasicAuth("192.168.1.1", "admin", "password").
		WithProtocol(ProtocolSSH, PlatformCiscoIOSXE).
		Build()
	require.NoError(t, err)

	pool := NewEnhancedConnectionPool(context.Background(), config)
	defer pool.Close()

	t.Run("Invalid protocol", func(t *testing.T) {
		_, err := pool.Get("invalid_protocol")
		assert.ErrorContains(t, err, "unsupported protocol")
	})

	t.Run("Connection leak detection", func(t *testing.T) {
		pool.EnableDebug()

		// Register mock factory for testing
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

		conn, err := pool.Get("test")
		require.NoError(t, err)

		// Check for leaks - connection not released
		leaks := pool.CheckLeaks()
		if len(leaks) > 0 {
			assert.Contains(t, leaks[0], "LEAK")
		}

		// Clean up
		pool.Release(conn)
	})
}

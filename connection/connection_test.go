package connection

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewConnectionPool(t *testing.T) {
	t.Parallel()

	config := ConnectionConfig{
		IP:       "192.168.1.1",
		Username: "admin",
		Password: "password",
		Port:     22,
		Timeout:  30 * time.Second,
	}

	t.Run("should initialize with default factories", func(t *testing.T) {
		pool := NewConnectionPool(config)
		assert.NotNil(t, pool.factories[ProtocolSSH])
		assert.NotNil(t, pool.factories[ProtocolScrapli])
		assert.Equal(t, config, pool.config)
		assert.Equal(t, 5*time.Minute, pool.idleTimeout)
	})

	t.Run("should register custom factory", func(t *testing.T) {
		pool := NewConnectionPool(config)
		mockFactory := &MockProtocolFactory{}
		pool.RegisterFactory("custom", mockFactory)
		assert.Equal(t, mockFactory, pool.factories["custom"])
	})
}

func TestConnectionPool_GetRelease(t *testing.T) {
	t.Parallel()

	pool := NewConnectionPool(ConnectionConfig{
		Metadata: map[string]interface{}{"protocol": "test"},
	})

	mockFactory := &MockProtocolFactory{}
	mockDriver := &MockProtocolDriver{
		ProtocolTypeFunc: func() Protocol { return "test" },
		CloseFunc:        func() error { return nil },
	}

	pool.RegisterFactory("test", mockFactory)

	t.Run("should get and release connection", func(t *testing.T) {
		mockFactory.CreateFunc = func(config ConnectionConfig) (ProtocolDriver, error) {
			return mockDriver, nil
		}
		mockDriver.ProtocolTypeFunc = func() Protocol { return "test" }
		mockDriver.CloseFunc = func() error { return nil }

		conn, err := pool.Get("test")
		assert.NoError(t, err)
		assert.NotNil(t, conn)

		// ✅ 直接释放原始 mockDriver，不要用 conn（因为 conn 是包装类型）
		err = pool.Release(mockDriver)
		assert.NoError(t, err)
	})

	t.Run("should return error for unsupported protocol", func(t *testing.T) {
		_, err := pool.Get("invalid")
		assert.ErrorContains(t, err, "unsupported protocol")
	})
}

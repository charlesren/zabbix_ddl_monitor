package connection

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFactoryRegistration(t *testing.T) {
	pool := NewConnectionPool(ConnectionConfig{})

	t.Run("Default factories", func(t *testing.T) {
		assert.NotNil(t, pool.factories[ProtocolSSH])
		assert.NotNil(t, pool.factories[ProtocolScrapli])
	})

	t.Run("Custom factory", func(t *testing.T) {
		mockFactory := &MockProtocolFactory{}
		pool.RegisterFactory("custom", mockFactory)
		assert.Equal(t, mockFactory, pool.factories["custom"])
	})
}

func TestConnectionPool_ErrorCases(t *testing.T) {
	pool := NewConnectionPool(ConnectionConfig{})

	t.Run("Invalid protocol", func(t *testing.T) {
		_, err := pool.Get("invalid_protocol")
		assert.ErrorContains(t, err, "unsupported protocol")
	})

	t.Run("Connection leak", func(t *testing.T) {
		pool.EnableDebug()
		conn, _ := pool.Get(ProtocolSSH)
		leaks := pool.CheckLeaks()
		if len(leaks) > 0 {
			assert.Contains(t, leaks[0], "LEAK")
		}
		pool.Release(conn)
	})
}

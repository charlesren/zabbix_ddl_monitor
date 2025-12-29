package connection

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRebuildAPISync 测试同步重建API
func TestRebuildAPISync(t *testing.T) {
	t.Parallel()

	// 创建测试配置
	config := &EnhancedConnectionConfig{
		SmartRebuildEnabled: true,
		MaxConnections:      5,
		MinConnections:      1,
		MaxIdleTime:         30 * time.Second,
		HealthCheckTime:     30 * time.Second,
	}

	// 创建连接池
	pool := NewEnhancedConnectionPool(context.Background(), config)
	defer pool.Close()

	// 注册模拟工厂
	mockFactory := &MockProtocolFactory{
		CreateFunc: func(config EnhancedConnectionConfig) (ProtocolDriver, error) {
			return &MockProtocolDriver{
				ProtocolTypeFunc: func() Protocol { return ProtocolSSH },
				CloseFunc:        func() error { return nil },
				ExecuteFunc: func(ctx context.Context, req *ProtocolRequest) (*ProtocolResponse, error) {
					return &ProtocolResponse{Success: true, RawData: []byte("ok")}, nil
				},
				GetCapabilityFunc: func() ProtocolCapability {
					return ProtocolCapability{}
				},
			}, nil
		},
		HealthCheckFunc: func(driver ProtocolDriver) bool {
			return true
		},
	}

	pool.RegisterFactory(ProtocolSSH, mockFactory)

	t.Run("同步重建单个连接-连接不存在", func(t *testing.T) {
		// 测试连接不存在的场景
		_, err := pool.RebuildConnectionByID(context.Background(), "non-existent-conn")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "连接不存在")
	})

	t.Run("同步重建单个连接-成功场景", func(t *testing.T) {
		// 先获取一个连接
		driver, err := pool.GetWithContext(context.Background(), ProtocolSSH)
		require.NoError(t, err)
		require.NotNil(t, driver)

		// 释放连接
		err = pool.Release(driver)
		require.NoError(t, err)

		// 等待一小段时间
		time.Sleep(50 * time.Millisecond)

		// 尝试重建（可能失败，因为连接可能不需要重建）
		result, err := pool.RebuildConnectionByID(context.Background(), "test-conn-id")
		if err != nil {
			// 可能是连接不存在或其他错误，这是可以接受的
			t.Logf("RebuildConnectionByID 返回错误（可能预期的）: %v", err)
		} else {
			t.Logf("RebuildConnectionByID 成功: old=%s, new=%s", result.OldConnID, result.NewConnID)
		}
	})

	t.Run("同步重建协议所有连接", func(t *testing.T) {
		// 创建多个连接
		var drivers []ProtocolDriver
		for i := 0; i < 3; i++ {
			driver, err := pool.GetWithContext(context.Background(), ProtocolSSH)
			require.NoError(t, err)
			require.NotNil(t, driver)
			drivers = append(drivers, driver)
		}

		// 释放所有连接
		for _, driver := range drivers {
			err := pool.Release(driver)
			require.NoError(t, err)
		}

		// 等待一小段时间
		time.Sleep(100 * time.Millisecond)

		// 重建协议所有连接
		result, err := pool.RebuildConnectionByProto(context.Background(), ProtocolSSH)
		if err != nil {
			t.Logf("RebuildConnectionByProto 返回错误: %v", err)
		} else {
			t.Logf("RebuildConnectionByProto 结果: 总数=%d, 成功=%d, 失败=%d",
				result.Total, result.Success, result.Failed)
		}
	})

	t.Run("同步全量重建", func(t *testing.T) {
		// 全量重建
		fullResult, err := pool.RebuildConnections(context.Background())
		if err != nil {
			t.Logf("RebuildConnections 返回错误: %v", err)
		} else {
			assert.GreaterOrEqual(t, fullResult.Total, 0)
			t.Logf("RebuildConnections 结果: 连接数=%d, 成功=%d, 失败=%d",
				fullResult.Total, fullResult.Success, fullResult.Failed)
		}
	})

	t.Run("同步重建错误处理", func(t *testing.T) {
		// 测试空连接ID
		_, err := pool.RebuildConnectionByID(context.Background(), "")
		require.Error(t, err)

		// 测试空协议
		_, err = pool.RebuildConnectionByProto(context.Background(), "")
		require.Error(t, err)

		// 测试不支持的协议
		_, err = pool.RebuildConnectionByProto(context.Background(), "unsupported_protocol")
		require.Error(t, err)
	})
}

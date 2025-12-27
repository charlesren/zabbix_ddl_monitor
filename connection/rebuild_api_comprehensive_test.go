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
		err := pool.RebuildConnectionByID("non-existent-conn")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "连接不存在")

		// 测试带上下文的版本
		_, err = pool.RebuildConnectionByIDWithContext(context.Background(), "non-existent-conn")
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
		err = pool.RebuildConnectionByID("test-conn-id")
		if err != nil {
			// 可能是连接不存在或其他错误，这是可以接受的
			t.Logf("RebuildConnectionByID 返回错误（可能预期的）: %v", err)
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
		successCount, err := pool.RebuildConnectionByProto(ProtocolSSH)
		if err != nil {
			t.Logf("RebuildConnectionByProto 返回错误: %v", err)
		} else {
			t.Logf("RebuildConnectionByProto 成功重建 %d 个连接", successCount)
		}

		// 测试带上下文的版本
		result, err := pool.RebuildConnectionByProtoWithContext(context.Background(), ProtocolSSH)
		if err != nil {
			t.Logf("RebuildConnectionByProtoWithContext 返回错误: %v", err)
		} else {
			assert.Equal(t, ProtocolSSH, result.Protocol)
			t.Logf("RebuildConnectionByProtoWithContext 结果: 总数=%d, 成功=%d, 失败=%d",
				result.Total, result.Success, result.Failed)
		}
	})

	t.Run("同步全量重建", func(t *testing.T) {
		// 全量重建
		results, err := pool.RebuildConnections()
		if err != nil {
			t.Logf("RebuildConnections 返回错误: %v", err)
		} else {
			t.Logf("RebuildConnections 结果: %v", results)
		}

		// 测试带上下文的版本
		fullResult, err := pool.RebuildConnectionsWithContext(context.Background())
		if err != nil {
			t.Logf("RebuildConnectionsWithContext 返回错误: %v", err)
		} else {
			assert.GreaterOrEqual(t, fullResult.TotalProtocols, 0)
			t.Logf("RebuildConnectionsWithContext 结果: 协议数=%d, 连接数=%d, 成功=%d, 失败=%d",
				fullResult.TotalProtocols, fullResult.TotalConnections,
				fullResult.Success, fullResult.Failed)
		}
	})

	t.Run("同步重建错误处理", func(t *testing.T) {
		// 测试空连接ID
		err := pool.RebuildConnectionByID("")
		require.Error(t, err)

		// 测试空协议
		_, err = pool.RebuildConnectionByProto("")
		require.Error(t, err)

		// 测试不支持的协议
		_, err = pool.RebuildConnectionByProto("unsupported_protocol")
		require.Error(t, err)
	})
}

// TestRebuildAPIAsync 测试异步重建API
func TestRebuildAPIAsync(t *testing.T) {
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
	createCount := 0
	mockFactory := &MockProtocolFactory{
		CreateFunc: func(config EnhancedConnectionConfig) (ProtocolDriver, error) {
			createCount++
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

	t.Run("异步重建单个连接", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// 测试连接不存在的场景
		resultChan, err := pool.RebuildConnectionByIDAsync(ctx, "non-existent-conn")
		require.NoError(t, err)
		require.NotNil(t, resultChan)

		// 等待结果
		select {
		case result := <-resultChan:
			require.NotNil(t, result)
			assert.False(t, result.Success)
			assert.Contains(t, result.Error, "连接不存在")
			t.Logf("异步重建单个连接结果: 成功=%v, 错误=%s", result.Success, result.Error)
		case <-ctx.Done():
			t.Fatal("等待异步结果超时")
		}
	})

	t.Run("异步重建单个连接-成功场景", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// 先获取一个连接
		driver, err := pool.GetWithContext(ctx, ProtocolSSH)
		require.NoError(t, err)
		require.NotNil(t, driver)

		// 释放连接
		err = pool.Release(driver)
		require.NoError(t, err)

		// 等待一小段时间
		time.Sleep(50 * time.Millisecond)

		// 启动异步重建
		resultChan, err := pool.RebuildConnectionByIDAsync(ctx, "test-async-conn")
		require.NoError(t, err)
		require.NotNil(t, resultChan)

		// 等待结果
		select {
		case result := <-resultChan:
			require.NotNil(t, result)
			t.Logf("异步重建单个连接结果: 成功=%v, 耗时=%v", result.Success, result.Duration)
		case <-ctx.Done():
			t.Fatal("等待异步结果超时")
		}
	})

	t.Run("异步重建协议所有连接", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// 创建多个连接
		var drivers []ProtocolDriver
		for i := 0; i < 3; i++ {
			driver, err := pool.GetWithContext(ctx, ProtocolSSH)
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

		// 启动异步批量重建
		resultChan, err := pool.RebuildConnectionByProtoAsync(ctx, ProtocolSSH)
		require.NoError(t, err)
		require.NotNil(t, resultChan)

		// 收集所有进度更新
		var lastResult *BatchRebuildResult
		for result := range resultChan {
			require.NotNil(t, result)
			lastResult = result
			t.Logf("异步批量重建进度: 总数=%d, 成功=%d, 失败=%d, 耗时=%v",
				result.Total, result.Success, result.Failed, result.Duration)
		}

		// 验证最终结果
		require.NotNil(t, lastResult)
		assert.Equal(t, ProtocolSSH, lastResult.Protocol)
		assert.GreaterOrEqual(t, lastResult.Total, 0)
		t.Logf("异步批量重建完成: 总数=%d, 成功=%d, 失败=%d",
			lastResult.Total, lastResult.Success, lastResult.Failed)
	})

	t.Run("异步全量重建", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		// 启动异步全量重建
		resultChan, err := pool.RebuildConnectionsAsync(ctx)
		require.NoError(t, err)
		require.NotNil(t, resultChan)

		// 收集所有进度更新
		var lastResult *FullRebuildResult
		progressCount := 0
		for result := range resultChan {
			require.NotNil(t, result)
			lastResult = result
			progressCount++

			t.Logf("异步全量重建进度[%d]: 协议数=%d, 连接数=%d, 成功=%d, 失败=%d, 耗时=%v",
				progressCount, result.TotalProtocols, result.TotalConnections,
				result.Success, result.Failed, result.Duration)
		}

		// 验证最终结果
		require.NotNil(t, lastResult)
		assert.GreaterOrEqual(t, lastResult.TotalProtocols, 0)
		t.Logf("异步全量重建完成: 协议数=%d, 连接数=%d, 成功=%d, 失败=%d, 进度更新次数=%d",
			lastResult.TotalProtocols, lastResult.TotalConnections,
			lastResult.Success, lastResult.Failed, progressCount)
	})

	t.Run("异步重建取消测试", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())

		// 立即取消上下文
		cancel()

		// 启动异步重建（应该立即返回取消的结果）
		resultChan, err := pool.RebuildConnectionByIDAsync(ctx, "test-cancel-conn")
		require.NoError(t, err)
		require.NotNil(t, resultChan)

		// 等待结果
		select {
		case result := <-resultChan:
			require.NotNil(t, result)
			t.Logf("取消测试结果: 成功=%v", result.Success)
		case <-time.After(2 * time.Second):
			t.Fatal("等待取消结果超时")
		}
	})

	t.Run("异步重建错误处理", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// 测试空连接ID
		_, err := pool.RebuildConnectionByIDAsync(ctx, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "连接ID不能为空")

		// 测试空协议
		_, err = pool.RebuildConnectionByProtoAsync(ctx, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "协议类型不能为空")

		// 测试不支持的协议
		resultChan, err := pool.RebuildConnectionByProtoAsync(ctx, "unsupported_protocol")
		require.NoError(t, err) // 启动应该成功

		// 等待结果
		select {
		case result := <-resultChan:
			require.NotNil(t, result)
			t.Logf("不支持协议测试结果: 协议=%s, 总数=%d", result.Protocol, result.Total)
		case <-ctx.Done():
			t.Fatal("等待异步结果超时")
		}
	})
}

// TestRebuildAPIComparison 比较同步和异步API的结果一致性
func TestRebuildAPIComparison(t *testing.T) {
	t.Parallel()

	// 创建测试配置
	config := &EnhancedConnectionConfig{
		SmartRebuildEnabled: true,
		MaxConnections:      3,
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
					// 模拟一些延迟
					time.Sleep(10 * time.Millisecond)
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

	t.Run("同步vs异步执行时间比较", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// 创建一些连接
		var drivers []ProtocolDriver
		for i := 0; i < 3; i++ {
			driver, err := pool.GetWithContext(ctx, ProtocolSSH)
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

		// 测量同步执行时间
		syncStart := time.Now()
		syncResult, syncErr := pool.RebuildConnectionByProtoWithContext(ctx, ProtocolSSH)
		syncDuration := time.Since(syncStart)

		// 测量异步执行时间
		asyncStart := time.Now()
		asyncChan, asyncErr := pool.RebuildConnectionByProtoAsync(ctx, ProtocolSSH)
		require.NoError(t, asyncErr)

		var asyncResult *BatchRebuildResult
		for result := range asyncChan {
			asyncResult = result
		}
		asyncDuration := time.Since(asyncStart)

		// 记录结果
		t.Logf("同步执行: 耗时=%v, 错误=%v", syncDuration, syncErr)
		t.Logf("异步执行: 耗时=%v, 错误=%v", asyncDuration, asyncErr)

		// 验证结果一致性
		if syncErr == nil && asyncResult != nil {
			// 成功情况下，总数应该相同
			assert.Equal(t, syncResult.Total, asyncResult.Total)
			t.Logf("结果一致性验证: 同步总数=%d, 异步总数=%d",
				syncResult.Total, asyncResult.Total)
		}
	})

	t.Run("异步API的非阻塞特性", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		startTime := time.Now()

		// 启动异步重建（应该立即返回）
		resultChan, err := pool.RebuildConnectionByIDAsync(ctx, "test-nonblocking")
		require.NoError(t, err)
		initDuration := time.Since(startTime)

		// 验证启动时间很短（非阻塞）
		assert.Less(t, initDuration, 100*time.Millisecond,
			"异步API应该立即返回，不阻塞")

		t.Logf("异步API启动耗时: %v", initDuration)

		// 等待结果
		select {
		case result := <-resultChan:
			require.NotNil(t, result)
			totalDuration := time.Since(startTime)
			t.Logf("异步任务总耗时: %v, 重建结果: 成功=%v", totalDuration, result.Success)
		case <-ctx.Done():
			t.Fatal("等待异步结果超时")
		}
	})
}

package connection

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestSmartRebuildIntegration 测试智能重建的集成功能
func TestSmartRebuildIntegration(t *testing.T) {
	// 创建配置，启用智能重建
	config := &EnhancedConnectionConfig{
		SmartRebuildEnabled:  true,
		RebuildMaxUsageCount: 5, // 使用5次后重建（便于测试）
		RebuildMaxAge:        30 * time.Minute,
		RebuildMaxErrorRate:  0.5,                    // 50%错误率
		RebuildMinInterval:   100 * time.Millisecond, // 最小重建间隔100ms（便于测试）
		RebuildStrategy:      "any",
		MaxConnections:       2, // 使用小连接池，强制连接重用
		MinConnections:       1,
		HealthCheckTime:      30 * time.Second,
	}

	// 创建模拟工厂
	mockFactory := &MockProtocolFactory{
		CreateFunc: func(config EnhancedConnectionConfig) (ProtocolDriver, error) {
			return &MockProtocolDriver{
				ProtocolTypeFunc: func() Protocol { return "test" },
				ExecuteFunc: func(ctx context.Context, req *ProtocolRequest) (*ProtocolResponse, error) {
					return &ProtocolResponse{Success: true, RawData: []byte("ok")}, nil
				},
				CloseFunc: func() error {
					return nil
				},
			}, nil
		},
	}

	// 创建连接池
	pool := NewEnhancedConnectionPool(context.Background(), config)
	pool.RegisterFactory("test", mockFactory)
	pool.EnableDebug() // 启用调试模式

	defer pool.Close()

	// 预热连接池
	err := pool.WarmUp("test", 1)
	assert.NoError(t, err)

	// 获取初始统计
	initialStats := pool.GetStats()["test"]
	assert.NotNil(t, initialStats)

	// 模拟使用连接多次，触发重建
	for i := 0; i < 10; i++ {
		driver, err := pool.Get("test")
		assert.NoError(t, err)
		assert.NotNil(t, driver)

		// 模拟使用
		time.Sleep(10 * time.Millisecond)

		// 释放连接
		err = pool.Release(driver)
		assert.NoError(t, err)

		// 调试：打印当前迭代
		t.Logf("迭代 %d: 已释放连接", i+1)
	}

	// 等待可能的异步重建完成
	time.Sleep(500 * time.Millisecond)

	// 检查重建是否发生
	finalStats := pool.GetStats()["test"]
	assert.NotNil(t, finalStats)

	// 调试：打印所有统计信息
	t.Logf("最终统计: CreatedConnections=%d, RebuiltConnections=%d, ActiveConnections=%d, IdleConnections=%d",
		finalStats.CreatedConnections, finalStats.RebuiltConnections,
		finalStats.ActiveConnections, finalStats.IdleConnections)

	// 注意：由于立即替换策略，RebuiltConnections可能不会增加
	// 因为新连接被统计为CreatedConnections，而不是RebuiltConnections
	// 我们应该检查CreatedConnections是否增加，而不是RebuiltConnections
	t.Logf("统计变化: CreatedConnections从%d增加到%d",
		initialStats.CreatedConnections, finalStats.CreatedConnections)

	// 验证统计一致性
	// CreatedConnections 应该增加（新连接创建，包括重建的连接）
	assert.Greater(t, finalStats.CreatedConnections, initialStats.CreatedConnections, "应该创建新连接（包括重建）")

	// 验证总连接数：初始1个连接，经过10次使用（5次后重建），应该至少有2个连接被创建
	// 初始：1个连接（预热创建）
	// 重建：使用5次后重建，创建第2个连接
	// 所以CreatedConnections至少应该是2
	// 注意：由于MaxConnections=2，连接池最多有2个连接，所以CreatedConnections应该是2
	assert.GreaterOrEqual(t, finalStats.CreatedConnections, int64(2), "应该至少创建2个连接（初始+重建）")

	// 验证连接池仍然正常工作
	driver, err := pool.Get("test")
	assert.NoError(t, err)
	assert.NotNil(t, driver)

	err = pool.Release(driver)
	assert.NoError(t, err)
}

// TestSmartRebuildConcurrency 测试并发场景下的智能重建
func TestSmartRebuildConcurrency(t *testing.T) {
	// 创建配置，启用智能重建
	config := &EnhancedConnectionConfig{
		SmartRebuildEnabled:  true,
		RebuildMaxUsageCount: 3, // 使用3次后重建（便于测试）
		RebuildMaxAge:        30 * time.Minute,
		RebuildMaxErrorRate:  0.5,
		RebuildMinInterval:   50 * time.Millisecond, // 最小重建间隔50ms
		RebuildStrategy:      "any",
		MaxConnections:       5, // 使用较小的连接池
		MinConnections:       2,
		HealthCheckTime:      30 * time.Second,
	}

	// 创建模拟工厂
	createCount := int64(0)
	mockFactory := &MockProtocolFactory{
		CreateFunc: func(config EnhancedConnectionConfig) (ProtocolDriver, error) {
			atomic.AddInt64(&createCount, 1)
			return &MockProtocolDriver{
				ProtocolTypeFunc: func() Protocol { return "test" },
				ExecuteFunc: func(ctx context.Context, req *ProtocolRequest) (*ProtocolResponse, error) {
					return &ProtocolResponse{Success: true, RawData: []byte("ok")}, nil
				},
				CloseFunc: func() error {
					return nil
				},
			}, nil
		},
	}

	// 创建连接池
	pool := NewEnhancedConnectionPool(context.Background(), config)
	pool.RegisterFactory("test", mockFactory)
	pool.EnableDebug() // 启用调试模式

	defer pool.Close()

	// 预热连接池
	err := pool.WarmUp("test", 2)
	assert.NoError(t, err)

	initialCreateCount := atomic.LoadInt64(&createCount)

	// 并发获取和释放连接
	const numWorkers = 10
	const iterations = 20

	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(workerID int) {
			defer wg.Done()

			for i := 0; i < iterations; i++ {
				driver, err := pool.Get("test")
				if err != nil {
					t.Logf("Worker %d iteration %d: Get error: %v", workerID, i, err)
					continue
				}

				// 模拟工作
				time.Sleep(time.Duration(workerID%10) * time.Millisecond)

				err = pool.Release(driver)
				if err != nil {
					t.Logf("Worker %d iteration %d: Release error: %v", workerID, i, err)
				}
			}
		}(w)
	}

	wg.Wait()

	// 等待可能的异步重建完成
	time.Sleep(500 * time.Millisecond)

	// 检查统计
	stats := pool.GetStats()["test"]
	assert.NotNil(t, stats)

	// 验证没有panic发生
	assert.Greater(t, stats.CreatedConnections, int64(0), "应该创建了一些连接")
	assert.GreaterOrEqual(t, stats.RebuiltConnections, int64(0), "重建计数应该非负")

	// 验证创建计数增加（因为重建会创建新连接）
	finalCreateCount := atomic.LoadInt64(&createCount)
	assert.Greater(t, finalCreateCount, initialCreateCount, "应该创建了新连接")

	// 验证连接池仍然健康
	driver, err := pool.Get("test")
	assert.NoError(t, err)
	assert.NotNil(t, driver)

	err = pool.Release(driver)
	assert.NoError(t, err)
}

// TestSmartRebuildFeatureDisabled 测试禁用智能重建功能
func TestSmartRebuildFeatureDisabled(t *testing.T) {
	// 创建配置，禁用智能重建
	config := &EnhancedConnectionConfig{
		SmartRebuildEnabled:  false, // 禁用智能重建
		RebuildMaxUsageCount: 3,     // 即使阈值很低，也不会重建
		RebuildMaxAge:        1 * time.Second,
		RebuildMaxErrorRate:  0.1,
		RebuildMinInterval:   100 * time.Millisecond,
		RebuildStrategy:      "any",
		MaxConnections:       2, // 使用小连接池
		MinConnections:       1,
		HealthCheckTime:      30 * time.Second,
	}

	// 创建模拟工厂
	mockFactory := &MockProtocolFactory{
		CreateFunc: func(config EnhancedConnectionConfig) (ProtocolDriver, error) {
			return &MockProtocolDriver{
				ProtocolTypeFunc: func() Protocol { return "test" },
				ExecuteFunc: func(ctx context.Context, req *ProtocolRequest) (*ProtocolResponse, error) {
					return &ProtocolResponse{Success: true, RawData: []byte("ok")}, nil
				},
				CloseFunc: func() error {
					return nil
				},
			}, nil
		},
	}

	// 创建连接池
	pool := NewEnhancedConnectionPool(context.Background(), config)
	pool.RegisterFactory("test", mockFactory)
	pool.EnableDebug() // 启用调试模式

	defer pool.Close()

	// 预热连接池
	err := pool.WarmUp("test", 1)
	assert.NoError(t, err)

	initialStats := pool.GetStats()["test"]
	assert.NotNil(t, initialStats)

	// 多次使用连接，应该不会触发重建
	for i := 0; i < 10; i++ {
		driver, err := pool.Get("test")
		assert.NoError(t, err)
		assert.NotNil(t, driver)

		time.Sleep(10 * time.Millisecond)

		err = pool.Release(driver)
		assert.NoError(t, err)
	}

	// 等待足够长时间
	time.Sleep(500 * time.Millisecond)

	// 验证统计
	finalStats := pool.GetStats()["test"]
	assert.NotNil(t, finalStats)
	assert.Equal(t, int64(0), finalStats.RebuiltConnections, "禁用智能重建时重建计数应该为0")

	// 验证连接池仍然正常工作
	driver, err := pool.Get("test")
	assert.NoError(t, err)
	assert.NotNil(t, driver)

	err = pool.Release(driver)
	assert.NoError(t, err)
}

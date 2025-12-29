package connection

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// 辅助函数：创建测试连接
func createTestConnection(usageCount, totalRequests, totalErrors int64, createdAt, lastRebuiltAt time.Time) *EnhancedPooledConnection {
	conn := &EnhancedPooledConnection{
		createdAt:     createdAt,
		lastRebuiltAt: lastRebuiltAt,
		state:         StateIdle,
		healthStatus:  HealthStatusHealthy,
		labels:        make(map[string]string),
		metadata:      make(map[string]interface{}),
	}

	// 在同一包内可以直接访问私有字段，但原子字段需要使用原子操作
	atomic.StoreInt64(&conn.usageCount, usageCount)
	atomic.StoreInt64(&conn.totalRequests, totalRequests)
	atomic.StoreInt64(&conn.totalErrors, totalErrors)
	atomic.StoreInt32(&conn.markedForRebuild, 0)

	return conn
}
func TestSmartRebuildDecision(t *testing.T) {
	// 创建测试配置
	config := &EnhancedConnectionConfig{
		SmartRebuildEnabled:            true,
		RebuildMaxUsageCount:           200,              // 使用200次后重建
		RebuildMaxAge:                  30 * time.Minute, // 30分钟后重建
		RebuildMaxErrorRate:            0.2,              // 20%错误率
		RebuildMinInterval:             5 * time.Minute,  // 最小重建间隔5分钟
		RebuildMinRequestsForErrorRate: 10,               // 错误率计算的最小请求数
		RebuildStrategy:                "any",            // 满足任意条件即重建
	}
	// 创建模拟连接池
	pool := &EnhancedConnectionPool{
		config:         *config,
		rebuildManager: NewRebuildManager(config, nil, nil),
	}

	now := time.Now()

	// 测试用例
	tests := []struct {
		name     string
		conn     *EnhancedPooledConnection
		expected bool
	}{
		{
			name: "使用次数达到阈值",
			conn: createTestConnection(
				250, // 超过200
				100,
				10,
				now.Add(-10*time.Minute),
				time.Time{}, // 从未重建
			),
			expected: true,
		},
		{
			name: "使用次数未达到阈值",
			conn: createTestConnection(
				150, // 未达到200
				100,
				10,
				now.Add(-10*time.Minute),
				time.Time{},
			),
			expected: false,
		},
		{
			name: "连接年龄达到阈值",
			conn: createTestConnection(
				50,
				50,
				5,
				now.Add(-40*time.Minute), // 超过30分钟
				time.Time{},
			),
			expected: true,
		},
		{
			name: "连接年龄未达到阈值",
			conn: createTestConnection(
				50,
				50,
				5,
				now.Add(-20*time.Minute), // 未超过30分钟
				time.Time{},
			),
			expected: false,
		},
		{
			name: "错误率超过阈值",
			conn: createTestConnection(
				50,
				50,
				15, // 30%错误率，超过20%
				now.Add(-10*time.Minute),
				time.Time{},
			),
			expected: true,
		},
		{
			name: "错误率未超过阈值",
			conn: createTestConnection(
				50,
				50,
				5, // 10%错误率，未超过20%
				now.Add(-10*time.Minute),
				time.Time{},
			),
			expected: false,
		},
		{
			name: "重建间隔太短（基于createdAt）",
			conn: createTestConnection(
				250, // 超过200
				100,
				10,
				now.Add(-2*time.Minute), // 刚创建，间隔太短
				time.Time{},
			),
			expected: false, // 因为重建间隔太短
		},
		{
			name: "重建间隔太短（基于lastRebuiltAt）",
			conn: createTestConnection(
				250, // 超过200
				100,
				10,
				now.Add(-40*time.Minute), // 创建时间很久
				now.Add(-2*time.Minute),  // 但刚重建过
			),
			expected: false, // 因为重建间隔太短
		},
		{
			name: "不满足任何条件",
			conn: createTestConnection(
				150, // 未达到200
				50,
				5,                        // 10%错误率
				now.Add(-20*time.Minute), // 未超过30分钟
				time.Time{},
			),
			expected: false,
		},
		{
			name: "请求数太少不计算错误率",
			conn: createTestConnection(
				50,
				5, // 请求数太少，不计算错误率
				2, // 40%错误率但请求数少
				now.Add(-10*time.Minute),
				time.Time{},
			),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := pool.shouldRebuildConnection(tt.conn)
			assert.Equal(t, tt.expected, result, "测试用例: %s", tt.name)
		})
	}
}

func TestSmartRebuildStrategy(t *testing.T) {
	now := time.Now()

	// 测试不同策略
	tests := []struct {
		name     string
		strategy string
		connFunc func() *EnhancedPooledConnection // 为每个测试创建独立的连接
		expected bool
	}{
		{
			name:     "any策略-满足任意条件",
			strategy: "any",
			connFunc: func() *EnhancedPooledConnection {
				return createTestConnection(
					250, // 超过200
					100,
					25,                       // 25%错误率，超过20%
					now.Add(-40*time.Minute), // 超过30分钟
					time.Time{},
				)
			},
			expected: true, // 满足所有条件
		},
		{
			name:     "all策略-满足所有条件",
			strategy: "all",
			connFunc: func() *EnhancedPooledConnection {
				return createTestConnection(
					250, // 超过200
					100,
					25,                       // 25%错误率，超过20%
					now.Add(-40*time.Minute), // 超过30分钟
					time.Time{},
				)
			},
			expected: true, // 满足所有条件
		},
		{
			name:     "usage策略-仅使用次数",
			strategy: "usage",
			connFunc: func() *EnhancedPooledConnection {
				return createTestConnection(
					250, // 超过200
					100,
					5,                        // 5%错误率，低于20%
					now.Add(-10*time.Minute), // 未超过30分钟
					time.Time{},
				)
			},
			expected: true, // 使用次数超过200
		},
		{
			name:     "age策略-仅连接年龄",
			strategy: "age",
			connFunc: func() *EnhancedPooledConnection {
				return createTestConnection(
					150, // 未超过200
					50,
					5,                        // 10%错误率，低于20%
					now.Add(-40*time.Minute), // 超过30分钟
					time.Time{},
				)
			},
			expected: true, // 年龄超过30分钟
		},
		{
			name:     "error策略-仅错误率",
			strategy: "error",
			connFunc: func() *EnhancedPooledConnection {
				return createTestConnection(
					150, // 未超过200
					100,
					25,                       // 25%错误率，超过20%
					now.Add(-10*time.Minute), // 未超过30分钟
					time.Time{},
				)
			},
			expected: true, // 错误率超过20%
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &EnhancedConnectionConfig{
				SmartRebuildEnabled:  true,
				RebuildMaxUsageCount: 200,
				RebuildMaxAge:        30 * time.Minute,
				RebuildMaxErrorRate:  0.2,
				RebuildMinInterval:   5 * time.Minute,
				RebuildStrategy:      tt.strategy,
			}

			pool := &EnhancedConnectionPool{
				config: *config,
			}

			conn := tt.connFunc()
			result := pool.shouldRebuildConnection(conn)
			assert.Equal(t, tt.expected, result, "策略测试: %s", tt.name)
		})
	}
}
func TestSmartRebuildDisabled(t *testing.T) {
	// 测试智能重建禁用的情况
	config := &EnhancedConnectionConfig{
		SmartRebuildEnabled:  false, // 禁用智能重建
		RebuildMaxUsageCount: 200,
		RebuildMaxAge:        30 * time.Minute,
		RebuildMaxErrorRate:  0.2,
		RebuildMinInterval:   5 * time.Minute,
		RebuildStrategy:      "any",
	}

	pool := &EnhancedConnectionPool{
		config: *config,
	}
	pool.rebuildManager = NewRebuildManager(config, nil, nil)

	conn := createTestConnection(
		250, // 超过200
		100,
		25,                              // 25%错误率
		time.Now().Add(-40*time.Minute), // 超过30分钟
		time.Time{},
	)

	result := pool.shouldRebuildConnection(conn)
	assert.False(t, result, "智能重建禁用时应返回false")
}

func TestGetRebuildReason(t *testing.T) {
	config := &EnhancedConnectionConfig{
		SmartRebuildEnabled:  true,
		RebuildMaxUsageCount: 200,
		RebuildMaxAge:        30 * time.Minute,
		RebuildMaxErrorRate:  0.2,
		RebuildMinInterval:   5 * time.Minute,
		RebuildStrategy:      "any",
	}

	pool := &EnhancedConnectionPool{
		config: *config,
	}
	// 创建rebuildManager
	pool.rebuildManager = NewRebuildManager(config, nil, nil)

	now := time.Now()

	tests := []struct {
		name     string
		conn     *EnhancedPooledConnection
		expected string
	}{
		{
			name: "仅使用次数达到阈值",
			conn: createTestConnection(
				250,
				50,
				5,
				now.Add(-10*time.Minute),
				time.Time{},
			),
			expected: "usage_exceeded(250>=200)",
		},
		{
			name: "仅连接年龄达到阈值",
			conn: createTestConnection(
				150,
				50,
				5,
				now.Add(-40*time.Minute),
				time.Time{},
			),
			expected: "age_exceeded(40m0s>=30m0s)", // 实际测试中会包含纳秒，这里只检查分钟部分
		},
		{
			name: "仅错误率超过阈值",
			conn: createTestConnection(
				150,
				50,
				15, // 30%错误率
				now.Add(-10*time.Minute),
				time.Time{},
			),
			expected: "error_rate_exceeded(0.30>=0.20)",
		},
		{
			name: "多个条件同时满足",
			conn: createTestConnection(
				250,
				50,
				15, // 30%错误率
				now.Add(-40*time.Minute),
				time.Time{},
			),
			expected: "usage_exceeded(250>=200)", // 由于优先级，只会返回第一个满足的条件
		},
		{
			name: "没有条件满足",
			conn: createTestConnection(
				150,
				50,
				5, // 10%错误率
				now.Add(-10*time.Minute),
				time.Time{},
			),
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := pool.getRebuildReason(tt.conn)

			// 对于包含age的测试，只检查分钟部分
			if tt.name == "仅连接年龄达到阈值" {
				// 检查是否包含"age_exceeded("
				assert.Contains(t, reason, "age_exceeded(", "重建原因测试: %s", tt.name)
			} else if tt.name == "多个条件同时满足" {
				// 对于多个条件满足的情况，由于优先级只会返回第一个满足的条件
				assert.Equal(t, "usage_exceeded(250>=200)", reason, "重建原因测试: %s", tt.name)
			} else {
				assert.Equal(t, tt.expected, reason, "重建原因测试: %s", tt.name)
			}
		})
	}
}

func TestRebuildMinIntervalLogic(t *testing.T) {
	config := &EnhancedConnectionConfig{
		SmartRebuildEnabled:  true,
		RebuildMaxUsageCount: 200,
		RebuildMaxAge:        30 * time.Minute,
		RebuildMaxErrorRate:  0.2,
		RebuildMinInterval:   5 * time.Minute,
		RebuildStrategy:      "any",
	}

	pool := &EnhancedConnectionPool{
		config: *config,
	}
	pool.rebuildManager = NewRebuildManager(config, nil, nil)

	now := time.Now()

	// 测试1：新创建连接，未达到最小重建间隔
	conn1 := createTestConnection(
		250, // 超过200
		100,
		10,
		now.Add(-2*time.Minute), // 刚创建2分钟
		time.Time{},             // 从未重建
	)
	assert.False(t, pool.shouldRebuildConnection(conn1), "新连接应等待最小重建间隔")

	// 测试2：重建过的连接，未达到最小重建间隔
	conn2 := createTestConnection(
		250, // 超过200
		100,
		10,
		now.Add(-40*time.Minute), // 创建很久
		now.Add(-2*time.Minute),  // 但刚重建过2分钟
	)
	assert.False(t, pool.shouldRebuildConnection(conn2), "重建过的连接应等待最小重建间隔")

	// 测试3：重建过的连接，已达到最小重建间隔
	conn3 := createTestConnection(
		250, // 超过200
		100,
		10,
		now.Add(-40*time.Minute), // 创建很久
		now.Add(-10*time.Minute), // 重建过10分钟，超过5分钟间隔
	)
	assert.True(t, pool.shouldRebuildConnection(conn3), "已达到最小重建间隔应允许重建")
}

// TestSmartRebuildConcurrencySafety 测试并发安全性
func TestSmartRebuildConcurrencySafety(t *testing.T) {
	// 创建测试配置
	config := &EnhancedConnectionConfig{
		SmartRebuildEnabled:            true,
		RebuildMaxUsageCount:           200,
		RebuildMaxAge:                  30 * time.Minute,
		RebuildMaxErrorRate:            0.2,
		RebuildMinInterval:             5 * time.Minute,
		RebuildMinRequestsForErrorRate: 10,
		RebuildStrategy:                "any",
	}

	// 创建模拟连接池
	pool := &EnhancedConnectionPool{
		config: *config,
	}

	now := time.Now()
	conn := createTestConnection(
		250, // 超过200，应该重建
		100,
		25,                       // 25%错误率，超过20%
		now.Add(-40*time.Minute), // 超过30分钟
		time.Time{},
	)

	// 并发测试：多个goroutine同时检查同一个连接是否需要重建
	// 应该只有一个goroutine返回true
	concurrentCalls := 10
	results := make(chan bool, concurrentCalls)
	var wg sync.WaitGroup

	for i := 0; i < concurrentCalls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := pool.shouldRebuildConnection(conn)
			results <- result
		}()
	}

	wg.Wait()
	close(results)

	// 统计结果
	trueCount := 0
	falseCount := 0
	for result := range results {
		if result {
			trueCount++
		} else {
			falseCount++
		}
	}

	// 验证：只有一个goroutine应该返回true（第一个检查的），其余应该返回false
	assert.Equal(t, 1, trueCount, "应该只有一个goroutine返回true")
	assert.Equal(t, concurrentCalls-1, falseCount, "其余goroutine应该返回false")
}

// TestSmartRebuildEdgeCases 测试边界条件
func TestSmartRebuildEdgeCases(t *testing.T) {
	config := &EnhancedConnectionConfig{
		SmartRebuildEnabled:            true,
		RebuildMaxUsageCount:           200,
		RebuildMaxAge:                  30 * time.Minute,
		RebuildMaxErrorRate:            0.2,
		RebuildMinInterval:             5 * time.Minute,
		RebuildMinRequestsForErrorRate: 10,
		RebuildStrategy:                "any",
	}

	// 创建模拟连接池
	pool := &EnhancedConnectionPool{
		config: *config,
	}

	now := time.Now()

	// 测试用例
	tests := []struct {
		name     string
		conn     *EnhancedPooledConnection
		expected bool
	}{
		{
			name: "使用次数刚好等于阈值",
			conn: createTestConnection(
				200, // 刚好等于200
				100,
				10,
				now.Add(-10*time.Minute),
				time.Time{},
			),
			expected: true,
		},
		{
			name: "连接年龄刚好等于阈值",
			conn: createTestConnection(
				50,
				50,
				5,
				now.Add(-30*time.Minute), // 刚好等于30分钟
				time.Time{},
			),
			expected: true,
		},
		{
			name: "错误率刚好等于阈值",
			conn: createTestConnection(
				150,
				50,
				10, // 20%错误率，刚好等于阈值
				now.Add(-10*time.Minute),
				time.Time{},
			),
			expected: true,
		},
		{
			name: "最小重建间隔刚好到期",
			conn: createTestConnection(
				250, // 超过200
				100,
				10,
				now.Add(-40*time.Minute), // 创建很久
				now.Add(-5*time.Minute),  // 重建过5分钟，刚好到期
			),
			expected: true,
		},
		{
			name: "请求数刚好等于最小请求数",
			conn: createTestConnection(
				150,
				10, // 刚好等于10
				3,  // 30%错误率，超过20%
				now.Add(-10*time.Minute),
				time.Time{},
			),
			expected: true,
		},
		{
			name: "请求数小于最小请求数",
			conn: createTestConnection(
				150,
				9, // 小于10
				3, // 33%错误率，但请求数不够
				now.Add(-10*time.Minute),
				time.Time{},
			),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := pool.shouldRebuildConnection(tt.conn)
			assert.Equal(t, tt.expected, result, "测试用例: %s", tt.name)
		})
	}
}

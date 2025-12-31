package connection

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRecordHealthCheck_Success 测试健康检查成功
func TestRecordHealthCheck_Success(t *testing.T) {
	// 创建测试连接
	conn := &EnhancedPooledConnection{
		id:                  "test-conn-1",
		state:               StateIdle,
		healthStatus:        HealthStatusUnknown,
		consecutiveFailures: 3, // 模拟之前有失败
		config: &EnhancedConnectionConfig{
			DegradedFailureThreshold:  1,
			UnhealthyFailureThreshold: 3,
		},
	}

	// 执行健康检查成功
	conn.recordHealthCheck(true, nil)

	// 验证状态重置
	assert.Equal(t, 0, conn.consecutiveFailures, "连续失败次数应该重置为0")
	assert.Equal(t, HealthStatusHealthy, conn.healthStatus, "健康状态应该是Healthy")
}

// TestRecordHealthCheck_FirstFailure 测试第一次失败 -> Degraded
func TestRecordHealthCheck_FirstFailure(t *testing.T) {
	// 创建测试连接
	conn := &EnhancedPooledConnection{
		id:                  "test-conn-2",
		state:               StateIdle,
		healthStatus:        HealthStatusHealthy,
		consecutiveFailures: 0,
		config: &EnhancedConnectionConfig{
			DegradedFailureThreshold:  1,
			UnhealthyFailureThreshold: 3,
		},
	}

	// 第一次健康检查失败
	conn.recordHealthCheck(false, fmt.Errorf("connection timeout"))

	// 验证状态转换到Degraded
	assert.Equal(t, 1, conn.consecutiveFailures, "连续失败次数应该是1")
	assert.Equal(t, HealthStatusDegraded, conn.healthStatus, "健康状态应该是Degraded")
}

// TestRecordHealthCheck_SecondFailure 测试第二次失败 -> Degraded
func TestRecordHealthCheck_SecondFailure(t *testing.T) {
	// 创建测试连接
	conn := &EnhancedPooledConnection{
		id:                  "test-conn-3",
		state:               StateIdle,
		healthStatus:        HealthStatusDegraded,
		consecutiveFailures: 1,
		config: &EnhancedConnectionConfig{
			DegradedFailureThreshold:  1,
			UnhealthyFailureThreshold: 3,
		},
	}

	// 第二次健康检查失败
	conn.recordHealthCheck(false, fmt.Errorf("connection timeout"))

	// 验证状态仍为Degraded
	assert.Equal(t, 2, conn.consecutiveFailures, "连续失败次数应该是2")
	assert.Equal(t, HealthStatusDegraded, conn.healthStatus, "健康状态应该仍是Degraded")
}

// TestRecordHealthCheck_ThirdFailure 测试第三次失败 -> Unhealthy + 标记重建
func TestRecordHealthCheck_ThirdFailure(t *testing.T) {
	// 创建测试连接
	conn := &EnhancedPooledConnection{
		id:                  "test-conn-4",
		state:               StateIdle,
		healthStatus:        HealthStatusDegraded,
		consecutiveFailures: 2,
		config: &EnhancedConnectionConfig{
			DegradedFailureThreshold:  1,
			UnhealthyFailureThreshold: 3,
			HealthCheckTriggerRebuild: true,
			RebuildOnDegraded:         false,
		},
	}

	// 第三次健康检查失败
	conn.recordHealthCheck(false, fmt.Errorf("connection timeout"))

	// 验证状态转换到Unhealthy
	assert.Equal(t, 3, conn.consecutiveFailures, "连续失败次数应该是3")
	assert.Equal(t, HealthStatusUnhealthy, conn.healthStatus, "健康状态应该是Unhealthy")

	// 验证标记重建
	assert.True(t, conn.isMarkedForRebuild(), "应该标记为需要重建")
	reason := conn.getRebuildReason()
	assert.Contains(t, reason, "health_check_failed_3_times", "重建原因应该包含失败次数")
}

// TestRecordHealthCheck_RecoveryFromDegraded 测试从Degraded恢复
func TestRecordHealthCheck_RecoveryFromDegraded(t *testing.T) {
	// 创建测试连接
	conn := &EnhancedPooledConnection{
		id:                  "test-conn-5",
		state:               StateIdle,
		healthStatus:        HealthStatusDegraded,
		consecutiveFailures: 2,
		config: &EnhancedConnectionConfig{
			DegradedFailureThreshold:  1,
			UnhealthyFailureThreshold: 3,
		},
	}

	// 健康检查成功
	conn.recordHealthCheck(true, nil)

	// 验证恢复到Healthy
	assert.Equal(t, 0, conn.consecutiveFailures, "连续失败次数应该重置为0")
	assert.Equal(t, HealthStatusHealthy, conn.healthStatus, "健康状态应该是Healthy")
}

// TestRecordHealthCheck_RecoveryFromUnhealthy 测试从Unhealthy恢复
func TestRecordHealthCheck_RecoveryFromUnhealthy(t *testing.T) {
	// 创建测试连接
	conn := &EnhancedPooledConnection{
		id:                  "test-conn-6",
		state:               StateIdle,
		healthStatus:        HealthStatusUnhealthy,
		consecutiveFailures: 5,
		markedForRebuild:    1, // 已标记重建
		config: &EnhancedConnectionConfig{
			DegradedFailureThreshold:  1,
			UnhealthyFailureThreshold: 3,
		},
	}

	// 健康检查成功
	conn.recordHealthCheck(true, nil)

	// 验证恢复到Healthy
	assert.Equal(t, 0, conn.consecutiveFailures, "连续失败次数应该重置为0")
	assert.Equal(t, HealthStatusHealthy, conn.healthStatus, "健康状态应该是Healthy")
}

// TestRecordHealthCheck_RebuildOnDegraded 测试降级时重建配置
func TestRecordHealthCheck_RebuildOnDegraded(t *testing.T) {
	// 创建测试连接（启用降级时重建）
	conn := &EnhancedPooledConnection{
		id:                  "test-conn-7",
		state:               StateIdle,
		healthStatus:        HealthStatusHealthy,
		consecutiveFailures: 0,
		config: &EnhancedConnectionConfig{
			DegradedFailureThreshold:  1,
			UnhealthyFailureThreshold: 3,
			RebuildOnDegraded:         true, // 启用降级时重建
		},
	}

	// 第一次健康检查失败
	conn.recordHealthCheck(false, fmt.Errorf("connection timeout"))

	// 验证状态转换到Degraded
	assert.Equal(t, 1, conn.consecutiveFailures, "连续失败次数应该是1")
	assert.Equal(t, HealthStatusDegraded, conn.healthStatus, "健康状态应该是Degraded")

	// 验证标记重建（因为启用了RebuildOnDegraded）
	assert.True(t, conn.isMarkedForRebuild(), "应该标记为需要重建（降级时）")
	reason := conn.getRebuildReason()
	assert.Contains(t, reason, "health_check_degraded_1_times", "重建原因应该包含降级信息")
}

// TestRecordHealthCheck_NoRebuildWhenDisabled 测试禁用健康检查触发重建
func TestRecordHealthCheck_NoRebuildWhenDisabled(t *testing.T) {
	// 创建测试连接（禁用健康检查触发重建）
	conn := &EnhancedPooledConnection{
		id:                  "test-conn-8",
		state:               StateIdle,
		healthStatus:        HealthStatusDegraded,
		consecutiveFailures: 2,
		config: &EnhancedConnectionConfig{
			DegradedFailureThreshold:  1,
			UnhealthyFailureThreshold: 3,
			HealthCheckTriggerRebuild: false, // 禁用
			RebuildOnDegraded:         false,
		},
	}

	// 第三次健康检查失败
	conn.recordHealthCheck(false, fmt.Errorf("connection timeout"))

	// 验证状态转换到Unhealthy
	assert.Equal(t, 3, conn.consecutiveFailures, "连续失败次数应该是3")
	assert.Equal(t, HealthStatusUnhealthy, conn.healthStatus, "健康状态应该是Unhealthy")

	// 验证未标记重建
	assert.False(t, conn.isMarkedForRebuild(), "不应该标记为需要重建（已禁用）")
}

// TestRecordHealthCheck_CustomThresholds 测试自定义阈值
func TestRecordHealthCheck_CustomThresholds(t *testing.T) {
	// 创建测试连接（自定义阈值）
	conn := &EnhancedPooledConnection{
		id:                  "test-conn-9",
		state:               StateIdle,
		healthStatus:        HealthStatusHealthy,
		consecutiveFailures: 0,
		config: &EnhancedConnectionConfig{
			DegradedFailureThreshold:  2, // 自定义：2次失败降级
			UnhealthyFailureThreshold: 5, // 自定义：5次失败不健康
		},
	}

	// 第一次失败
	conn.recordHealthCheck(false, fmt.Errorf("connection timeout"))
	assert.Equal(t, 1, conn.consecutiveFailures, "连续失败次数应该是1")
	assert.Equal(t, HealthStatusHealthy, conn.healthStatus, "健康状态应该仍是Healthy（未达到降级阈值）")

	// 第二次失败
	conn.recordHealthCheck(false, fmt.Errorf("connection timeout"))
	assert.Equal(t, 2, conn.consecutiveFailures, "连续失败次数应该是2")
	assert.Equal(t, HealthStatusDegraded, conn.healthStatus, "健康状态应该是Degraded")

	// 第三次失败
	conn.recordHealthCheck(false, fmt.Errorf("connection timeout"))
	assert.Equal(t, 3, conn.consecutiveFailures, "连续失败次数应该是3")
	assert.Equal(t, HealthStatusDegraded, conn.healthStatus, "健康状态应该仍是Degraded（未达到不健康阈值）")

	// 第四次失败
	conn.recordHealthCheck(false, fmt.Errorf("connection timeout"))
	assert.Equal(t, 4, conn.consecutiveFailures, "连续失败次数应该是4")
	assert.Equal(t, HealthStatusDegraded, conn.healthStatus, "健康状态应该仍是Degraded")

	// 第五次失败
	conn.recordHealthCheck(false, fmt.Errorf("connection timeout"))
	assert.Equal(t, 5, conn.consecutiveFailures, "连续失败次数应该是5")
	assert.Equal(t, HealthStatusUnhealthy, conn.healthStatus, "健康状态应该是Unhealthy")
}

// TestRecordHealthCheck_StateRecovery 测试状态恢复（Checking -> Idle）
func TestRecordHealthCheck_StateRecovery(t *testing.T) {
	// 创建测试连接（在Checking状态）
	conn := &EnhancedPooledConnection{
		id:                  "test-conn-10",
		state:               StateChecking,
		healthStatus:        HealthStatusHealthy,
		consecutiveFailures: 0,
		config: &EnhancedConnectionConfig{
			DegradedFailureThreshold:  1,
			UnhealthyFailureThreshold: 3,
		},
	}

	// 健康检查成功
	conn.recordHealthCheck(true, nil)

	// 验证状态转换：Checking -> Acquired（新的设计）
	// recordHealthCheck 会在成功后将状态从 Checking 转换到 Acquired
	// 然后由上层调用者决定何时通过 Release() 转回 Idle
	assert.Equal(t, StateAcquired, conn.state, "连接状态应该转换为Acquired")
	assert.Equal(t, HealthStatusHealthy, conn.healthStatus, "健康状态应该是Healthy")
}

// BenchmarkRecordHealthCheck 基准测试
func BenchmarkRecordHealthCheck(b *testing.B) {
	conn := &EnhancedPooledConnection{
		id:                  "bench-conn",
		state:               StateIdle,
		healthStatus:        HealthStatusHealthy,
		consecutiveFailures: 0,
		config: &EnhancedConnectionConfig{
			DegradedFailureThreshold:  1,
			UnhealthyFailureThreshold: 3,
		},
	}

	err := fmt.Errorf("connection timeout")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if i%10 == 0 {
			// 每10次成功一次
			conn.recordHealthCheck(true, nil)
		} else {
			conn.recordHealthCheck(false, err)
		}
	}
}

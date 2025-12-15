package task

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/charlesren/zabbix_ddl_monitor/connection"
	"github.com/stretchr/testify/assert"
)

// TestStatusConstants 测试状态常量定义
func TestStatusConstants(t *testing.T) {
	tests := []struct {
		name     string
		constant string
		expected string
	}{
		{"StatusCheckFinished", StatusCheckFinished, "CheckFinished"},
		{"StatusCheckTimeout", StatusCheckTimeout, "CheckTimeout"},
		{"StatusParseFailed", StatusParseFailed, "ParseResultFailed"},
		{"StatusConnectionError", StatusConnectionError, "ConnectionError"},
		{"StatusExecutionError", StatusExecutionError, "ExecutionError"},
		{"StatusMissingStatusField", StatusMissingStatusField, "MissingStatusField"},
		{"StatusPacketLossOutOfRange", StatusPacketLossOutOfRange, "PacketLossOutOfRange"},
		{"StatusInvalidPacketLossData", StatusInvalidPacketLossData, "InvalidPacketLossData"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.constant, "状态常量 %s 的值应该为 %s", tt.name, tt.expected)
		})
	}
}

// TestResultWithStatus 测试Result结构体中的status字段
func TestResultWithStatus(t *testing.T) {
	tests := []struct {
		name           string
		result         Result
		expectedStatus string
		hasStatus      bool
	}{
		{
			name: "成功结果带CheckFinished状态",
			result: Result{
				Success: true,
				Data: map[string]interface{}{
					"status":      StatusCheckFinished,
					"packet_loss": 0,
					"target_ip":   "192.168.1.1",
				},
			},
			expectedStatus: StatusCheckFinished,
			hasStatus:      true,
		},
		{
			name: "失败结果带ParseFailed状态",
			result: Result{
				Success: false,
				Error:   "解析失败",
				Data: map[string]interface{}{
					"status": StatusParseFailed,
				},
			},
			expectedStatus: StatusParseFailed,
			hasStatus:      true,
		},
		{
			name: "结果缺少status字段",
			result: Result{
				Success: true,
				Data: map[string]interface{}{
					"packet_loss": 50,
					"target_ip":   "192.168.1.1",
				},
			},
			expectedStatus: "",
			hasStatus:      false,
		},
		{
			name: "结果带ConnectionError状态",
			result: Result{
				Success: false,
				Error:   "连接失败",
				Data: map[string]interface{}{
					"status": StatusConnectionError,
				},
			},
			expectedStatus: StatusConnectionError,
			hasStatus:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 测试status字段的获取
			if status, ok := tt.result.Data["status"].(string); ok {
				assert.True(t, tt.hasStatus, "应该包含status字段")
				assert.Equal(t, tt.expectedStatus, status, "status字段值应该为 %s", tt.expectedStatus)
			} else {
				assert.False(t, tt.hasStatus, "不应该包含status字段")
			}
		})
	}
}

// TestExecutorErrorStatus 测试Executor错误处理中的状态设置
func TestExecutorErrorStatus(t *testing.T) {
	// 创建一个模拟的Task
	mockTask := &mockTaskWithError{}

	// 创建Executor
	executor := NewExecutor(nil)

	// 创建模拟的上下文
	ctx := TaskContext{
		TaskType:    "ping",
		Platform:    connection.PlatformCiscoIOSXE,
		Protocol:    connection.ProtocolSSH,
		CommandType: connection.CommandTypeCommands,
		Params: map[string]interface{}{
			"target_ip": "192.168.1.1",
			"repeat":    5,
			"timeout":   30 * time.Second,
		},
		Ctx: context.Background(),
	}

	// 测试参数验证失败
	ctxInvalidParams := ctx
	ctxInvalidParams.Params = map[string]interface{}{} // 缺少target_ip

	result, err := executor.Execute(mockTask, nil, ctxInvalidParams)
	assert.Error(t, err, "参数验证应该失败")
	assert.False(t, result.Success, "结果应该标记为失败")
	assert.Contains(t, result.Error, "target_ip", "错误信息应该包含target_ip")

	// 检查status字段
	if status, ok := result.Data["status"].(string); ok {
		assert.Equal(t, StatusExecutionError, status, "参数验证失败应该设置ExecutionError状态")
	} else {
		t.Error("结果应该包含status字段")
	}
}

// TestContextTimeoutStatus 测试上下文超时的状态设置
func TestContextTimeoutStatus(t *testing.T) {
	// 创建一个会超时的上下文
	timeoutCtx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	// 等待超时
	time.Sleep(2 * time.Millisecond)

	// 检查上下文错误
	assert.Equal(t, context.DeadlineExceeded, timeoutCtx.Err(), "上下文应该已超时")
}

// mockTaskWithError 模拟一个会返回错误的Task
type mockTaskWithError struct{}

func (m *mockTaskWithError) Meta() TaskMeta {
	return TaskMeta{
		Type:        "ping",
		Description: "Mock ping task for testing",
		Platforms: []PlatformSupport{
			{
				Platform: connection.PlatformCiscoIOSXE,
				Protocols: []ProtocolSupport{
					{
						Protocol: connection.ProtocolSSH,
						CommandTypes: []CommandTypeSupport{
							{
								CommandType: connection.CommandTypeCommands,
								ImplFactory: func() Task { return m },
								Params: []ParamSpec{
									{
										Name:     "target_ip",
										Type:     "string",
										Required: true,
									},
									{
										Name:     "repeat",
										Type:     "int",
										Required: false,
										Default:  5,
									},
									{
										Name:     "timeout",
										Type:     "duration",
										Required: false,
										Default:  30 * time.Second,
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func (m *mockTaskWithError) ValidateParams(params map[string]interface{}) error {
	// 简单的参数验证
	if _, ok := params["target_ip"].(string); !ok {
		return fmt.Errorf("target_ip parameter is required and must be a string")
	}
	return nil
}

func (m *mockTaskWithError) BuildCommand(tc TaskContext) (Command, error) {
	// 返回一个简单的命令
	return Command{
		Type:    connection.CommandTypeCommands,
		Payload: []string{"ping 192.168.1.1"},
	}, nil
}

func (m *mockTaskWithError) ParseOutput(tc TaskContext, raw interface{}) (Result, error) {
	// 模拟解析失败
	return Result{
		Success: false,
		Error:   "mock parse error",
		Data: map[string]interface{}{
			"status": StatusParseFailed,
		},
	}, nil
}

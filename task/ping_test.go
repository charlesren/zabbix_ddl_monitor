package task

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charlesren/zabbix_ddl_monitor/connection"
	"github.com/scrapli/scrapligo/channel"
	"github.com/stretchr/testify/assert"
)

func TestPingTask_Meta(t *testing.T) {
	task := &PingTask{}
	meta := task.Meta()

	// 验证基本元信息
	if meta.Type != "ping" {
		t.Errorf("Expected task type 'ping', got '%s'", meta.Type)
	}

	if meta.Description == "" {
		t.Error("Expected non-empty description")
	}

	// 验证平台支持
	if len(meta.Platforms) == 0 {
		t.Fatal("Expected at least one supported platform")
	}

	// 验证Cisco IOS-XE平台支持
	foundCisco := false
	// 验证H3C Comware平台支持
	foundH3C := false
	for _, platform := range meta.Platforms {
		if platform.Platform == connection.PlatformCiscoIOSXE {
			foundCisco = true
			// 验证协议支持
			if len(platform.Protocols) == 0 {
				t.Error("Expected at least one protocol for Cisco IOS-XE")
			}
		}
		if platform.Platform == connection.PlatformH3CComware {
			foundH3C = true
			// 验证协议支持
			if len(platform.Protocols) == 0 {
				t.Error("Expected at least one protocol for H3C Comware")
			}
		}
	}
	if !foundCisco {
		t.Error("Expected Cisco IOS-XE platform support")
	}
	if !foundH3C {
		t.Error("Expected H3C Comware platform support")
	}
}

func TestPingTask_ValidateParams(t *testing.T) {
	task := &PingTask{}

	tests := []struct {
		name    string
		params  map[string]interface{}
		wantErr bool
		errMsg  string
	}{
		{
			name:    "missing target_ip",
			params:  map[string]interface{}{},
			wantErr: true,
			errMsg:  "target_ip parameter is required",
		},
		{
			name: "valid single target_ip",
			params: map[string]interface{}{
				"target_ip": "192.168.1.1",
			},
			wantErr: false,
		},
		{
			name: "empty target_ip",
			params: map[string]interface{}{
				"target_ip": "",
			},
			wantErr: true,
			errMsg:  "target_ip cannot be empty",
		},
		{
			name: "invalid target_ip type",
			params: map[string]interface{}{
				"target_ip": 123,
			},
			wantErr: true,
			errMsg:  "target_ip must be a string",
		},
		{
			name: "invalid repeat value",
			params: map[string]interface{}{
				"target_ip": "192.168.1.1",
				"repeat":    -1,
			},
			wantErr: true,
			errMsg:  "repeat must be between 1 and 100",
		},
		{
			name: "repeat too large",
			params: map[string]interface{}{
				"target_ip": "192.168.1.1",
				"repeat":    101,
			},
			wantErr: true,
			errMsg:  "repeat must be between 1 and 100",
		},
		{
			name: "invalid repeat type",
			params: map[string]interface{}{
				"target_ip": "192.168.1.1",
				"repeat":    "5",
			},
			wantErr: true,
			errMsg:  "repeat must be an integer",
		},
		{
			name: "invalid timeout value",
			params: map[string]interface{}{
				"target_ip": "192.168.1.1",
				"timeout":   -1 * time.Second,
			},
			wantErr: true,
			errMsg:  "timeout must be between 1ms and 60s",
		},
		{
			name: "timeout too large",
			params: map[string]interface{}{
				"target_ip": "192.168.1.1",
				"timeout":   61 * time.Second,
			},
			wantErr: true,
			errMsg:  "timeout must be between 1ms and 60s",
		},
		{
			name: "invalid timeout type",
			params: map[string]interface{}{
				"target_ip": "192.168.1.1",
				"timeout":   "10s",
			},
			wantErr: true,
			errMsg:  "timeout must be a time.Duration",
		},
		{
			name: "invalid enable_password type",
			params: map[string]interface{}{
				"target_ip":       "192.168.1.1",
				"enable_password": 123,
			},
			wantErr: true,
			errMsg:  "enable_password must be a string",
		},
		{
			name: "valid all parameters",
			params: map[string]interface{}{
				"target_ip":       "192.168.1.1",
				"repeat":          5,
				"timeout":         10 * time.Second,
				"enable_password": "admin123",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := task.ValidateParams(tt.params)
			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected error but got none")
					return
				}
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("Expected error message to contain '%s', got '%s'", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error but got: %v", err)
				}
			}
		})
	}
}

func TestPingTask_BuildCommand_SingleIP(t *testing.T) {
	task := &PingTask{}

	tests := []struct {
		name        string
		ctx         TaskContext
		wantErr     bool
		wantCmdType connection.CommandType
		checkFunc   func(*testing.T, Command)
	}{
		{
			name: "cisco_iosxe_single_ip_no_enable",
			ctx: TaskContext{
				Platform:    connection.PlatformCiscoIOSXE,
				Protocol:    connection.ProtocolScrapli,
				CommandType: connection.CommandTypeInteractiveEvent,
				Params: map[string]interface{}{
					"target_ip": "192.168.1.1",
					"repeat":    5,
					"timeout":   10 * time.Second,
				},
				Ctx: context.Background(),
			},
			wantErr:     false,
			wantCmdType: connection.CommandTypeInteractiveEvent,
			checkFunc: func(t *testing.T, cmd Command) {
				events, ok := cmd.Payload.([]*channel.SendInteractiveEvent)
				if !ok {
					t.Fatal("Expected payload to be []*channel.SendInteractiveEvent")
				}
				if len(events) != 1 {
					t.Errorf("Expected 1 event, got %d", len(events))
				}
				if !strings.Contains(events[0].ChannelInput, "ping 192.168.1.1") {
					t.Errorf("Expected ping command for 192.168.1.1, got '%s'", events[0].ChannelInput)
				}
			},
		},
		{
			name: "cisco_iosxe_single_ip_with_enable",
			ctx: TaskContext{
				Platform:    connection.PlatformCiscoIOSXE,
				Protocol:    connection.ProtocolScrapli,
				CommandType: connection.CommandTypeInteractiveEvent,
				Params: map[string]interface{}{
					"target_ip":       "192.168.1.1",
					"repeat":          3,
					"timeout":         5 * time.Second,
					"enable_password": "admin123",
				},
				Ctx: context.Background(),
			},
			wantErr:     false,
			wantCmdType: connection.CommandTypeInteractiveEvent,
			checkFunc: func(t *testing.T, cmd Command) {
				events, ok := cmd.Payload.([]*channel.SendInteractiveEvent)
				if !ok {
					t.Fatal("Expected payload to be []*channel.SendInteractiveEvent")
				}
				if len(events) != 1 {
					t.Errorf("Expected 1 event for single IP, got %d", len(events))
				}
				// 检查ping命令
				if !strings.Contains(events[0].ChannelInput, "ping 192.168.1.1") {
					t.Errorf("Expected ping command, got '%s'", events[0].ChannelInput)
				}
			},
		},
		{
			name: "huawei_vrp_single_ip",
			ctx: TaskContext{
				Platform:    connection.PlatformHuaweiVRP,
				Protocol:    connection.ProtocolScrapli,
				CommandType: connection.CommandTypeInteractiveEvent,
				Params: map[string]interface{}{
					"target_ip": "10.1.1.1",
					"repeat":    4,
					"timeout":   3 * time.Second,
				},
				Ctx: context.Background(),
			},
			wantErr:     false,
			wantCmdType: connection.CommandTypeInteractiveEvent,
			checkFunc: func(t *testing.T, cmd Command) {
				events, ok := cmd.Payload.([]*channel.SendInteractiveEvent)
				if !ok {
					t.Fatal("Expected payload to be []*channel.SendInteractiveEvent")
				}
				if len(events) != 1 {
					t.Errorf("Expected 1 event, got %d", len(events))
				}
				expectedCmd := "ping -c 4 -t 3 10.1.1.1"
				if events[0].ChannelInput != expectedCmd {
					t.Errorf("Expected '%s', got '%s'", expectedCmd, events[0].ChannelInput)
				}
			},
		},
		{
			name: "unsupported_platform",
			ctx: TaskContext{
				Platform:    "unknown_platform",
				Protocol:    connection.ProtocolScrapli,
				CommandType: connection.CommandTypeInteractiveEvent,
				Params: map[string]interface{}{
					"target_ip": "192.168.1.1",
				},
				Ctx: context.Background(),
			},
			wantErr: true,
		},
		{
			name: "missing_target_ip",
			ctx: TaskContext{
				Platform:    connection.PlatformCiscoIOSXE,
				Protocol:    connection.ProtocolScrapli,
				CommandType: connection.CommandTypeInteractiveEvent,
				Params:      map[string]interface{}{},
				Ctx:         context.Background(),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := task.BuildCommand(tt.ctx)
			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Expected no error but got: %v", err)
				return
			}

			if cmd.Type != tt.wantCmdType {
				t.Errorf("Expected command type %s, got %s", tt.wantCmdType, cmd.Type)
			}

			if tt.checkFunc != nil {
				tt.checkFunc(t, cmd)
			}
		})
	}
}

func TestPingTask_ParseOutput_SingleIP(t *testing.T) {
	task := &PingTask{}

	tests := []struct {
		name       string
		ctx        TaskContext
		rawOutput  interface{}
		wantErr    bool
		wantResult Result
	}{
		{
			name: "cisco_iosxe_success",
			ctx: TaskContext{
				Platform: connection.PlatformCiscoIOSXE,
				Params: map[string]interface{}{
					"target_ip": "192.168.1.1",
				},
			},
			rawOutput: `Type escape sequence to abort.
Sending 5, 100-byte ICMP Echos to 192.168.1.1, timeout is 2 seconds:
!!!!!
Success rate is 100 percent (5/5), round-trip min/avg/max = 1/2/4 ms`,
			wantErr: false,
			wantResult: Result{
				Success: true,
				Data: map[string]interface{}{
					"target_ip":    "192.168.1.1",
					"success_rate": 100,
					"status":       "CheckFinished",
				},
			},
		},
		{
			name: "cisco_iosxe_failure",
			ctx: TaskContext{
				Platform: connection.PlatformCiscoIOSXE,
				Params: map[string]interface{}{
					"target_ip": "192.168.1.1",
				},
			},
			rawOutput: `Type escape sequence to abort.
Sending 5, 100-byte ICMP Echos to 192.168.1.1, timeout is 2 seconds:
.....
Success rate is 0 percent (0/5)`,
			wantErr: false,
			wantResult: Result{
				Success: true,
				Data: map[string]interface{}{
					"target_ip":   "192.168.1.1",
					"packet_loss": 100,
					"status":      "CheckFinished",
				},
			},
		},
		{
			name: "huawei_vrp_success",
			ctx: TaskContext{
				Platform: connection.PlatformHuaweiVRP,
				Params: map[string]interface{}{
					"target_ip": "10.1.1.1",
				},
			},
			rawOutput: `PING 10.1.1.1: 56  data bytes, press CTRL_C to break
Reply from 10.1.1.1: bytes=56 Sequence=1 ttl=64 time=1 ms
Reply from 10.1.1.1: bytes=56 Sequence=2 ttl=64 time=1 ms
Reply from 10.1.1.1: bytes=56 Sequence=3 ttl=64 time=1 ms
Reply from 10.1.1.1: bytes=56 Sequence=4 ttl=64 time=1 ms
Reply from 10.1.1.1: bytes=56 Sequence=5 ttl=64 time=1 ms

--- 10.1.1.1 ping statistics ---
5 packet(s) transmitted
5 packet(s) received
0% packet loss`,
			wantErr: false,
			wantResult: Result{
				Success: true,
				Data: map[string]interface{}{
					"target_ip":   "10.1.1.1",
					"packet_loss": 0,
					"status":      "CheckFinished",
				},
			},
		},
		{
			name: "huawei_vrp_failure",
			ctx: TaskContext{
				Platform: connection.PlatformHuaweiVRP,
				Params: map[string]interface{}{
					"target_ip": "10.1.1.1",
				},
			},
			rawOutput: `PING 10.1.1.1: 56  data bytes, press CTRL_C to break

--- 10.1.1.1 ping statistics ---
5 packet(s) transmitted
0 packet(s) received
100% packet loss`,
			wantErr: false,
			wantResult: Result{
				Success: true,
				Data: map[string]interface{}{
					"target_ip":   "10.1.1.1",
					"packet_loss": 100,
					"status":      "CheckFinished",
				},
			},
		},
		{
			name: "unsupported_output_type",
			ctx: TaskContext{
				Platform: connection.PlatformCiscoIOSXE,
				Params: map[string]interface{}{
					"target_ip": "192.168.1.1",
				},
			},
			rawOutput: 12345,
			wantErr:   true,
		},
		{
			name: "byte_slice_output",
			ctx: TaskContext{
				Platform: connection.PlatformCiscoIOSXE,
				Params: map[string]interface{}{
					"target_ip": "192.168.1.1",
				},
			},
			rawOutput: []byte(`Success rate is 100 percent (5/5)`),
			wantErr:   false,
			wantResult: Result{
				Success: true,
				Data: map[string]interface{}{
					"target_ip":    "192.168.1.1",
					"success_rate": 100,
					"status":       "CheckFinished",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := task.ParseOutput(tt.ctx, tt.rawOutput)
			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Expected no error but got: %v", err)
				return
			}

			// 检查成功状态
			if result.Success != tt.wantResult.Success {
				t.Errorf("Expected success %t, got %t", tt.wantResult.Success, result.Success)
			}

			// 检查数据字段
			if result.Data == nil {
				t.Fatal("Expected result.Data to be non-nil")
			}

			// 检查raw_output字段存在
			if _, ok := result.Data["raw_output"]; !ok {
				t.Error("Expected raw_output field in result data")
			}

			// 检查target_ip字段
			if targetIP, ok := result.Data["target_ip"]; ok {
				if targetIP != tt.wantResult.Data["target_ip"] {
					t.Errorf("Expected target_ip=%v, got %v", tt.wantResult.Data["target_ip"], targetIP)
				}
			}

			// 检查特定的数据字段
			for key, expectedValue := range tt.wantResult.Data {
				if key == "raw_output" || key == "target_ip" {
					continue // raw_output和target_ip已经检查过
				}
				if actualValue, ok := result.Data[key]; ok {
					if actualValue != expectedValue {
						t.Errorf("Expected %s=%v, got %v", key, expectedValue, actualValue)
					}
				}
			}
		})
	}
}

func TestPingTask_buildCiscoEvent(t *testing.T) {
	task := PingTask{}

	tests := []struct {
		name           string
		targetIP       string
		repeat         int
		timeout        time.Duration
		enablePassword string
		expectedCount  int
		checkFunc      func(*testing.T, []*channel.SendInteractiveEvent)
	}{
		{
			name:           "single_ip_no_enable",
			targetIP:       "192.168.1.1",
			repeat:         5,
			timeout:        10 * time.Second,
			enablePassword: "",
			expectedCount:  1,
			checkFunc: func(t *testing.T, events []*channel.SendInteractiveEvent) {
				if !strings.Contains(events[0].ChannelInput, "ping 192.168.1.1") {
					t.Error("Expected ping command for 192.168.1.1")
				}
				if events[0].ChannelResponse != ">" {
					t.Error("Expected prompt '>' for user mode")
				}
			},
		},
		{
			name:           "single_ip_with_enable",
			targetIP:       "192.168.1.1",
			repeat:         3,
			timeout:        5 * time.Second,
			enablePassword: "admin123",
			expectedCount:  1,
			checkFunc: func(t *testing.T, events []*channel.SendInteractiveEvent) {
				if !strings.Contains(events[0].ChannelInput, "ping 192.168.1.1") {
					t.Error("Expected ping command")
				}
				if events[0].ChannelResponse != "#" {
					t.Error("Expected prompt '#' for privileged mode")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := task.buildCiscoEvent(tt.targetIP, tt.repeat, tt.timeout, tt.enablePassword)

			if len(events) != tt.expectedCount {
				t.Errorf("Expected %d events, got %d", tt.expectedCount, len(events))
			}

			if tt.checkFunc != nil {
				tt.checkFunc(t, events)
			}
		})
	}
}

func TestPingTask_buildHuaweiEvent(t *testing.T) {
	task := PingTask{}

	tests := []struct {
		name          string
		targetIP      string
		repeat        int
		timeout       time.Duration
		expectedCount int
		checkFunc     func(*testing.T, []*channel.SendInteractiveEvent)
	}{
		{
			name:          "single_ip",
			targetIP:      "10.1.1.1",
			repeat:        4,
			timeout:       3 * time.Second,
			expectedCount: 1,
			checkFunc: func(t *testing.T, events []*channel.SendInteractiveEvent) {
				expected := "ping -c 4 -W 3 10.1.1.1"
				if events[0].ChannelInput != expected {
					t.Errorf("Expected '%s', got '%s'", expected, events[0].ChannelInput)
				}
				if events[0].ChannelResponse != ">" {
					t.Error("Expected prompt '>'")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := task.buildHuaweiEvent(tt.targetIP, tt.repeat, tt.timeout)

			if len(events) != tt.expectedCount {
				t.Errorf("Expected %d events, got %d", tt.expectedCount, len(events))
			}

			if tt.checkFunc != nil {
				tt.checkFunc(t, events)
			}
		})
	}
}

// Benchmark tests
func BenchmarkPingTask_ValidateParams(b *testing.B) {
	task := &PingTask{}
	params := map[string]interface{}{
		"target_ip": "192.168.1.1",
		"repeat":    5,
		"timeout":   10 * time.Second,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = task.ValidateParams(params)
	}
}

func BenchmarkPingTask_BuildCommand_SingleIP(b *testing.B) {
	task := &PingTask{}
	ctx := TaskContext{
		Platform:    connection.PlatformCiscoIOSXE,
		Protocol:    connection.ProtocolScrapli,
		CommandType: connection.CommandTypeInteractiveEvent,
		Params: map[string]interface{}{
			"target_ip": "192.168.1.1",
			"repeat":    5,
			"timeout":   10 * time.Second,
		},
		Ctx: context.Background(),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = task.BuildCommand(ctx)
	}
}

func BenchmarkPingTask_ParseOutput(b *testing.B) {
	task := &PingTask{}
	ctx := TaskContext{
		Platform: connection.PlatformCiscoIOSXE,
		Params: map[string]interface{}{
			"target_ip": "192.168.1.1",
		},
	}
	output := `Type escape sequence to abort.
Sending 5, 100-byte ICMP Echos to 192.168.1.1, timeout is 2 seconds:
!!!!!
Success rate is 100 percent (5/5), round-trip min/avg/max = 1/2/4 ms`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = task.ParseOutput(ctx, output)
	}
}

// Test helper functions
func createValidTaskContext(platform connection.Platform) TaskContext {
	return TaskContext{
		Platform:    platform,
		Protocol:    connection.ProtocolScrapli,
		CommandType: connection.CommandTypeInteractiveEvent,
		Params: map[string]interface{}{
			"target_ip": "192.168.1.1",
			"repeat":    5,
			"timeout":   10 * time.Second,
		},
		Ctx: context.Background(),
	}
}

// Integration test with real-like scenarios
func TestPingTask_Integration(t *testing.T) {
	task := &PingTask{}

	// Test complete workflow: validate -> build -> parse
	t.Run("complete_workflow_single_ip", func(t *testing.T) {
		ctx := createValidTaskContext(connection.PlatformCiscoIOSXE)

		// Step 1: Validate
		err := task.ValidateParams(ctx.Params)
		if err != nil {
			t.Fatalf("Validation failed: %v", err)
		}

		// Step 2: Build command
		cmd, err := task.BuildCommand(ctx)
		if err != nil {
			t.Fatalf("Build command failed: %v", err)
		}

		if cmd.Type != connection.CommandTypeInteractiveEvent {
			t.Errorf("Expected InteractiveEvent command type")
		}

		// Step 3: Parse mock output
		mockOutput := `Success rate is 100 percent (5/5), round-trip min/avg/max = 1/2/4 ms`
		result, err := task.ParseOutput(ctx, mockOutput)
		if err != nil {
			t.Fatalf("Parse output failed: %v", err)
		}

		if !result.Success {
			t.Error("Expected successful result")
		}
	})
}

func TestPingTask_H3CComware(t *testing.T) {
	task := &PingTask{}

	// 测试Meta方法
	meta := task.Meta()
	found := false
	for _, platform := range meta.Platforms {
		if platform.Platform == connection.PlatformH3CComware {
			found = true
			break
		}
	}
	assert.True(t, found, "H3C Comware should be in supported platforms")

	// 测试命令构建 - 非交互式命令
	t.Run("build_commands", func(t *testing.T) {
		ctx := TaskContext{
			Platform:    connection.PlatformH3CComware,
			CommandType: connection.CommandTypeCommands,
			Params: map[string]interface{}{
				"target_ip": "8.8.8.8",
				"repeat":    3,
				"timeout":   2 * time.Second,
			},
		}

		cmd, err := task.BuildCommand(ctx)
		assert.NoError(t, err)
		assert.Equal(t, connection.CommandTypeCommands, cmd.Type)

		commands, ok := cmd.Payload.([]string)
		assert.True(t, ok)
		assert.Len(t, commands, 1)
		assert.Contains(t, commands[0], "ping -c 3 -t 2 8.8.8.8")
	})

	// 测试命令构建 - 交互式事件
	t.Run("build_interactive_events", func(t *testing.T) {
		ctx := TaskContext{
			Platform:    connection.PlatformH3CComware,
			CommandType: connection.CommandTypeInteractiveEvent,
			Params: map[string]interface{}{
				"target_ip": "192.168.1.1",
				"repeat":    5,
				"timeout":   3 * time.Second,
			},
		}

		cmd, err := task.BuildCommand(ctx)
		assert.NoError(t, err)
		assert.Equal(t, connection.CommandTypeInteractiveEvent, cmd.Type)

		events, ok := cmd.Payload.([]*channel.SendInteractiveEvent)
		assert.True(t, ok)
		assert.Len(t, events, 1)
		assert.Contains(t, events[0].ChannelInput, "ping -c 5 -t 3 192.168.1.1")
		assert.Equal(t, ">", events[0].ChannelResponse)
	})

	// 测试输出解析 - 成功情况
	t.Run("parse_success_output", func(t *testing.T) {
		ctx := TaskContext{
			Platform: connection.PlatformH3CComware,
			Params: map[string]interface{}{
				"target_ip": "8.8.8.8",
			},
		}

		successOutput := `ping -c 3 -t 2 8.8.8.8
PING 8.8.8.8 (8.8.8.8): 56 data bytes
64 bytes from 8.8.8.8: icmp_seq=0 ttl=117 time=25.367 ms
64 bytes from 8.8.8.8: icmp_seq=1 ttl=117 time=25.213 ms
64 bytes from 8.8.8.8: icmp_seq=2 ttl=117 time=25.296 ms

--- Ping statistics for 8.8.8.8 ---
3 packet(s) transmitted, 3 packet(s) received, 0.0% packet loss
round-trip min/avg/max/std-dev = 25.213/25.292/25.367/0.014 ms`

		result, err := task.ParseOutput(ctx, successOutput)
		assert.NoError(t, err)
		assert.True(t, result.Success)
		assert.Equal(t, 0, result.Data["packet_loss"])
		assert.Equal(t, 100, result.Data["success_rate"])
		assert.Equal(t, "CheckFinished", result.Data["status"])
	})

	// 测试输出解析 - 失败情况（100%丢包）
	t.Run("parse_failure_output_100_percent_loss", func(t *testing.T) {
		ctx := TaskContext{
			Platform: connection.PlatformH3CComware,
			Params: map[string]interface{}{
				"target_ip": "192.0.2.1",
			},
		}

		failOutput := `ping -c 3 -t 2 192.0.2.1
PING 192.0.2.1 (192.0.2.1): 56 data bytes

--- Ping statistics for 192.0.2.1 ---
3 packet(s) transmitted, 0 packet(s) received, 100.0% packet loss`

		result, err := task.ParseOutput(ctx, failOutput)
		assert.NoError(t, err)
		assert.True(t, result.Success)
		assert.Equal(t, 100, result.Data["packet_loss"])
		assert.Equal(t, 0, result.Data["success_rate"])
		assert.Equal(t, "CheckFinished", result.Data["status"])
	})

	// 测试输出解析 - 部分丢包
	t.Run("parse_partial_loss_output", func(t *testing.T) {
		ctx := TaskContext{
			Platform: connection.PlatformH3CComware,
			Params: map[string]interface{}{
				"target_ip": "10.0.0.1",
			},
		}

		partialOutput := `ping -c 5 -t 2 10.0.0.1
PING 10.0.0.1 (10.0.0.1): 56 data bytes
64 bytes from 10.0.0.1: icmp_seq=0 ttl=64 time=1.234 ms
64 bytes from 10.0.0.1: icmp_seq=1 ttl=64 time=1.345 ms
64 bytes from 10.0.0.1: icmp_seq=4 ttl=64 time=1.456 ms

--- Ping statistics for 10.0.0.1 ---
5 packet(s) transmitted, 3 packet(s) received, 40.0% packet loss
round-trip min/avg/max/std-dev = 1.234/1.345/1.456/0.014 ms`

		result, err := task.ParseOutput(ctx, partialOutput)
		assert.NoError(t, err)
		assert.True(t, result.Success)
		assert.Equal(t, 40, result.Data["packet_loss"])
		assert.Equal(t, 60, result.Data["success_rate"])
		assert.Equal(t, "CheckFinished", result.Data["status"])
	})

	// 测试输出解析 - 请求超时
	t.Run("parse_timeout_output", func(t *testing.T) {
		ctx := TaskContext{
			Platform: connection.PlatformH3CComware,
			Params: map[string]interface{}{
				"target_ip": "203.0.113.1",
			},
		}

		timeoutOutput := `ping -c 3 -t 2 203.0.113.1
PING 203.0.113.1 (203.0.113.1): 56 data bytes
Request time out
Request time out
Request time out

--- Ping statistics for 203.0.113.1 ---
3 packet(s) transmitted, 0 packet(s) received, 100.0% packet loss`

		result, err := task.ParseOutput(ctx, timeoutOutput)
		assert.NoError(t, err)
		assert.True(t, result.Success)
		assert.Equal(t, 100, result.Data["packet_loss"])
		assert.Equal(t, 0, result.Data["success_rate"])
		assert.Equal(t, "CheckFinished", result.Data["status"])
	})

	// 测试输出解析 - 主机不可达
	t.Run("parse_unreachable_output", func(t *testing.T) {
		ctx := TaskContext{
			Platform: connection.PlatformH3CComware,
			Params: map[string]interface{}{
				"target_ip": "192.168.99.99",
			},
		}

		unreachableOutput := `ping -c 3 -t 2 192.168.99.99
PING 192.168.99.99 (192.168.99.99): 56 data bytes
From 192.168.1.1 icmp_seq=0 Destination Host Unreachable
From 192.168.1.1 icmp_seq=1 Destination Host Unreachable
From 192.168.1.1 icmp_seq=2 Destination Host Unreachable

--- Ping statistics for 192.168.99.99 ---
3 packet(s) transmitted, 0 packet(s) received, 100.0% packet loss`

		result, err := task.ParseOutput(ctx, unreachableOutput)
		assert.NoError(t, err)
		assert.True(t, result.Success)
		assert.Equal(t, 100, result.Data["packet_loss"])
		assert.Equal(t, 0, result.Data["success_rate"])
		assert.Equal(t, "CheckFinished", result.Data["status"])
	})

	// 测试参数验证
	t.Run("validate_params", func(t *testing.T) {
		validParams := map[string]interface{}{
			"target_ip": "8.8.8.8",
			"repeat":    5,
			"timeout":   2 * time.Second,
		}

		err := task.ValidateParams(validParams)
		assert.NoError(t, err)

		// 测试无效参数
		invalidParams := map[string]interface{}{
			"repeat": 0, // 小于1
		}
		err = task.ValidateParams(invalidParams)
		assert.Error(t, err)
	})
}

func TestPingTask_ErrorCases(t *testing.T) {
	task := &PingTask{}

	t.Run("invalid_parameter_types", func(t *testing.T) {
		invalidParams := []map[string]interface{}{
			{"target_ip": 123},
			{"repeat": "not_int"},
			{"timeout": "not_duration"},
			{"enable_password": 456},
		}

		for i, params := range invalidParams {
			err := task.ValidateParams(params)
			if err == nil {
				t.Errorf("Test case %d: expected validation error but got none", i)
			}
		}
	})

	t.Run("build_command_errors", func(t *testing.T) {
		errorCases := []TaskContext{
			// Missing target_ip
			{
				Platform: connection.PlatformCiscoIOSXE,
				Params:   map[string]interface{}{},
				Ctx:      context.Background(),
			},
			// Unsupported platform
			{
				Platform: "unsupported_platform",
				Params: map[string]interface{}{
					"target_ip": "192.168.1.1",
				},
				Ctx: context.Background(),
			},
		}

		for i, ctx := range errorCases {
			_, err := task.BuildCommand(ctx)
			if err == nil {
				t.Errorf("Test case %d: expected build command error but got none", i)
			}
		}
	})
}

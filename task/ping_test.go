package task

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charlesren/zabbix_ddl_monitor/connection"
	"github.com/scrapli/scrapligo/channel"
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
	found := false
	for _, platform := range meta.Platforms {
		if platform.Platform == connection.PlatformCiscoIOSXE {
			found = true
			// 验证协议支持
			if len(platform.Protocols) == 0 {
				t.Error("Expected at least one protocol for Cisco IOS-XE")
			}
			break
		}
	}
	if !found {
		t.Error("Expected Cisco IOS-XE platform support")
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
			name:    "missing both target_ip and target_ips",
			params:  map[string]interface{}{},
			wantErr: true,
			errMsg:  "either target_ip or target_ips parameter is required",
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
			name: "valid target_ips",
			params: map[string]interface{}{
				"target_ips": []string{"192.168.1.1", "192.168.1.2"},
			},
			wantErr: false,
		},
		{
			name: "empty target_ips",
			params: map[string]interface{}{
				"target_ips": []string{},
			},
			wantErr: true,
			errMsg:  "target_ips cannot be empty",
		},
		{
			name: "target_ips with empty IP",
			params: map[string]interface{}{
				"target_ips": []string{"192.168.1.1", ""},
			},
			wantErr: true,
			errMsg:  "target_ips[1] cannot be empty",
		},
		{
			name: "invalid target_ips type",
			params: map[string]interface{}{
				"target_ips": "192.168.1.1",
			},
			wantErr: true,
			errMsg:  "target_ips must be a []string",
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
				if len(events) != 3 {
					t.Errorf("Expected 3 events (enable, password, ping), got %d", len(events))
				}
				// 检查enable命令
				if events[0].ChannelInput != "enable" {
					t.Errorf("Expected 'enable' command, got '%s'", events[0].ChannelInput)
				}
				// 检查密码（应该隐藏）
				if !events[1].HideInput {
					t.Error("Expected enable password to be hidden")
				}
				// 检查ping命令
				if !strings.Contains(events[2].ChannelInput, "ping 192.168.1.1") {
					t.Errorf("Expected ping command, got '%s'", events[2].ChannelInput)
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
				expectedCmd := "ping -c 4 -W 3 10.1.1.1"
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

func TestPingTask_BuildCommand_BatchIP(t *testing.T) {
	task := &PingTask{}

	tests := []struct {
		name      string
		ctx       TaskContext
		wantErr   bool
		checkFunc func(*testing.T, Command)
	}{
		{
			name: "cisco_iosxe_batch_ips",
			ctx: TaskContext{
				Platform:    connection.PlatformCiscoIOSXE,
				Protocol:    connection.ProtocolScrapli,
				CommandType: connection.CommandTypeInteractiveEvent,
				Params: map[string]interface{}{
					"target_ips": []string{"192.168.1.1", "192.168.1.2", "192.168.1.3"},
					"repeat":     3,
					"timeout":    5 * time.Second,
				},
				Ctx: context.Background(),
			},
			wantErr: false,
			checkFunc: func(t *testing.T, cmd Command) {
				events, ok := cmd.Payload.([]*channel.SendInteractiveEvent)
				if !ok {
					t.Fatal("Expected payload to be []*channel.SendInteractiveEvent")
				}
				if len(events) != 3 {
					t.Errorf("Expected 3 events for 3 IPs, got %d", len(events))
				}

				// 检查每个IP的ping命令
				expectedIPs := []string{"192.168.1.1", "192.168.1.2", "192.168.1.3"}
				for i, event := range events {
					if !strings.Contains(event.ChannelInput, expectedIPs[i]) {
						t.Errorf("Event %d: expected ping for %s, got '%s'", i, expectedIPs[i], event.ChannelInput)
					}
					if !strings.Contains(event.ChannelInput, "repeat 3") {
						t.Errorf("Event %d: expected repeat 3, got '%s'", i, event.ChannelInput)
					}
				}
			},
		},
		{
			name: "huawei_vrp_batch_ips",
			ctx: TaskContext{
				Platform:    connection.PlatformHuaweiVRP,
				Protocol:    connection.ProtocolScrapli,
				CommandType: connection.CommandTypeInteractiveEvent,
				Params: map[string]interface{}{
					"target_ips": []string{"10.1.1.1", "10.1.1.2"},
					"repeat":     4,
					"timeout":    2 * time.Second,
				},
				Ctx: context.Background(),
			},
			wantErr: false,
			checkFunc: func(t *testing.T, cmd Command) {
				events, ok := cmd.Payload.([]*channel.SendInteractiveEvent)
				if !ok {
					t.Fatal("Expected payload to be []*channel.SendInteractiveEvent")
				}
				if len(events) != 2 {
					t.Errorf("Expected 2 events for 2 IPs, got %d", len(events))
				}

				expectedCmds := []string{
					"ping -c 4 -W 2 10.1.1.1",
					"ping -c 4 -W 2 10.1.1.2",
				}
				for i, event := range events {
					if event.ChannelInput != expectedCmds[i] {
						t.Errorf("Event %d: expected '%s', got '%s'", i, expectedCmds[i], event.ChannelInput)
					}
				}
			},
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
					"batch_mode":   false,
					"success_rate": "100%",
					"status":       "success",
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
				Success: false,
				Data: map[string]interface{}{
					"batch_mode":   false,
					"success_rate": "0%",
					"status":       "failed",
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
					"batch_mode":  false,
					"packet_loss": "0%",
					"status":      "success",
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
				Success: false,
				Data: map[string]interface{}{
					"batch_mode":  false,
					"packet_loss": "100%",
					"status":      "failed",
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
					"batch_mode":   false,
					"success_rate": "100%",
					"status":       "success",
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

			// 检查batch_mode字段
			if batchMode, ok := result.Data["batch_mode"]; ok {
				if batchMode != tt.wantResult.Data["batch_mode"] {
					t.Errorf("Expected batch_mode %v, got %v", tt.wantResult.Data["batch_mode"], batchMode)
				}
			}

			// 检查特定的数据字段
			for key, expectedValue := range tt.wantResult.Data {
				if key == "raw_output" {
					continue // raw_output由函数生成，不需要检查具体值
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

func TestPingTask_ParseOutput_BatchIP(t *testing.T) {
	task := &PingTask{}

	tests := []struct {
		name      string
		ctx       TaskContext
		rawOutput interface{}
		wantErr   bool
		checkFunc func(*testing.T, Result)
	}{
		{
			name: "cisco_iosxe_batch_mixed_results",
			ctx: TaskContext{
				Platform: connection.PlatformCiscoIOSXE,
				Params: map[string]interface{}{
					"target_ips": []string{"192.168.1.1", "192.168.1.2", "192.168.1.3"},
				},
			},
			rawOutput: `Router>ping 192.168.1.1 repeat 5 timeout 2
Type escape sequence to abort.
Sending 5, 100-byte ICMP Echos to 192.168.1.1, timeout is 2 seconds:
!!!!!
Success rate is 100 percent (5/5), round-trip min/avg/max = 1/2/4 ms
Router>ping 192.168.1.2 repeat 5 timeout 2
Type escape sequence to abort.
Sending 5, 100-byte ICMP Echos to 192.168.1.2, timeout is 2 seconds:
!!!!!
Success rate is 100 percent (5/5), round-trip min/avg/max = 2/3/5 ms
Router>ping 192.168.1.3 repeat 5 timeout 2
Type escape sequence to abort.
Sending 5, 100-byte ICMP Echos to 192.168.1.3, timeout is 2 seconds:
.....
Success rate is 0 percent (0/5)`,
			wantErr: false,
			checkFunc: func(t *testing.T, result Result) {
				// 检查batch模式
				if batchMode, ok := result.Data["batch_mode"]; !ok || batchMode != true {
					t.Error("Expected batch_mode=true")
				}

				// 检查batch_results
				batchResults, ok := result.Data["batch_results"].(map[string]Result)
				if !ok {
					t.Fatal("Expected batch_results to be map[string]Result")
				}

				if len(batchResults) != 3 {
					t.Errorf("Expected 3 results, got %d", len(batchResults))
				}

				// 检查第一个IP（成功）
				if r1, ok := batchResults["192.168.1.1"]; ok {
					if !r1.Success {
						t.Error("Expected 192.168.1.1 to be successful")
					}
				} else {
					t.Error("Expected result for 192.168.1.1")
				}

				// 检查第二个IP（成功）
				if r2, ok := batchResults["192.168.1.2"]; ok {
					if !r2.Success {
						t.Error("Expected 192.168.1.2 to be successful")
					}
				} else {
					t.Error("Expected result for 192.168.1.2")
				}

				// 检查第三个IP（失败）
				if r3, ok := batchResults["192.168.1.3"]; ok {
					if r3.Success {
						t.Error("Expected 192.168.1.3 to be failed")
					}
				} else {
					t.Error("Expected result for 192.168.1.3")
				}

				// 检查统计信息
				if successCount, ok := result.Data["success_count"].(int); !ok || successCount != 2 {
					t.Errorf("Expected success_count=2, got %v", result.Data["success_count"])
				}

				if totalCount, ok := result.Data["total_count"].(int); !ok || totalCount != 3 {
					t.Errorf("Expected total_count=3, got %v", result.Data["total_count"])
				}

				if successRate, ok := result.Data["success_rate"].(string); !ok || successRate != "66.7%" {
					t.Errorf("Expected success_rate='66.7%%', got %v", result.Data["success_rate"])
				}

				// 整体成功状态应该为true（至少有一个成功）
				if !result.Success {
					t.Error("Expected overall result to be successful")
				}
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

			if tt.checkFunc != nil {
				tt.checkFunc(t, result)
			}
		})
	}
}

func TestPingTask_buildCiscoEvents(t *testing.T) {
	task := PingTask{}

	tests := []struct {
		name           string
		targetIPs      []string
		repeat         int
		timeout        time.Duration
		enablePassword string
		expectedCount  int
		checkFunc      func(*testing.T, []*channel.SendInteractiveEvent)
	}{
		{
			name:           "single_ip_no_enable",
			targetIPs:      []string{"192.168.1.1"},
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
			targetIPs:      []string{"192.168.1.1"},
			repeat:         3,
			timeout:        5 * time.Second,
			enablePassword: "admin123",
			expectedCount:  3, // enable, password, ping
			checkFunc: func(t *testing.T, events []*channel.SendInteractiveEvent) {
				if events[0].ChannelInput != "enable" {
					t.Error("Expected 'enable' command first")
				}
				if !events[1].HideInput {
					t.Error("Expected password to be hidden")
				}
				if !strings.Contains(events[2].ChannelInput, "ping 192.168.1.1") {
					t.Error("Expected ping command")
				}
				if events[2].ChannelResponse != "#" {
					t.Error("Expected prompt '#' for privileged mode")
				}
			},
		},
		{
			name:           "multiple_ips_no_enable",
			targetIPs:      []string{"192.168.1.1", "192.168.1.2", "192.168.1.3"},
			repeat:         5,
			timeout:        10 * time.Second,
			enablePassword: "",
			expectedCount:  3,
			checkFunc: func(t *testing.T, events []*channel.SendInteractiveEvent) {
				expectedIPs := []string{"192.168.1.1", "192.168.1.2", "192.168.1.3"}
				if len(events) != len(expectedIPs) {
					t.Errorf("Expected %d events, got %d", len(expectedIPs), len(events))
				}
				for i, event := range events {
					if !strings.Contains(event.ChannelInput, expectedIPs[i]) {
						t.Errorf("Event %d: expected IP %s, got '%s'", i, expectedIPs[i], event.ChannelInput)
					}
					if event.ChannelResponse != ">" {
						t.Error("Expected prompt '>' for user mode")
					}
				}
			},
		},
		{
			name:           "multiple_ips_with_enable",
			targetIPs:      []string{"192.168.1.1", "192.168.1.2"},
			repeat:         3,
			timeout:        5 * time.Second,
			enablePassword: "admin123",
			expectedCount:  4, // enable, password, ping1, ping2
			checkFunc: func(t *testing.T, events []*channel.SendInteractiveEvent) {
				if events[0].ChannelInput != "enable" {
					t.Error("Expected 'enable' command first")
				}
				if !events[1].HideInput {
					t.Error("Expected password to be hidden")
				}
				// 检查两个ping命令
				if !strings.Contains(events[2].ChannelInput, "192.168.1.1") {
					t.Error("Expected ping for 192.168.1.1")
				}
				if !strings.Contains(events[3].ChannelInput, "192.168.1.2") {
					t.Error("Expected ping for 192.168.1.2")
				}
				// 特权模式提示符
				if events[2].ChannelResponse != "#" || events[3].ChannelResponse != "#" {
					t.Error("Expected prompt '#' for privileged mode")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := task.buildCiscoEvents(tt.targetIPs, tt.repeat, tt.timeout, tt.enablePassword)

			if len(events) != tt.expectedCount {
				t.Errorf("Expected %d events, got %d", tt.expectedCount, len(events))
			}

			if tt.checkFunc != nil {
				tt.checkFunc(t, events)
			}
		})
	}
}

func TestPingTask_buildHuaweiEvents(t *testing.T) {
	task := PingTask{}

	tests := []struct {
		name          string
		targetIPs     []string
		repeat        int
		timeout       time.Duration
		expectedCount int
		checkFunc     func(*testing.T, []*channel.SendInteractiveEvent)
	}{
		{
			name:          "single_ip",
			targetIPs:     []string{"10.1.1.1"},
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
		{
			name:          "multiple_ips",
			targetIPs:     []string{"10.1.1.1", "10.1.1.2", "10.1.1.3"},
			repeat:        5,
			timeout:       2 * time.Second,
			expectedCount: 3,
			checkFunc: func(t *testing.T, events []*channel.SendInteractiveEvent) {
				expectedCmds := []string{
					"ping -c 5 -W 2 10.1.1.1",
					"ping -c 5 -W 2 10.1.1.2",
					"ping -c 5 -W 2 10.1.1.3",
				}
				for i, event := range events {
					if event.ChannelInput != expectedCmds[i] {
						t.Errorf("Event %d: expected '%s', got '%s'", i, expectedCmds[i], event.ChannelInput)
					}
					if event.ChannelResponse != ">" {
						t.Errorf("Event %d: expected prompt '>'", i)
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := task.buildHuaweiEvents(tt.targetIPs, tt.repeat, tt.timeout)

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

func BenchmarkPingTask_BuildCommand_BatchIP(b *testing.B) {
	task := &PingTask{}
	ctx := TaskContext{
		Platform:    connection.PlatformCiscoIOSXE,
		Protocol:    connection.ProtocolScrapli,
		CommandType: connection.CommandTypeInteractiveEvent,
		Params: map[string]interface{}{
			"target_ips": []string{"192.168.1.1", "192.168.1.2", "192.168.1.3", "192.168.1.4", "192.168.1.5"},
			"repeat":     5,
			"timeout":    10 * time.Second,
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

func createBatchTaskContext(platform connection.Platform, ips []string) TaskContext {
	return TaskContext{
		Platform:    platform,
		Protocol:    connection.ProtocolScrapli,
		CommandType: connection.CommandTypeInteractiveEvent,
		Params: map[string]interface{}{
			"target_ips": ips,
			"repeat":     5,
			"timeout":    10 * time.Second,
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

	t.Run("complete_workflow_batch_ip", func(t *testing.T) {
		ips := []string{"192.168.1.1", "192.168.1.2", "192.168.1.3"}
		ctx := createBatchTaskContext(connection.PlatformCiscoIOSXE, ips)

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

		events, ok := cmd.Payload.([]*channel.SendInteractiveEvent)
		if !ok || len(events) != 3 {
			t.Fatalf("Expected 3 interactive events for batch IPs")
		}

		// Step 3: Parse mock batch output
		mockOutput := `Router>ping 192.168.1.1 repeat 5 timeout 10
Success rate is 100 percent (5/5)
Router>ping 192.168.1.2 repeat 5 timeout 10
Success rate is 100 percent (5/5)
Router>ping 192.168.1.3 repeat 5 timeout 10
Success rate is 0 percent (0/5)`

		result, err := task.ParseOutput(ctx, mockOutput)
		if err != nil {
			t.Fatalf("Parse output failed: %v", err)
		}

		// 检查批量结果
		if batchMode, ok := result.Data["batch_mode"]; !ok || batchMode != true {
			t.Error("Expected batch_mode=true")
		}

		if successCount, ok := result.Data["success_count"]; !ok || successCount != 2 {
			t.Errorf("Expected 2 successful IPs, got %v", successCount)
		}
	})
}

func TestPingTask_ErrorCases(t *testing.T) {
	task := &PingTask{}

	t.Run("invalid_parameter_types", func(t *testing.T) {
		invalidParams := []map[string]interface{}{
			{"target_ip": 123},
			{"target_ips": "not_a_slice"},
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
			// Missing both target_ip and target_ips
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

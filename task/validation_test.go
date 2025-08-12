package task

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/charlesren/zabbix_ddl_monitor/connection"
)

// Test comprehensive parameter validation scenarios

func TestValidation_IPAddressFormats(t *testing.T) {
	task := &PingTask{}

	tests := []struct {
		name    string
		ip      string
		wantErr bool
		errMsg  string
	}{
		// Valid IPv4 addresses
		{
			name:    "valid_ipv4_basic",
			ip:      "192.168.1.1",
			wantErr: false,
		},
		{
			name:    "valid_ipv4_edge_values",
			ip:      "255.255.255.255",
			wantErr: false,
		},
		{
			name:    "valid_ipv4_zero",
			ip:      "0.0.0.0",
			wantErr: false,
		},
		{
			name:    "valid_ipv4_localhost",
			ip:      "127.0.0.1",
			wantErr: false,
		},
		{
			name:    "valid_ipv4_private_class_a",
			ip:      "10.0.0.1",
			wantErr: false,
		},
		{
			name:    "valid_ipv4_private_class_b",
			ip:      "172.16.0.1",
			wantErr: false,
		},
		{
			name:    "valid_ipv4_private_class_c",
			ip:      "192.168.0.1",
			wantErr: false,
		},

		// Invalid IPv4 addresses
		{
			name:    "invalid_empty_string",
			ip:      "",
			wantErr: true,
			errMsg:  "empty",
		},
		{
			name:    "invalid_out_of_range_octet",
			ip:      "256.1.1.1",
			wantErr: true,
			errMsg:  "invalid",
		},
		{
			name:    "invalid_negative_octet",
			ip:      "192.168.-1.1",
			wantErr: true,
			errMsg:  "invalid",
		},
		{
			name:    "invalid_too_few_octets",
			ip:      "192.168.1",
			wantErr: true,
			errMsg:  "invalid",
		},
		{
			name:    "invalid_too_many_octets",
			ip:      "192.168.1.1.1",
			wantErr: true,
			errMsg:  "invalid",
		},
		{
			name:    "invalid_non_numeric",
			ip:      "192.168.x.1",
			wantErr: true,
			errMsg:  "invalid",
		},
		{
			name:    "invalid_with_spaces",
			ip:      "192.168.1.1 ",
			wantErr: true,
			errMsg:  "invalid",
		},
		{
			name:    "invalid_with_leading_zeros",
			ip:      "192.168.001.001",
			wantErr: true,
			errMsg:  "invalid",
		},
		{
			name:    "invalid_hostname",
			ip:      "example.com",
			wantErr: true,
			errMsg:  "invalid",
		},
		{
			name:    "invalid_with_port",
			ip:      "192.168.1.1:80",
			wantErr: true,
			errMsg:  "invalid",
		},
		{
			name:    "invalid_cidr_notation",
			ip:      "192.168.1.0/24",
			wantErr: true,
			errMsg:  "invalid",
		},
		{
			name:    "invalid_hex_format",
			ip:      "0xC0A80101",
			wantErr: true,
			errMsg:  "invalid",
		},
		{
			name:    "invalid_binary_data",
			ip:      "\xc0\xa8\x01\x01",
			wantErr: true,
			errMsg:  "invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]interface{}{
				"target_ip": tt.ip,
				"repeat":    3,
			}

			err := task.ValidateParams(params)

			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected validation error for IP '%s'", tt.ip)
				} else if tt.errMsg != "" && !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.errMsg)) {
					t.Errorf("Expected error message to contain '%s', got '%s'", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error for valid IP '%s', got: %v", tt.ip, err)
				}
			}
		})
	}
}

func TestValidation_RepeatParameterBoundaries(t *testing.T) {
	task := &PingTask{}

	tests := []struct {
		name    string
		repeat  interface{}
		wantErr bool
		errMsg  string
	}{
		// Valid repeat values
		{
			name:    "valid_repeat_min",
			repeat:  1,
			wantErr: false,
		},
		{
			name:    "valid_repeat_typical",
			repeat:  3,
			wantErr: false,
		},
		{
			name:    "valid_repeat_max_reasonable",
			repeat:  100,
			wantErr: false,
		},
		{
			name:    "valid_repeat_as_string",
			repeat:  "5",
			wantErr: false,
		},
		{
			name:    "valid_repeat_as_float",
			repeat:  5.0,
			wantErr: false,
		},

		// Invalid repeat values
		{
			name:    "invalid_repeat_zero",
			repeat:  0,
			wantErr: true,
			errMsg:  "positive",
		},
		{
			name:    "invalid_repeat_negative",
			repeat:  -1,
			wantErr: true,
			errMsg:  "positive",
		},
		{
			name:    "invalid_repeat_too_large",
			repeat:  10000,
			wantErr: true,
			errMsg:  "too large",
		},
		{
			name:    "invalid_repeat_string_non_numeric",
			repeat:  "abc",
			wantErr: true,
			errMsg:  "invalid",
		},
		{
			name:    "invalid_repeat_string_empty",
			repeat:  "",
			wantErr: true,
			errMsg:  "invalid",
		},
		{
			name:    "invalid_repeat_float_decimal",
			repeat:  3.5,
			wantErr: true,
			errMsg:  "integer",
		},
		{
			name:    "invalid_repeat_nil",
			repeat:  nil,
			wantErr: true,
			errMsg:  "required",
		},
		{
			name:    "invalid_repeat_boolean",
			repeat:  true,
			wantErr: true,
			errMsg:  "invalid",
		},
		{
			name:    "invalid_repeat_array",
			repeat:  []int{1, 2, 3},
			wantErr: true,
			errMsg:  "invalid",
		},
		{
			name:    "invalid_repeat_map",
			repeat:  map[string]int{"count": 3},
			wantErr: true,
			errMsg:  "invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]interface{}{
				"target_ip": "192.168.1.1",
				"repeat":    tt.repeat,
			}

			err := task.ValidateParams(params)

			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected validation error for repeat value '%v'", tt.repeat)
				} else if tt.errMsg != "" && !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.errMsg)) {
					t.Errorf("Expected error message to contain '%s', got '%s'", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error for valid repeat '%v', got: %v", tt.repeat, err)
				}
			}
		})
	}
}

func TestValidation_TimeoutParameterFormats(t *testing.T) {
	task := &PingTask{}

	tests := []struct {
		name    string
		timeout interface{}
		wantErr bool
		errMsg  string
	}{
		// Valid timeout values
		{
			name:    "valid_timeout_duration",
			timeout: 5 * time.Second,
			wantErr: false,
		},
		{
			name:    "valid_timeout_string_seconds",
			timeout: "10s",
			wantErr: false,
		},
		{
			name:    "valid_timeout_string_minutes",
			timeout: "2m",
			wantErr: false,
		},
		{
			name:    "valid_timeout_string_milliseconds",
			timeout: "500ms",
			wantErr: false,
		},
		{
			name:    "valid_timeout_int_seconds",
			timeout: 30,
			wantErr: false,
		},
		{
			name:    "valid_timeout_float_seconds",
			timeout: 10.5,
			wantErr: false,
		},

		// Invalid timeout values
		{
			name:    "invalid_timeout_negative_duration",
			timeout: -5 * time.Second,
			wantErr: true,
			errMsg:  "positive",
		},
		{
			name:    "invalid_timeout_zero_duration",
			timeout: 0 * time.Second,
			wantErr: true,
			errMsg:  "positive",
		},
		{
			name:    "invalid_timeout_too_large",
			timeout: 24 * time.Hour,
			wantErr: true,
			errMsg:  "too large",
		},
		{
			name:    "invalid_timeout_negative_int",
			timeout: -10,
			wantErr: true,
			errMsg:  "positive",
		},
		{
			name:    "invalid_timeout_zero_int",
			timeout: 0,
			wantErr: true,
			errMsg:  "positive",
		},
		{
			name:    "invalid_timeout_string_invalid_format",
			timeout: "invalid",
			wantErr: true,
			errMsg:  "invalid",
		},
		{
			name:    "invalid_timeout_string_negative",
			timeout: "-5s",
			wantErr: true,
			errMsg:  "positive",
		},
		{
			name:    "invalid_timeout_boolean",
			timeout: true,
			wantErr: true,
			errMsg:  "invalid",
		},
		{
			name:    "invalid_timeout_array",
			timeout: []string{"5s"},
			wantErr: true,
			errMsg:  "invalid",
		},
		{
			name:    "invalid_timeout_nil",
			timeout: nil,
			wantErr: true,
			errMsg:  "required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]interface{}{
				"target_ip": "192.168.1.1",
				"repeat":    3,
				"timeout":   tt.timeout,
			}

			err := task.ValidateParams(params)

			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected validation error for timeout value '%v'", tt.timeout)
				} else if tt.errMsg != "" && !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.errMsg)) {
					t.Errorf("Expected error message to contain '%s', got '%s'", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error for valid timeout '%v', got: %v", tt.timeout, err)
				}
			}
		})
	}
}

func TestValidation_BatchIPArrays(t *testing.T) {
	task := &PingTask{}

	tests := []struct {
		name      string
		targetIPs interface{}
		wantErr   bool
		errMsg    string
	}{
		// Valid batch IP arrays
		{
			name:      "valid_batch_multiple_ips",
			targetIPs: []string{"192.168.1.1", "192.168.1.2", "192.168.1.3"},
			wantErr:   false,
		},
		{
			name:      "valid_batch_single_ip",
			targetIPs: []string{"192.168.1.1"},
			wantErr:   false,
		},
		{
			name:      "valid_batch_different_networks",
			targetIPs: []string{"10.0.0.1", "172.16.0.1", "192.168.1.1"},
			wantErr:   false,
		},

		// Invalid batch IP arrays
		{
			name:      "invalid_batch_empty_array",
			targetIPs: []string{},
			wantErr:   true,
			errMsg:    "empty",
		},
		{
			name:      "invalid_batch_nil_array",
			targetIPs: nil,
			wantErr:   true,
			errMsg:    "required",
		},
		{
			name:      "invalid_batch_not_array",
			targetIPs: "192.168.1.1",
			wantErr:   true,
			errMsg:    "array",
		},
		{
			name:      "invalid_batch_mixed_valid_invalid",
			targetIPs: []string{"192.168.1.1", "invalid.ip", "192.168.1.2"},
			wantErr:   true,
			errMsg:    "invalid",
		},
		{
			name:      "invalid_batch_all_invalid",
			targetIPs: []string{"invalid1", "invalid2", "invalid3"},
			wantErr:   true,
			errMsg:    "invalid",
		},
		{
			name:      "invalid_batch_empty_strings",
			targetIPs: []string{"", "", ""},
			wantErr:   true,
			errMsg:    "empty",
		},
		{
			name:      "invalid_batch_duplicates",
			targetIPs: []string{"192.168.1.1", "192.168.1.1", "192.168.1.2"},
			wantErr:   true,
			errMsg:    "duplicate",
		},
		{
			name:      "invalid_batch_too_many",
			targetIPs: generateIPArray(1000), // Assuming max is less than 1000
			wantErr:   true,
			errMsg:    "too many",
		},
		{
			name:      "invalid_batch_interface_array",
			targetIPs: []interface{}{"192.168.1.1", 123, true},
			wantErr:   true,
			errMsg:    "string",
		},
		{
			name:      "invalid_batch_nested_array",
			targetIPs: [][]string{{"192.168.1.1"}, {"192.168.1.2"}},
			wantErr:   true,
			errMsg:    "string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]interface{}{
				"target_ips": tt.targetIPs,
				"repeat":     3,
			}

			err := task.ValidateParams(params)

			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected validation error for target_ips '%v'", tt.targetIPs)
				} else if tt.errMsg != "" && !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.errMsg)) {
					t.Errorf("Expected error message to contain '%s', got '%s'", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error for valid target_ips '%v', got: %v", tt.targetIPs, err)
				}
			}
		})
	}
}

func TestValidation_ParameterCombinations(t *testing.T) {
	task := &PingTask{}

	tests := []struct {
		name    string
		params  map[string]interface{}
		wantErr bool
		errMsg  string
	}{
		// Valid combinations
		{
			name: "valid_single_ip_with_all_params",
			params: map[string]interface{}{
				"target_ip": "192.168.1.1",
				"repeat":    5,
				"timeout":   "10s",
			},
			wantErr: false,
		},
		{
			name: "valid_batch_ips_with_all_params",
			params: map[string]interface{}{
				"target_ips": []string{"192.168.1.1", "192.168.1.2"},
				"repeat":     3,
				"timeout":    "5s",
			},
			wantErr: false,
		},
		{
			name: "valid_single_ip_minimal",
			params: map[string]interface{}{
				"target_ip": "192.168.1.1",
				"repeat":    1,
			},
			wantErr: false,
		},

		// Invalid combinations
		{
			name: "invalid_both_single_and_batch",
			params: map[string]interface{}{
				"target_ip":  "192.168.1.1",
				"target_ips": []string{"192.168.1.2", "192.168.1.3"},
				"repeat":     3,
			},
			wantErr: true,
			errMsg:  "both",
		},
		{
			name: "invalid_neither_single_nor_batch",
			params: map[string]interface{}{
				"repeat": 3,
			},
			wantErr: true,
			errMsg:  "required",
		},
		{
			name: "invalid_missing_repeat",
			params: map[string]interface{}{
				"target_ip": "192.168.1.1",
			},
			wantErr: true,
			errMsg:  "required",
		},
		{
			name:    "invalid_empty_params",
			params:  map[string]interface{}{},
			wantErr: true,
			errMsg:  "required",
		},
		{
			name:    "invalid_nil_params",
			params:  nil,
			wantErr: true,
			errMsg:  "required",
		},
		{
			name: "invalid_extra_unknown_params",
			params: map[string]interface{}{
				"target_ip":      "192.168.1.1",
				"repeat":         3,
				"unknown_param1": "value1",
				"unknown_param2": 123,
			},
			wantErr: true,
			errMsg:  "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := task.ValidateParams(tt.params)

			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected validation error for params %v", tt.params)
				} else if tt.errMsg != "" && !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.errMsg)) {
					t.Errorf("Expected error message to contain '%s', got '%s'", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error for valid params %v, got: %v", tt.params, err)
				}
			}
		})
	}
}

func TestValidation_PlatformSpecificParameters(t *testing.T) {
	_ = &PingTask{}

	tests := []struct {
		name     string
		platform connection.Platform
		params   map[string]interface{}
		wantErr  bool
		errMsg   string
	}{
		// Cisco-specific parameters
		{
			name:     "valid_cisco_enable_password",
			platform: connection.PlatformCiscoIOSXE,
			params: map[string]interface{}{
				"target_ip":       "192.168.1.1",
				"repeat":          3,
				"enable_password": "cisco123",
			},
			wantErr: false,
		},
		{
			name:     "valid_cisco_vrf",
			platform: connection.PlatformCiscoIOSXE,
			params: map[string]interface{}{
				"target_ip": "192.168.1.1",
				"repeat":    3,
				"vrf":       "management",
			},
			wantErr: false,
		},

		// Huawei-specific parameters
		{
			name:     "valid_huawei_vpn_instance",
			platform: connection.PlatformHuaweiVRP,
			params: map[string]interface{}{
				"target_ip":    "192.168.1.1",
				"repeat":       3,
				"vpn_instance": "MGMT_VPN",
			},
			wantErr: false,
		},

		// Invalid platform-specific parameters
		{
			name:     "invalid_huawei_param_on_cisco",
			platform: connection.PlatformCiscoIOSXE,
			params: map[string]interface{}{
				"target_ip":    "192.168.1.1",
				"repeat":       3,
				"vpn_instance": "MGMT_VPN", // Huawei-specific on Cisco
			},
			wantErr: true,
			errMsg:  "unsupported",
		},
		{
			name:     "invalid_cisco_param_on_huawei",
			platform: connection.PlatformHuaweiVRP,
			params: map[string]interface{}{
				"target_ip":       "192.168.1.1",
				"repeat":          3,
				"enable_password": "cisco123", // Cisco-specific on Huawei
			},
			wantErr: true,
			errMsg:  "unsupported",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create task context with platform information
			_ = TaskContext{
				Platform: tt.platform,
				Params:   tt.params,
			}

			// For this test, we'll use the adapter to validate platform-specific params
			adapter := GetAdapter(tt.platform)
			if adapter == nil {
				t.Fatalf("No adapter found for platform %s", tt.platform)
			}

			err := adapter.ValidatePlatformParams(tt.params)

			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected platform validation error for params %v on %s", tt.params, tt.platform)
				} else if tt.errMsg != "" && !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.errMsg)) {
					t.Errorf("Expected error message to contain '%s', got '%s'", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Expected no platform error for valid params %v on %s, got: %v", tt.params, tt.platform, err)
				}
			}
		})
	}
}

func TestValidation_UnicodeAndSpecialCharacters(t *testing.T) {
	task := &PingTask{}

	tests := []struct {
		name   string
		params map[string]interface{}
		desc   string
	}{
		{
			name: "unicode_in_string_params",
			params: map[string]interface{}{
				"target_ip":       "192.168.1.1",
				"repeat":          3,
				"enable_password": "пароль123", // Cyrillic characters
			},
			desc: "Should handle unicode characters in string parameters",
		},
		{
			name: "special_chars_in_params",
			params: map[string]interface{}{
				"target_ip":       "192.168.1.1",
				"repeat":          3,
				"enable_password": "!@#$%^&*()",
			},
			desc: "Should handle special characters in parameters",
		},
		{
			name: "emoji_in_params",
			params: map[string]interface{}{
				"target_ip":       "192.168.1.1",
				"repeat":          3,
				"enable_password": "password😀",
			},
			desc: "Should handle emoji characters in parameters",
		},
		{
			name: "control_chars_in_params",
			params: map[string]interface{}{
				"target_ip":       "192.168.1.1",
				"repeat":          3,
				"enable_password": "pass\000word\t\n",
			},
			desc: "Should handle control characters in parameters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log(tt.desc)

			// Basic validation should not fail due to unicode/special chars
			err := task.ValidateParams(tt.params)
			if err != nil {
				t.Logf("Validation error (may be expected): %v", err)
			}

			// Test that the task can build commands with these parameters
			ctx := TaskContext{
				TaskType:    "ping",
				Platform:    connection.PlatformCiscoIOSXE,
				Protocol:    connection.ProtocolScrapli,
				CommandType: connection.CommandTypeInteractiveEvent,
				Params:      tt.params,
				Ctx:         context.Background(),
			}

			cmd, err := task.BuildCommand(ctx)
			if err != nil {
				t.Logf("Command build error (may be expected): %v", err)
			} else {
				t.Logf("Command built successfully with special characters")
				payloadStr := fmt.Sprintf("%v", cmd.Payload)
				t.Logf("Command type: %s, Payload length: %d", cmd.Type, len(payloadStr))
			}
		})
	}
}

func TestValidation_LargeParameterValues(t *testing.T) {
	task := &PingTask{}

	// Test extremely large parameter values
	largeString := strings.Repeat("x", 1000000) // 1MB string
	largeArray := make([]string, 10000)
	for i := 0; i < 10000; i++ {
		largeArray[i] = fmt.Sprintf("192.168.%d.%d", (i/254)%256, (i%254)+1)
	}

	tests := []struct {
		name   string
		params map[string]interface{}
		desc   string
	}{
		{
			name: "huge_string_parameter",
			params: map[string]interface{}{
				"target_ip":       "192.168.1.1",
				"repeat":          3,
				"enable_password": largeString,
			},
			desc: "Should handle extremely large string parameters gracefully",
		},
		{
			name: "huge_array_parameter",
			params: map[string]interface{}{
				"target_ips": largeArray[:100], // Use reasonable subset
				"repeat":     3,
			},
			desc: "Should handle large IP arrays",
		},
		{
			name: "deeply_nested_parameters",
			params: map[string]interface{}{
				"target_ip": "192.168.1.1",
				"repeat":    3,
				"metadata": map[string]interface{}{
					"level1": map[string]interface{}{
						"level2": map[string]interface{}{
							"level3": map[string]interface{}{
								"data": largeString[:1000], // Smaller subset
							},
						},
					},
				},
			},
			desc: "Should handle deeply nested parameter structures",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log(tt.desc)

			start := time.Now()
			err := task.ValidateParams(tt.params)
			duration := time.Since(start)

			t.Logf("Validation took %v", duration)

			if duration > 5*time.Second {
				t.Errorf("Validation took too long: %v", duration)
			}

			if err != nil {
				t.Logf("Validation error (may be expected for large values): %v", err)
			}
		})
	}
}

func TestValidation_ContextCancellation(t *testing.T) {
	task := &PingTask{}

	// Create a context that gets cancelled quickly
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	params := map[string]interface{}{
		"target_ip": "192.168.1.1",
		"repeat":    3,
	}

	taskCtx := TaskContext{
		TaskType:    "ping",
		Platform:    connection.PlatformCiscoIOSXE,
		Protocol:    connection.ProtocolScrapli,
		CommandType: connection.CommandTypeInteractiveEvent,
		Params:      params,
		Ctx:         ctx,
	}

	// Validation should complete quickly and not be affected by context cancellation
	start := time.Now()
	err := task.ValidateParams(params)
	duration := time.Since(start)

	if duration > 100*time.Millisecond {
		t.Errorf("Validation should be quick, took %v", duration)
	}

	if err != nil {
		t.Logf("Validation error: %v", err)
	}

	// Building command might be affected by context cancellation
	_, err = task.BuildCommand(taskCtx)
	if err != nil {
		t.Logf("Command build error (expected due to context cancellation): %v", err)
	}
}

// Helper functions

func generateIPArray(count int) []string {
	ips := make([]string, count)
	for i := 0; i < count; i++ {
		ips[i] = fmt.Sprintf("192.168.%d.%d", (i/254)%256, (i%254)+1)
	}
	return ips
}

func isValidIP(ip string) bool {
	return net.ParseIP(ip) != nil
}

// Benchmark validation performance

func BenchmarkValidation_SingleIP(b *testing.B) {
	task := &PingTask{}
	params := map[string]interface{}{
		"target_ip": "192.168.1.1",
		"repeat":    3,
		"timeout":   "5s",
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = task.ValidateParams(params)
	}
}

func BenchmarkValidation_BatchIP(b *testing.B) {
	task := &PingTask{}
	params := map[string]interface{}{
		"target_ips": []string{
			"192.168.1.1", "192.168.1.2", "192.168.1.3",
			"192.168.1.4", "192.168.1.5", "192.168.1.6",
			"192.168.1.7", "192.168.1.8", "192.168.1.9",
			"192.168.1.10",
		},
		"repeat":  3,
		"timeout": "5s",
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = task.ValidateParams(params)
	}
}

func BenchmarkValidation_LargeParameterSet(b *testing.B) {
	task := &PingTask{}

	// Create large IP array
	ips := make([]string, 100)
	for i := 0; i < 100; i++ {
		ips[i] = fmt.Sprintf("192.168.%d.%d", (i/254)%256, (i%254)+1)
	}

	params := map[string]interface{}{
		"target_ips":      ips,
		"repeat":          5,
		"timeout":         "10s",
		"enable_password": "password123",
		"vrf":             "management",
		"metadata": map[string]interface{}{
			"description": "Large parameter set test",
			"tags":        []string{"test", "benchmark", "large"},
			"config": map[string]interface{}{
				"retry_count": 3,
				"backoff":     "exponential",
			},
		},
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = task.ValidateParams(params)
	}
}

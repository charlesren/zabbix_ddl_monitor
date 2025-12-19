package task

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charlesren/zabbix_ddl_monitor/connection"
)

func TestGetAdapter(t *testing.T) {
	tests := []struct {
		name     string
		platform connection.Platform
		expected string
	}{
		{
			name:     "cisco_iosxe_adapter",
			platform: connection.PlatformCiscoIOSXE,
			expected: "*task.CiscoIOSXEAdapter",
		},
		{
			name:     "cisco_iosxr_adapter",
			platform: connection.PlatformCiscoIOSXR,
			expected: "*task.CiscoIOSXRAdapter",
		},
		{
			name:     "cisco_nxos_adapter",
			platform: connection.PlatformCiscoNXOS,
			expected: "*task.CiscoNXOSAdapter",
		},
		{
			name:     "huawei_vrp_adapter",
			platform: connection.PlatformHuaweiVRP,
			expected: "*task.HuaweiVRPAdapter",
		},
		{
			name:     "h3c_comware_adapter",
			platform: connection.PlatformH3CComware,
			expected: "*task.H3CComwareAdapter",
		},
		{
			name:     "unknown_platform_fallback",
			platform: "unknown_platform",
			expected: "*task.GenericAdapter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := GetAdapter(tt.platform)
			if adapter == nil {
				t.Fatal("Expected non-nil adapter")
			}

			actualType := getTypeName(adapter)
			if actualType != tt.expected {
				t.Errorf("Expected adapter type %s, got %s", tt.expected, actualType)
			}
		})
	}
}

func TestRegisterAdapter(t *testing.T) {
	// Save original adapters
	originalAdapters := make(map[connection.Platform]PlatformAdapter)
	for k, v := range adapters {
		originalAdapters[k] = v
	}

	// Restore original adapters after test
	defer func() {
		adapters = originalAdapters
	}()

	// Register custom adapter
	customPlatform := connection.Platform("custom_platform")
	customAdapter := &GenericAdapter{}

	RegisterAdapter(customPlatform, customAdapter)

	// Verify registration
	retrievedAdapter := GetAdapter(customPlatform)
	if retrievedAdapter != customAdapter {
		t.Error("Expected custom adapter to be retrieved")
	}

	// Verify it's in the adapters map
	if adapters[customPlatform] != customAdapter {
		t.Error("Expected custom adapter to be in adapters map")
	}
}

func TestGenericAdapter_NormalizeParams(t *testing.T) {
	adapter := &GenericAdapter{}

	tests := []struct {
		name     string
		params   map[string]interface{}
		expected map[string]interface{}
	}{
		{
			name:   "empty_params",
			params: map[string]interface{}{},
			expected: map[string]interface{}{
				"repeat":  5,
				"timeout": 2 * time.Second,
			},
		},
		{
			name: "existing_repeat",
			params: map[string]interface{}{
				"repeat": 10,
			},
			expected: map[string]interface{}{
				"repeat":  10,
				"timeout": 2 * time.Second,
			},
		},
		{
			name: "existing_timeout",
			params: map[string]interface{}{
				"timeout": 5 * time.Second,
			},
			expected: map[string]interface{}{
				"repeat":  5,
				"timeout": 5 * time.Second,
			},
		},
		{
			name: "all_params_exist",
			params: map[string]interface{}{
				"repeat":    8,
				"timeout":   3 * time.Second,
				"target_ip": "192.168.1.1",
			},
			expected: map[string]interface{}{
				"repeat":    8,
				"timeout":   3 * time.Second,
				"target_ip": "192.168.1.1",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := adapter.NormalizeParams(tt.params)

			for key, expectedValue := range tt.expected {
				if actualValue, ok := result[key]; !ok {
					t.Errorf("Expected key '%s' to exist", key)
				} else if actualValue != expectedValue {
					t.Errorf("Expected %s=%v, got %v", key, expectedValue, actualValue)
				}
			}
		})
	}
}

func TestGenericAdapter_ConvertOutput(t *testing.T) {
	adapter := &GenericAdapter{}

	tests := []struct {
		name     string
		raw      string
		expected map[string]interface{}
	}{
		{
			name: "success_output",
			raw:  "PING successful",
			expected: map[string]interface{}{
				"raw_output": "PING successful",
				"success":    true,
				"parsed_by":  "generic_adapter",
			},
		},
		{
			name: "failed_output",
			raw:  "PING failed",
			expected: map[string]interface{}{
				"raw_output": "PING failed",
				"success":    false,
				"parsed_by":  "generic_adapter",
			},
		},
		{
			name: "neutral_output",
			raw:  "PING completed",
			expected: map[string]interface{}{
				"raw_output": "PING completed",
				"success":    true,
				"parsed_by":  "generic_adapter",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := adapter.ConvertOutput(tt.raw)

			for key, expectedValue := range tt.expected {
				if actualValue, ok := result[key]; !ok {
					t.Errorf("Expected key '%s' to exist", key)
				} else if actualValue != expectedValue {
					t.Errorf("Expected %s=%v, got %v", key, expectedValue, actualValue)
				}
			}
		})
	}
}

func TestGenericAdapter_GetCommandTemplate(t *testing.T) {
	adapter := &GenericAdapter{}

	tests := []struct {
		name        string
		taskType    TaskType
		commandType connection.CommandType
		wantErr     bool
		expected    string
	}{
		{
			name:        "ping_task",
			taskType:    "ping",
			commandType: connection.CommandTypeCommands,
			wantErr:     false,
			expected:    "ping {{.target_ip}}",
		},
		{
			name:        "unsupported_task",
			taskType:    "traceroute",
			commandType: connection.CommandTypeCommands,
			wantErr:     true,
			expected:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			template, err := adapter.GetCommandTemplate(tt.taskType, tt.commandType)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error but got: %v", err)
				}
				if template != tt.expected {
					t.Errorf("Expected template '%s', got '%s'", tt.expected, template)
				}
			}
		})
	}
}

func TestGenericAdapter_ValidatePlatformParams(t *testing.T) {
	adapter := &GenericAdapter{}

	// Generic adapter should not do additional validation
	params := map[string]interface{}{
		"invalid_param": "invalid_value",
	}

	err := adapter.ValidatePlatformParams(params)
	if err != nil {
		t.Errorf("Generic adapter should not validate params, got error: %v", err)
	}
}

func TestCiscoIOSXEAdapter_NormalizeParams(t *testing.T) {
	adapter := &CiscoIOSXEAdapter{}

	tests := []struct {
		name     string
		params   map[string]interface{}
		expected map[string]interface{}
	}{
		{
			name:   "empty_params",
			params: map[string]interface{}{},
			expected: map[string]interface{}{
				"timeout": 2 * time.Second,
				"repeat":  5,
			},
		},
		{
			name: "existing_params",
			params: map[string]interface{}{
				"timeout": 10 * time.Second,
				"repeat":  3,
			},
			expected: map[string]interface{}{
				"timeout": 10 * time.Second,
				"repeat":  3,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := adapter.NormalizeParams(tt.params)

			for key, expectedValue := range tt.expected {
				if actualValue, ok := result[key]; !ok {
					t.Errorf("Expected key '%s' to exist", key)
				} else if actualValue != expectedValue {
					t.Errorf("Expected %s=%v, got %v", key, expectedValue, actualValue)
				}
			}
		})
	}
}

func TestCiscoIOSXEAdapter_ConvertOutput(t *testing.T) {
	adapter := &CiscoIOSXEAdapter{}

	tests := []struct {
		name     string
		raw      string
		expected map[string]interface{}
	}{
		{
			name: "100_percent_success",
			raw:  "Success rate is 100 percent (5/5), round-trip min/avg/max = 1/2/4 ms",
			expected: map[string]interface{}{
				"success":      true,
				"success_rate": "100%",
			},
		},
		{
			name: "0_percent_success",
			raw:  "Success rate is 0 percent (0/5)",
			expected: map[string]interface{}{
				"success":      false,
				"success_rate": "0%",
			},
		},
		{
			name: "partial_success",
			raw:  "Success rate is 80 percent (4/5)",
			expected: map[string]interface{}{
				"success": true,
			},
		},
		{
			name: "no_success_rate",
			raw:  "Some other output",
			expected: map[string]interface{}{
				"success": true, // Default when no "failed" found
			},
		},
		{
			name: "failed_output",
			raw:  "Command failed",
			expected: map[string]interface{}{
				"success": false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := adapter.ConvertOutput(tt.raw)

			// Check required fields
			if _, ok := result["raw_output"]; !ok {
				t.Error("Expected raw_output field")
			}
			if _, ok := result["parsed_by"]; !ok {
				t.Error("Expected parsed_by field")
			}

			// Check expected fields
			for key, expectedValue := range tt.expected {
				if actualValue, ok := result[key]; !ok {
					t.Errorf("Expected key '%s' to exist", key)
				} else if actualValue != expectedValue {
					t.Errorf("Expected %s=%v, got %v", key, expectedValue, actualValue)
				}
			}
		})
	}
}

func TestCiscoIOSXEAdapter_ValidatePlatformParams(t *testing.T) {
	adapter := &CiscoIOSXEAdapter{}

	tests := []struct {
		name    string
		params  map[string]interface{}
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid_params",
			params:  map[string]interface{}{"repeat": 5, "timeout": 10 * time.Second},
			wantErr: false,
		},
		{
			name:    "repeat_too_small",
			params:  map[string]interface{}{"repeat": 0},
			wantErr: true,
			errMsg:  "repeat count must be between 1 and 2000",
		},
		{
			name:    "repeat_too_large",
			params:  map[string]interface{}{"repeat": 2001},
			wantErr: true,
			errMsg:  "repeat count must be between 1 and 2000",
		},
		{
			name:    "timeout_too_small",
			params:  map[string]interface{}{"timeout": 500 * time.Millisecond},
			wantErr: true,
			errMsg:  "timeout must be between 1 and 36 seconds",
		},
		{
			name:    "timeout_too_large",
			params:  map[string]interface{}{"timeout": 37 * time.Second},
			wantErr: true,
			errMsg:  "timeout must be between 1 and 36 seconds",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := adapter.ValidatePlatformParams(tt.params)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got none")
				} else if tt.errMsg != "" && !containsSubstring(err.Error(), tt.errMsg) {
					t.Errorf("Expected error to contain '%s', got '%s'", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error but got: %v", err)
				}
			}
		})
	}
}

func TestHuaweiVRPAdapter_NormalizeParams(t *testing.T) {
	adapter := &HuaweiVRPAdapter{}

	tests := []struct {
		name     string
		params   map[string]interface{}
		expected map[string]interface{}
	}{
		{
			name:   "empty_params",
			params: map[string]interface{}{},
			expected: map[string]interface{}{
				"repeat":  4, // Huawei default
				"timeout": 1 * time.Second,
			},
		},
		{
			name: "existing_params",
			params: map[string]interface{}{
				"repeat":  6,
				"timeout": 3 * time.Second,
			},
			expected: map[string]interface{}{
				"repeat":  6,
				"timeout": 3 * time.Second,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := adapter.NormalizeParams(tt.params)

			for key, expectedValue := range tt.expected {
				if actualValue, ok := result[key]; !ok {
					t.Errorf("Expected key '%s' to exist", key)
				} else if actualValue != expectedValue {
					t.Errorf("Expected %s=%v, got %v", key, expectedValue, actualValue)
				}
			}
		})
	}
}

func TestHuaweiVRPAdapter_ConvertOutput(t *testing.T) {
	adapter := &HuaweiVRPAdapter{}

	tests := []struct {
		name     string
		raw      string
		expected map[string]interface{}
	}{
		{
			name: "0_percent_loss",
			raw:  "5 packet(s) transmitted\n5 packet(s) received\n0% packet loss",
			expected: map[string]interface{}{
				"success":     true,
				"packet_loss": "0%",
			},
		},
		{
			name: "100_percent_loss",
			raw:  "5 packet(s) transmitted\n0 packet(s) received\n100% packet loss",
			expected: map[string]interface{}{
				"success":     false,
				"packet_loss": "100%",
			},
		},
		{
			name: "no_packet_loss_info",
			raw:  "Some other output",
			expected: map[string]interface{}{
				"success": true, // Default when no specific patterns found
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := adapter.ConvertOutput(tt.raw)

			// Check required fields
			if _, ok := result["raw_output"]; !ok {
				t.Error("Expected raw_output field")
			}
			if _, ok := result["parsed_by"]; !ok {
				t.Error("Expected parsed_by field")
			}

			// Check expected fields
			for key, expectedValue := range tt.expected {
				if actualValue, ok := result[key]; !ok {
					t.Errorf("Expected key '%s' to exist", key)
				} else if actualValue != expectedValue {
					t.Errorf("Expected %s=%v, got %v", key, expectedValue, actualValue)
				}
			}
		})
	}
}

func TestHuaweiVRPAdapter_ValidatePlatformParams(t *testing.T) {
	adapter := &HuaweiVRPAdapter{}

	tests := []struct {
		name    string
		params  map[string]interface{}
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid_params",
			params:  map[string]interface{}{"repeat": 5},
			wantErr: false,
		},
		{
			name:    "repeat_too_small",
			params:  map[string]interface{}{"repeat": 0},
			wantErr: true,
			errMsg:  "repeat count must be between 1 and 20",
		},
		{
			name:    "repeat_too_large",
			params:  map[string]interface{}{"repeat": 21},
			wantErr: true,
			errMsg:  "repeat count must be between 1 and 20",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := adapter.ValidatePlatformParams(tt.params)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got none")
				} else if tt.errMsg != "" && !containsSubstring(err.Error(), tt.errMsg) {
					t.Errorf("Expected error to contain '%s', got '%s'", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error but got: %v", err)
				}
			}
		})
	}
}

func TestCiscoIOSXRAdapter_GetCommandTemplate(t *testing.T) {
	adapter := &CiscoIOSXRAdapter{}

	template, err := adapter.GetCommandTemplate("ping", connection.CommandTypeCommands)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	expected := "ping {{.target_ip}} repeat {{.repeat}} timeout {{.timeout_seconds}}"
	if template != expected {
		t.Errorf("Expected template '%s', got '%s'", expected, template)
	}

	// Test unsupported task type
	_, err = adapter.GetCommandTemplate("unsupported", connection.CommandTypeCommands)
	if err == nil {
		t.Error("Expected error for unsupported task type")
	}
}

func TestCiscoNXOSAdapter_ConvertOutput(t *testing.T) {
	adapter := &CiscoNXOSAdapter{}

	tests := []struct {
		name     string
		raw      string
		expected bool
	}{
		{
			name:     "100_percent_success",
			raw:      "5 packets transmitted, 5 packets received, 100.00% success",
			expected: true,
		},
		{
			name:     "partial_success",
			raw:      "5 packets transmitted, 3 packets received, 60.00% success",
			expected: false,
		},
		{
			name:     "no_success_info",
			raw:      "Some other output",
			expected: true, // Default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := adapter.ConvertOutput(tt.raw)

			if success, ok := result["success"]; !ok {
				t.Error("Expected success field")
			} else if success != tt.expected {
				t.Errorf("Expected success=%t, got %t", tt.expected, success)
			}
		})
	}
}

func TestH3CComwareAdapter_GetCommandTemplate(t *testing.T) {
	adapter := &H3CComwareAdapter{}

	template, err := adapter.GetCommandTemplate("ping", connection.CommandTypeCommands)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	expected := "ping -c {{.repeat}} -t {{.timeout_seconds}} {{.target_ip}}"
	if template != expected {
		t.Errorf("Expected template '%s', got '%s'", expected, template)
	}

	_, err = adapter.GetCommandTemplate("invalid", connection.CommandTypeCommands)
	if err == nil {
		t.Error("Expected error for invalid task type")
	}
}

func TestH3CComwareAdapter_NormalizeParams(t *testing.T) {
	adapter := &H3CComwareAdapter{}

	// 测试默认值
	params := map[string]interface{}{
		"target_ip": "8.8.8.8",
	}
	normalized := adapter.NormalizeParams(params)

	if repeat, ok := normalized["repeat"]; !ok || repeat != 5 {
		t.Errorf("Expected repeat=5, got %v", repeat)
	}

	if timeout, ok := normalized["timeout"]; !ok || timeout != 2*time.Second {
		t.Errorf("Expected timeout=2s, got %v", timeout)
	}

	if timeoutSeconds, ok := normalized["timeout_seconds"]; !ok || timeoutSeconds != 2 {
		t.Errorf("Expected timeout_seconds=2, got %v", timeoutSeconds)
	}

	// 测试已有参数不被覆盖
	paramsWithValues := map[string]interface{}{
		"target_ip": "8.8.8.8",
		"repeat":    3,
		"timeout":   5 * time.Second,
	}
	normalizedWithValues := adapter.NormalizeParams(paramsWithValues)

	if repeat, ok := normalizedWithValues["repeat"]; !ok || repeat != 3 {
		t.Errorf("Expected repeat=3, got %v", repeat)
	}

	if timeout, ok := normalizedWithValues["timeout"]; !ok || timeout != 5*time.Second {
		t.Errorf("Expected timeout=5s, got %v", timeout)
	}

	if timeoutSeconds, ok := normalizedWithValues["timeout_seconds"]; !ok || timeoutSeconds != 5 {
		t.Errorf("Expected timeout_seconds=5, got %v", timeoutSeconds)
	}
}

func TestH3CComwareAdapter_ConvertOutput(t *testing.T) {
	adapter := &H3CComwareAdapter{}

	tests := []struct {
		name     string
		output   string
		expected bool
	}{
		{
			name:     "success_output",
			output:   "5 packet(s) transmitted, 5 packet(s) received, 0.0% packet loss",
			expected: true,
		},
		{
			name:     "failure_output",
			output:   "5 packet(s) transmitted, 0 packet(s) received, 100.0% packet loss",
			expected: false,
		},
		{
			name:     "partial_loss_output",
			output:   "5 packet(s) transmitted, 3 packet(s) received, 40.0% packet loss",
			expected: false,
		},
		{
			name:     "timeout_output",
			output:   "Request time out",
			expected: false,
		},
		{
			name:     "unreachable_output",
			output:   "Destination host unreachable",
			expected: false,
		},
		{
			name:     "no_route_output",
			output:   "No route to host",
			expected: false,
		},
		{
			name:     "generic_success",
			output:   "Ping successful",
			expected: true,
		},
		{
			name:     "generic_failure",
			output:   "Ping failed",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := adapter.ConvertOutput(tt.output)
			if success, ok := result["success"].(bool); !ok || success != tt.expected {
				t.Errorf("Expected success=%v, got %v", tt.expected, result["success"])
			}
		})
	}
}

func TestH3CComwareAdapter_ValidatePlatformParams(t *testing.T) {
	adapter := &H3CComwareAdapter{}

	tests := []struct {
		name    string
		params  map[string]interface{}
		wantErr bool
	}{
		{
			name: "valid_params",
			params: map[string]interface{}{
				"repeat":  5,
				"timeout": 2 * time.Second,
			},
			wantErr: false,
		},
		{
			name: "valid_min_values",
			params: map[string]interface{}{
				"repeat":  1,
				"timeout": 1 * time.Second,
			},
			wantErr: false,
		},
		{
			name: "valid_max_values",
			params: map[string]interface{}{
				"repeat":  100,
				"timeout": 10 * time.Second,
			},
			wantErr: false,
		},
		{
			name: "invalid_repeat_too_low",
			params: map[string]interface{}{
				"repeat": 0,
			},
			wantErr: true,
		},
		{
			name: "invalid_repeat_too_high",
			params: map[string]interface{}{
				"repeat": 101,
			},
			wantErr: true,
		},
		{
			name: "invalid_timeout_too_low",
			params: map[string]interface{}{
				"timeout": 500 * time.Millisecond,
			},
			wantErr: true,
		},
		{
			name: "invalid_timeout_too_high",
			params: map[string]interface{}{
				"timeout": 11 * time.Second,
			},
			wantErr: true,
		},
		{
			name: "invalid_repeat_type",
			params: map[string]interface{}{
				"repeat": "five",
			},
			wantErr: false, // 类型错误不会被ValidatePlatformParams捕获
		},
		{
			name: "invalid_timeout_type",
			params: map[string]interface{}{
				"timeout": "two seconds",
			},
			wantErr: false, // 类型错误不会被ValidatePlatformParams捕获
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := adapter.ValidatePlatformParams(tt.params)
			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error but got: %v", err)
				}
			}
		})
	}
}

// Helper functions
func getTypeName(obj interface{}) string {
	return fmt.Sprintf("%T", obj)
}

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr ||
			(len(s) > len(substr) &&
				(s[:len(substr)] == substr ||
					s[len(s)-len(substr):] == substr ||
					strings.Contains(s, substr))))
}

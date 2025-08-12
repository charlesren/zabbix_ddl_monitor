package task

import (
	"errors"
	"fmt"
	"testing"
)

func TestTaskError_Error(t *testing.T) {
	tests := []struct {
		name     string
		taskErr  *TaskError
		expected string
	}{
		{
			name: "error_without_cause",
			taskErr: &TaskError{
				Code:    ErrCodeInvalidParams,
				Message: "invalid parameters provided",
			},
			expected: "[INVALID_PARAMS] invalid parameters provided",
		},
		{
			name: "error_with_cause",
			taskErr: &TaskError{
				Code:    ErrCodeConnectionFailed,
				Message: "connection to device failed",
				Cause:   errors.New("network timeout"),
			},
			expected: "[CONNECTION_FAILED] connection to device failed: network timeout",
		},
		{
			name: "error_with_empty_message",
			taskErr: &TaskError{
				Code:    ErrCodeSystemError,
				Message: "",
			},
			expected: "[SYSTEM_ERROR] ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.taskErr.Error()
			if result != tt.expected {
				t.Errorf("Expected error string '%s', got '%s'", tt.expected, result)
			}
		})
	}
}

func TestTaskError_Unwrap(t *testing.T) {
	originalErr := errors.New("original error")

	taskErr := &TaskError{
		Code:    ErrCodeExecutionFailed,
		Message: "execution failed",
		Cause:   originalErr,
	}

	unwrapped := taskErr.Unwrap()
	if unwrapped != originalErr {
		t.Errorf("Expected unwrapped error to be %v, got %v", originalErr, unwrapped)
	}

	// Test without cause
	taskErrNoCause := &TaskError{
		Code:    ErrCodeExecutionFailed,
		Message: "execution failed",
	}

	unwrappedNil := taskErrNoCause.Unwrap()
	if unwrappedNil != nil {
		t.Errorf("Expected unwrapped error to be nil, got %v", unwrappedNil)
	}
}

func TestNewTaskError(t *testing.T) {
	code := ErrCodeInvalidParams
	message := "test error message"

	taskErr := NewTaskError(code, message)

	if taskErr == nil {
		t.Fatal("Expected non-nil TaskError")
	}

	if taskErr.Code != code {
		t.Errorf("Expected code %s, got %s", code, taskErr.Code)
	}

	if taskErr.Message != message {
		t.Errorf("Expected message '%s', got '%s'", message, taskErr.Message)
	}

	if taskErr.Details == nil {
		t.Error("Expected Details map to be initialized")
	}

	if len(taskErr.Details) != 0 {
		t.Error("Expected Details map to be empty initially")
	}

	if taskErr.Cause != nil {
		t.Error("Expected Cause to be nil")
	}
}

func TestNewTaskErrorWithCause(t *testing.T) {
	code := ErrCodeConnectionFailed
	message := "connection error"
	cause := errors.New("network unreachable")

	taskErr := NewTaskErrorWithCause(code, message, cause)

	if taskErr == nil {
		t.Fatal("Expected non-nil TaskError")
	}

	if taskErr.Code != code {
		t.Errorf("Expected code %s, got %s", code, taskErr.Code)
	}

	if taskErr.Message != message {
		t.Errorf("Expected message '%s', got '%s'", message, taskErr.Message)
	}

	if taskErr.Cause != cause {
		t.Errorf("Expected cause %v, got %v", cause, taskErr.Cause)
	}

	if taskErr.Details == nil {
		t.Error("Expected Details map to be initialized")
	}
}

func TestNewTaskErrorWithDetails(t *testing.T) {
	code := ErrCodeParamValidation
	message := "parameter validation failed"
	details := map[string]interface{}{
		"parameter": "target_ip",
		"value":     "invalid_ip",
		"reason":    "not a valid IP address",
	}

	taskErr := NewTaskErrorWithDetails(code, message, details)

	if taskErr == nil {
		t.Fatal("Expected non-nil TaskError")
	}

	if taskErr.Code != code {
		t.Errorf("Expected code %s, got %s", code, taskErr.Code)
	}

	if taskErr.Message != message {
		t.Errorf("Expected message '%s', got '%s'", message, taskErr.Message)
	}

	if taskErr.Details == nil {
		t.Fatal("Expected Details map to be non-nil")
	}

	for key, expectedValue := range details {
		if actualValue, ok := taskErr.Details[key]; !ok {
			t.Errorf("Expected detail key '%s' to exist", key)
		} else if actualValue != expectedValue {
			t.Errorf("Expected detail %s=%v, got %v", key, expectedValue, actualValue)
		}
	}
}

func TestTaskError_AddDetail(t *testing.T) {
	taskErr := NewTaskError(ErrCodeInvalidParams, "test error")

	// Add detail and verify chaining
	result := taskErr.AddDetail("key1", "value1")
	if result != taskErr {
		t.Error("Expected AddDetail to return the same TaskError instance")
	}

	if len(taskErr.Details) != 1 {
		t.Errorf("Expected 1 detail, got %d", len(taskErr.Details))
	}

	if value, ok := taskErr.Details["key1"]; !ok {
		t.Error("Expected key1 to exist in details")
	} else if value != "value1" {
		t.Errorf("Expected key1=value1, got %v", value)
	}

	// Add multiple details
	taskErr.AddDetail("key2", 42).AddDetail("key3", true)

	if len(taskErr.Details) != 3 {
		t.Errorf("Expected 3 details, got %d", len(taskErr.Details))
	}

	expectedDetails := map[string]interface{}{
		"key1": "value1",
		"key2": 42,
		"key3": true,
	}

	for key, expectedValue := range expectedDetails {
		if actualValue, ok := taskErr.Details[key]; !ok {
			t.Errorf("Expected detail key '%s' to exist", key)
		} else if actualValue != expectedValue {
			t.Errorf("Expected detail %s=%v, got %v", key, expectedValue, actualValue)
		}
	}
}

func TestTaskError_WithCause(t *testing.T) {
	taskErr := NewTaskError(ErrCodeExecutionFailed, "execution failed")
	originalErr := errors.New("root cause")

	// Test chaining
	result := taskErr.WithCause(originalErr)
	if result != taskErr {
		t.Error("Expected WithCause to return the same TaskError instance")
	}

	if taskErr.Cause != originalErr {
		t.Errorf("Expected cause %v, got %v", originalErr, taskErr.Cause)
	}
}

func TestTaskError_IsCode(t *testing.T) {
	taskErr := NewTaskError(ErrCodeInvalidParams, "test error")

	// Test matching code
	if !taskErr.IsCode(ErrCodeInvalidParams) {
		t.Error("Expected IsCode to return true for matching code")
	}

	// Test non-matching code
	if taskErr.IsCode(ErrCodeConnectionFailed) {
		t.Error("Expected IsCode to return false for non-matching code")
	}
}

func TestIsTaskError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "task_error",
			err:      NewTaskError(ErrCodeInvalidParams, "test"),
			expected: true,
		},
		{
			name:     "standard_error",
			err:      errors.New("standard error"),
			expected: false,
		},
		{
			name:     "nil_error",
			err:      nil,
			expected: false,
		},
		{
			name:     "wrapped_task_error",
			err:      fmt.Errorf("wrapped: %w", NewTaskError(ErrCodeInvalidParams, "test")),
			expected: false, // Direct type check, doesn't unwrap
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsTaskError(tt.err)
			if result != tt.expected {
				t.Errorf("Expected IsTaskError=%t, got %t", tt.expected, result)
			}
		})
	}
}

func TestGetTaskError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected *TaskError
	}{
		{
			name:     "task_error",
			err:      NewTaskError(ErrCodeInvalidParams, "test"),
			expected: NewTaskError(ErrCodeInvalidParams, "test"),
		},
		{
			name:     "standard_error",
			err:      errors.New("standard error"),
			expected: nil,
		},
		{
			name:     "nil_error",
			err:      nil,
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetTaskError(tt.err)

			if tt.expected == nil {
				if result != nil {
					t.Errorf("Expected nil TaskError, got %v", result)
				}
			} else {
				if result == nil {
					t.Fatal("Expected non-nil TaskError")
				}
				if result.Code != tt.expected.Code {
					t.Errorf("Expected code %s, got %s", tt.expected.Code, result.Code)
				}
				if result.Message != tt.expected.Message {
					t.Errorf("Expected message '%s', got '%s'", tt.expected.Message, result.Message)
				}
			}
		})
	}
}

func TestIsErrorCode(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		code     TaskErrorCode
		expected bool
	}{
		{
			name:     "matching_code",
			err:      NewTaskError(ErrCodeInvalidParams, "test"),
			code:     ErrCodeInvalidParams,
			expected: true,
		},
		{
			name:     "non_matching_code",
			err:      NewTaskError(ErrCodeInvalidParams, "test"),
			code:     ErrCodeConnectionFailed,
			expected: false,
		},
		{
			name:     "standard_error",
			err:      errors.New("standard error"),
			code:     ErrCodeInvalidParams,
			expected: false,
		},
		{
			name:     "nil_error",
			err:      nil,
			code:     ErrCodeInvalidParams,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsErrorCode(tt.err, tt.code)
			if result != tt.expected {
				t.Errorf("Expected IsErrorCode=%t, got %t", tt.expected, result)
			}
		})
	}
}

func TestPredefinedErrors(t *testing.T) {
	tests := []struct {
		name         string
		err          *TaskError
		expectedCode TaskErrorCode
	}{
		{
			name:         "ErrTaskNotFound",
			err:          ErrTaskNotFound,
			expectedCode: ErrCodeTaskNotFound,
		},
		{
			name:         "ErrInvalidParams",
			err:          ErrInvalidParams,
			expectedCode: ErrCodeInvalidParams,
		},
		{
			name:         "ErrMissingParams",
			err:          ErrMissingParams,
			expectedCode: ErrCodeMissingParams,
		},
		{
			name:         "ErrConnectionFailed",
			err:          ErrConnectionFailed,
			expectedCode: ErrCodeConnectionFailed,
		},
		{
			name:         "ErrExecutionTimeout",
			err:          ErrExecutionTimeout,
			expectedCode: ErrCodeTimeout,
		},
		{
			name:         "ErrUnsupportedPlatform",
			err:          ErrUnsupportedPlatform,
			expectedCode: ErrCodeUnsupportedPlatform,
		},
		{
			name:         "ErrParseOutputFailed",
			err:          ErrParseOutputFailed,
			expectedCode: ErrCodeParseOutputFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err == nil {
				t.Fatal("Expected non-nil predefined error")
			}

			if tt.err.Code != tt.expectedCode {
				t.Errorf("Expected code %s, got %s", tt.expectedCode, tt.err.Code)
			}

			if tt.err.Message == "" {
				t.Error("Expected non-empty message")
			}

			if tt.err.Details == nil {
				t.Error("Expected Details map to be initialized")
			}
		})
	}
}

func TestErrorConstructorFunctions(t *testing.T) {
	t.Run("ErrInvalidParam", func(t *testing.T) {
		paramName := "target_ip"
		reason := "not a valid IP address"

		err := ErrInvalidParam(paramName, reason)

		if err.Code != ErrCodeParamValidation {
			t.Errorf("Expected code %s, got %s", ErrCodeParamValidation, err.Code)
		}

		expectedMsg := fmt.Sprintf("invalid parameter '%s': %s", paramName, reason)
		if err.Message != expectedMsg {
			t.Errorf("Expected message '%s', got '%s'", expectedMsg, err.Message)
		}

		if param, ok := err.Details["parameter"]; !ok || param != paramName {
			t.Errorf("Expected parameter detail '%s', got %v", paramName, param)
		}

		if r, ok := err.Details["reason"]; !ok || r != reason {
			t.Errorf("Expected reason detail '%s', got %v", reason, r)
		}
	})

	t.Run("ErrMissingParam", func(t *testing.T) {
		paramName := "target_ip"

		err := ErrMissingParam(paramName)

		if err.Code != ErrCodeMissingParams {
			t.Errorf("Expected code %s, got %s", ErrCodeMissingParams, err.Code)
		}

		expectedMsg := fmt.Sprintf("missing required parameter: %s", paramName)
		if err.Message != expectedMsg {
			t.Errorf("Expected message '%s', got '%s'", expectedMsg, err.Message)
		}

		if param, ok := err.Details["parameter"]; !ok || param != paramName {
			t.Errorf("Expected parameter detail '%s', got %v", paramName, param)
		}
	})

	t.Run("ErrCommandFailed", func(t *testing.T) {
		command := "ping 192.168.1.1"
		output := "network unreachable"
		cause := errors.New("timeout")

		err := ErrCommandFailed(command, output, cause)

		if err.Code != ErrCodeCommandFailed {
			t.Errorf("Expected code %s, got %s", ErrCodeCommandFailed, err.Code)
		}

		expectedMsg := fmt.Sprintf("command execution failed: %s", command)
		if err.Message != expectedMsg {
			t.Errorf("Expected message '%s', got '%s'", expectedMsg, err.Message)
		}

		if err.Cause != cause {
			t.Errorf("Expected cause %v, got %v", cause, err.Cause)
		}

		if cmd, ok := err.Details["command"]; !ok || cmd != command {
			t.Errorf("Expected command detail '%s', got %v", command, cmd)
		}

		if out, ok := err.Details["output"]; !ok || out != output {
			t.Errorf("Expected output detail '%s', got %v", output, out)
		}
	})

	t.Run("ErrPlatformNotSupported", func(t *testing.T) {
		platform := "cisco_iosxe"
		taskType := TaskType("ping")

		err := ErrPlatformNotSupported(platform, taskType)

		if err.Code != ErrCodeUnsupportedPlatform {
			t.Errorf("Expected code %s, got %s", ErrCodeUnsupportedPlatform, err.Code)
		}

		expectedMsg := fmt.Sprintf("platform '%s' not supported for task '%s'", platform, taskType)
		if err.Message != expectedMsg {
			t.Errorf("Expected message '%s', got '%s'", expectedMsg, err.Message)
		}

		if plat, ok := err.Details["platform"]; !ok || plat != platform {
			t.Errorf("Expected platform detail '%s', got %v", platform, plat)
		}

		if tt, ok := err.Details["task_type"]; !ok || tt != taskType {
			t.Errorf("Expected task_type detail '%s', got %v", taskType, tt)
		}
	})

	t.Run("ErrCommandTypeNotSupported", func(t *testing.T) {
		commandType := "interactive_event"
		platform := "cisco_iosxe"

		err := ErrCommandTypeNotSupported(commandType, platform)

		if err.Code != ErrCodeUnsupportedCommand {
			t.Errorf("Expected code %s, got %s", ErrCodeUnsupportedCommand, err.Code)
		}

		expectedMsg := fmt.Sprintf("command type '%s' not supported on platform '%s'", commandType, platform)
		if err.Message != expectedMsg {
			t.Errorf("Expected message '%s', got '%s'", expectedMsg, err.Message)
		}

		if ct, ok := err.Details["command_type"]; !ok || ct != commandType {
			t.Errorf("Expected command_type detail '%s', got %v", commandType, ct)
		}

		if plat, ok := err.Details["platform"]; !ok || plat != platform {
			t.Errorf("Expected platform detail '%s', got %v", platform, plat)
		}
	})
}

func TestErrorClassificationFunctions(t *testing.T) {
	t.Run("IsRecoverableError", func(t *testing.T) {
		tests := []struct {
			name     string
			err      error
			expected bool
		}{
			{
				name:     "connection_timeout",
				err:      NewTaskError(ErrCodeConnectionTimeout, "timeout"),
				expected: true,
			},
			{
				name:     "execution_timeout",
				err:      NewTaskError(ErrCodeTimeout, "timeout"),
				expected: true,
			},
			{
				name:     "resource_limit",
				err:      NewTaskError(ErrCodeResourceLimit, "limit exceeded"),
				expected: true,
			},
			{
				name:     "invalid_params",
				err:      NewTaskError(ErrCodeInvalidParams, "invalid"),
				expected: false,
			},
			{
				name:     "standard_error",
				err:      errors.New("standard error"),
				expected: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := IsRecoverableError(tt.err)
				if result != tt.expected {
					t.Errorf("Expected IsRecoverableError=%t, got %t", tt.expected, result)
				}
			})
		}
	})

	t.Run("IsConfigError", func(t *testing.T) {
		tests := []struct {
			name     string
			err      error
			expected bool
		}{
			{
				name:     "invalid_params",
				err:      NewTaskError(ErrCodeInvalidParams, "invalid"),
				expected: true,
			},
			{
				name:     "missing_params",
				err:      NewTaskError(ErrCodeMissingParams, "missing"),
				expected: true,
			},
			{
				name:     "param_validation",
				err:      NewTaskError(ErrCodeParamValidation, "validation failed"),
				expected: true,
			},
			{
				name:     "connection_failed",
				err:      NewTaskError(ErrCodeConnectionFailed, "failed"),
				expected: false,
			},
			{
				name:     "standard_error",
				err:      errors.New("standard error"),
				expected: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := IsConfigError(tt.err)
				if result != tt.expected {
					t.Errorf("Expected IsConfigError=%t, got %t", tt.expected, result)
				}
			})
		}
	})

	t.Run("IsSystemError", func(t *testing.T) {
		tests := []struct {
			name     string
			err      error
			expected bool
		}{
			{
				name:     "system_error",
				err:      NewTaskError(ErrCodeSystemError, "system failure"),
				expected: true,
			},
			{
				name:     "internal_error",
				err:      NewTaskError(ErrCodeInternalError, "internal failure"),
				expected: true,
			},
			{
				name:     "resource_limit",
				err:      NewTaskError(ErrCodeResourceLimit, "limit exceeded"),
				expected: true,
			},
			{
				name:     "invalid_params",
				err:      NewTaskError(ErrCodeInvalidParams, "invalid"),
				expected: false,
			},
			{
				name:     "standard_error",
				err:      errors.New("standard error"),
				expected: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := IsSystemError(tt.err)
				if result != tt.expected {
					t.Errorf("Expected IsSystemError=%t, got %t", tt.expected, result)
				}
			})
		}
	})
}

func TestTaskErrorChaining(t *testing.T) {
	// Test method chaining
	err := NewTaskError(ErrCodeInvalidParams, "test error").
		AddDetail("key1", "value1").
		AddDetail("key2", "value2").
		WithCause(errors.New("root cause"))

	if err.Code != ErrCodeInvalidParams {
		t.Error("Code should be preserved during chaining")
	}

	if err.Message != "test error" {
		t.Error("Message should be preserved during chaining")
	}

	if len(err.Details) != 2 {
		t.Error("Details should be accumulated during chaining")
	}

	if err.Cause == nil {
		t.Error("Cause should be set during chaining")
	}

	// Verify specific details
	if err.Details["key1"] != "value1" {
		t.Error("First detail should be preserved")
	}

	if err.Details["key2"] != "value2" {
		t.Error("Second detail should be preserved")
	}
}

func TestTaskErrorCodes(t *testing.T) {
	// Test that all error codes are defined and unique
	codes := []TaskErrorCode{
		ErrCodeInvalidParams,
		ErrCodeMissingParams,
		ErrCodeParamValidation,
		ErrCodeConnectionFailed,
		ErrCodeConnectionTimeout,
		ErrCodeAuthFailed,
		ErrCodeExecutionFailed,
		ErrCodeCommandFailed,
		ErrCodeTimeout,
		ErrCodeUnsupportedPlatform,
		ErrCodeUnsupportedCommand,
		ErrCodeParseOutputFailed,
		ErrCodeInvalidOutput,
		ErrCodeSystemError,
		ErrCodeInternalError,
		ErrCodeResourceLimit,
		ErrCodeTaskNotFound,
		ErrCodeTaskExists,
		ErrCodeRegistryError,
	}

	// Check that all codes are non-empty
	for _, code := range codes {
		if string(code) == "" {
			t.Errorf("Error code should not be empty")
		}
	}

	// Check for uniqueness
	seen := make(map[TaskErrorCode]bool)
	for _, code := range codes {
		if seen[code] {
			t.Errorf("Duplicate error code found: %s", code)
		}
		seen[code] = true
	}

	// Verify we have the expected number of unique codes
	expectedCount := len(codes)
	if len(seen) != expectedCount {
		t.Errorf("Expected %d unique error codes, got %d", expectedCount, len(seen))
	}
}

package task

import "fmt"

// TaskErrorCode 任务错误码类型
type TaskErrorCode string

const (
	// 参数相关错误
	ErrCodeInvalidParams   TaskErrorCode = "INVALID_PARAMS"
	ErrCodeMissingParams   TaskErrorCode = "MISSING_PARAMS"
	ErrCodeParamValidation TaskErrorCode = "PARAM_VALIDATION"

	// 连接相关错误
	ErrCodeConnectionFailed  TaskErrorCode = "CONNECTION_FAILED"
	ErrCodeConnectionTimeout TaskErrorCode = "CONNECTION_TIMEOUT"
	ErrCodeAuthFailed        TaskErrorCode = "AUTH_FAILED"

	// 执行相关错误
	ErrCodeExecutionFailed     TaskErrorCode = "EXECUTION_FAILED"
	ErrCodeCommandFailed       TaskErrorCode = "COMMAND_FAILED"
	ErrCodeTimeout             TaskErrorCode = "TIMEOUT"
	ErrCodeUnsupportedPlatform TaskErrorCode = "UNSUPPORTED_PLATFORM"
	ErrCodeUnsupportedCommand  TaskErrorCode = "UNSUPPORTED_COMMAND"

	// 输出解析错误
	ErrCodeParseOutputFailed TaskErrorCode = "PARSE_OUTPUT_FAILED"
	ErrCodeInvalidOutput     TaskErrorCode = "INVALID_OUTPUT"

	// 系统相关错误
	ErrCodeSystemError   TaskErrorCode = "SYSTEM_ERROR"
	ErrCodeInternalError TaskErrorCode = "INTERNAL_ERROR"
	ErrCodeResourceLimit TaskErrorCode = "RESOURCE_LIMIT"

	// 任务注册相关错误
	ErrCodeTaskNotFound  TaskErrorCode = "TASK_NOT_FOUND"
	ErrCodeTaskExists    TaskErrorCode = "TASK_EXISTS"
	ErrCodeRegistryError TaskErrorCode = "REGISTRY_ERROR"
)

// TaskError 任务执行错误
type TaskError struct {
	Code    TaskErrorCode          `json:"code"`
	Message string                 `json:"message"`
	Details map[string]interface{} `json:"details,omitempty"`
	Cause   error                  `json:"-"` // 原始错误，不参与JSON序列化
}

// Error 实现error接口
func (e *TaskError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// Unwrap 支持errors.Unwrap
func (e *TaskError) Unwrap() error {
	return e.Cause
}

// NewTaskError 创建新的任务错误
func NewTaskError(code TaskErrorCode, message string) *TaskError {
	return &TaskError{
		Code:    code,
		Message: message,
		Details: make(map[string]interface{}),
	}
}

// NewTaskErrorWithCause 创建带原因的任务错误
func NewTaskErrorWithCause(code TaskErrorCode, message string, cause error) *TaskError {
	return &TaskError{
		Code:    code,
		Message: message,
		Details: make(map[string]interface{}),
		Cause:   cause,
	}
}

// NewTaskErrorWithDetails 创建带详细信息的任务错误
func NewTaskErrorWithDetails(code TaskErrorCode, message string, details map[string]interface{}) *TaskError {
	return &TaskError{
		Code:    code,
		Message: message,
		Details: details,
	}
}

// AddDetail 添加错误详细信息
func (e *TaskError) AddDetail(key string, value interface{}) *TaskError {
	if e.Details == nil {
		e.Details = make(map[string]interface{})
	}
	e.Details[key] = value
	return e
}

// WithCause 设置原因错误
func (e *TaskError) WithCause(cause error) *TaskError {
	e.Cause = cause
	return e
}

// IsCode 检查错误码是否匹配
func (e *TaskError) IsCode(code TaskErrorCode) bool {
	return e.Code == code
}

// IsType 检查是否为指定类型的错误
func IsTaskError(err error) bool {
	_, ok := err.(*TaskError)
	return ok
}

// GetTaskError 获取TaskError，如果不是则返回nil
func GetTaskError(err error) *TaskError {
	if taskErr, ok := err.(*TaskError); ok {
		return taskErr
	}
	return nil
}

// IsErrorCode 检查错误是否为指定错误码
func IsErrorCode(err error, code TaskErrorCode) bool {
	if taskErr := GetTaskError(err); taskErr != nil {
		return taskErr.IsCode(code)
	}
	return false
}

// 预定义的常用错误
var (
	ErrTaskNotFound        = NewTaskError(ErrCodeTaskNotFound, "task not found")
	ErrInvalidParams       = NewTaskError(ErrCodeInvalidParams, "invalid parameters")
	ErrMissingParams       = NewTaskError(ErrCodeMissingParams, "missing required parameters")
	ErrConnectionFailed    = NewTaskError(ErrCodeConnectionFailed, "connection failed")
	ErrExecutionTimeout    = NewTaskError(ErrCodeTimeout, "execution timeout")
	ErrUnsupportedPlatform = NewTaskError(ErrCodeUnsupportedPlatform, "unsupported platform")
	ErrParseOutputFailed   = NewTaskError(ErrCodeParseOutputFailed, "failed to parse output")
)

// 错误构造函数
func ErrInvalidParam(paramName string, reason string) *TaskError {
	return NewTaskErrorWithDetails(ErrCodeParamValidation,
		fmt.Sprintf("invalid parameter '%s': %s", paramName, reason),
		map[string]interface{}{
			"parameter": paramName,
			"reason":    reason,
		})
}

func ErrMissingParam(paramName string) *TaskError {
	return NewTaskErrorWithDetails(ErrCodeMissingParams,
		fmt.Sprintf("missing required parameter: %s", paramName),
		map[string]interface{}{
			"parameter": paramName,
		})
}

func ErrCommandFailed(command string, output string, cause error) *TaskError {
	return NewTaskErrorWithCause(ErrCodeCommandFailed,
		fmt.Sprintf("command execution failed: %s", command),
		cause).AddDetail("command", command).AddDetail("output", output)
}

func ErrPlatformNotSupported(platform string, taskType TaskType) *TaskError {
	return NewTaskErrorWithDetails(ErrCodeUnsupportedPlatform,
		fmt.Sprintf("platform '%s' not supported for task '%s'", platform, taskType),
		map[string]interface{}{
			"platform":  platform,
			"task_type": taskType,
		})
}

func ErrCommandTypeNotSupported(commandType string, platform string) *TaskError {
	return NewTaskErrorWithDetails(ErrCodeUnsupportedCommand,
		fmt.Sprintf("command type '%s' not supported on platform '%s'", commandType, platform),
		map[string]interface{}{
			"command_type": commandType,
			"platform":     platform,
		})
}

// 错误分类函数
func IsRecoverableError(err error) bool {
	if taskErr := GetTaskError(err); taskErr != nil {
		switch taskErr.Code {
		case ErrCodeConnectionTimeout, ErrCodeTimeout, ErrCodeResourceLimit:
			return true
		default:
			return false
		}
	}
	return false
}

func IsConfigError(err error) bool {
	if taskErr := GetTaskError(err); taskErr != nil {
		switch taskErr.Code {
		case ErrCodeInvalidParams, ErrCodeMissingParams, ErrCodeParamValidation:
			return true
		default:
			return false
		}
	}
	return false
}

func IsSystemError(err error) bool {
	if taskErr := GetTaskError(err); taskErr != nil {
		switch taskErr.Code {
		case ErrCodeSystemError, ErrCodeInternalError, ErrCodeResourceLimit:
			return true
		default:
			return false
		}
	}
	return false
}

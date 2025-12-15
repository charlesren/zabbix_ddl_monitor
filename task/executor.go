package task

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charlesren/ylog"
	"github.com/charlesren/zabbix_ddl_monitor/connection"
	"github.com/scrapli/scrapligo/channel"
)

const (
	logModule = "executor"
)

type (
	ExecutorFunc func(Task, connection.ProtocolDriver, TaskContext) (Result, error)
	Middleware   func(ExecutorFunc) ExecutorFunc
)

type Executor struct {
	core     ExecutorFunc
	callback func(Result, error)
}

// task/executor.go
func (e *Executor) coreExecute(task Task, conn connection.ProtocolDriver, ctx TaskContext) (Result, error) {
	ylog.Infof(logModule, "starting task %s on %s (%s) with params: %+v", ctx.TaskType, ctx.Platform, ctx.Protocol, ctx.Params)
	start := time.Now()
	if conn == nil {
		ylog.Errorf(logModule, "connection driver is nil for task %s on %s", ctx.TaskType, ctx.Platform)
		return Result{Error: "connection driver is nil"}, fmt.Errorf("connection driver is nil")
	}

	cmd, err := task.BuildCommand(ctx)
	if err != nil {
		ylog.Errorf(logModule, "failed to build command for %s on %s: %v", ctx.TaskType, ctx.Platform, err)
		return Result{
			Success: false,
			Error:   err.Error(),
			Data:    map[string]interface{}{"status": StatusExecutionError},
		}, err
	}
	ylog.Debugf(logModule, "built command for %s: type=%s, payload=%T", ctx.TaskType, cmd.Type, cmd.Payload)

	// 详细记录命令内容
	switch v := cmd.Payload.(type) {
	case []string:
		ylog.Debugf(logModule, "sending commands to %s: %v", ctx.Platform, v)
	case []*channel.SendInteractiveEvent:
		for i, event := range v {
			ylog.Debugf(logModule, "interactive event %d to %s: input=%s, response=%s",
				i, ctx.Platform, event.ChannelInput, event.ChannelResponse)
		}
	}

	// 类型安全转换
	var payload interface{}
	switch v := cmd.Payload.(type) {
	case []string:
		ylog.Debugf(logModule, "sending %d commands to %s: %v", len(v), ctx.Platform, v)
		payload = v
	case []*channel.SendInteractiveEvent:
		ylog.Debugf(logModule, "sending %d interactive events to %s", len(v), ctx.Platform)
		for i, event := range v {
			ylog.Debugf(logModule, "interactive event %d: channelInput=%s", i, event.ChannelInput)
		}
		payload = v
	default:
		ylog.Errorf(logModule, "unsupported payload type %T for task %s on %s", cmd.Payload, ctx.TaskType, ctx.Platform)
		return Result{
			Success: false,
			Error:   "unsupported payload type",
			Data:    map[string]interface{}{"status": StatusExecutionError},
		}, fmt.Errorf("unsupported payload type")
	}
	ylog.Debugf(logModule, "executing %s command on %s", cmd.Type, ctx.Platform)
	ylog.Debugf(logModule, "executing %s command on %s with payload: %+v", cmd.Type, ctx.Platform, payload)
	resp, err := conn.Execute(ctx.Ctx, &connection.ProtocolRequest{
		CommandType: cmd.Type,
		Payload:     payload,
	})

	if err != nil {
		duration := time.Since(start)
		ylog.Errorf(logModule, "execution failed for %s on %s after %v: %v", ctx.TaskType, ctx.Platform, duration, err)
		// 检查错误类型，设置相应的状态
		status := StatusExecutionError
		if ctx.Ctx != nil && ctx.Ctx.Err() == context.DeadlineExceeded {
			status = StatusCheckTimeout
		} else if strings.Contains(strings.ToLower(err.Error()), "connection") ||
			strings.Contains(strings.ToLower(err.Error()), "connect") ||
			strings.Contains(strings.ToLower(err.Error()), "timeout") {
			status = StatusConnectionError
		}
		return Result{
			Success: false,
			Error:   err.Error(),
			Data:    map[string]interface{}{"status": status},
		}, err
	}

	// 统一使用原始数据解析
	ylog.Debugf(logModule, "task %s completed on %s, received %d bytes of raw output", ctx.TaskType, ctx.Platform, len(resp.RawData))

	// 记录原始输出内容（截断过长的输出）
	if len(resp.RawData) > 0 {
		outputPreview := string(resp.RawData)
		if len(outputPreview) > 500 {
			outputPreview = outputPreview[:500] + "...[truncated]"
		}
		ylog.Debugf(logModule, "raw output from %s: %s", ctx.Platform, outputPreview)
	}
	result, err := task.ParseOutput(ctx, resp.RawData)
	duration := time.Since(start)

	if err != nil {
		ylog.Errorf(logModule, "failed to parse output for %s on %s after %v: %v", ctx.TaskType, ctx.Platform, duration, err)
		// 如果解析失败，确保有status字段
		if result.Data == nil {
			result.Data = make(map[string]interface{})
		}
		if _, hasStatus := result.Data["status"]; !hasStatus {
			result.Data["status"] = StatusParseFailed
		}
	} else if !result.Success {
		ylog.Warnf(logModule, "task %s on %s completed with failure after %v: %s", ctx.TaskType, ctx.Platform, duration, result.Error)
		// 确保失败的结果也有status字段
		if result.Data == nil {
			result.Data = make(map[string]interface{})
		}
		if _, hasStatus := result.Data["status"]; !hasStatus {
			result.Data["status"] = StatusExecutionError
		}
	} else {
		ylog.Infof(logModule, "task %s on %s completed successfully in %v", ctx.TaskType, ctx.Platform, duration)
		// 确保成功的结果也有status字段
		if result.Data == nil {
			result.Data = make(map[string]interface{})
		}
		if _, hasStatus := result.Data["status"]; !hasStatus {
			result.Data["status"] = StatusCheckFinished
		}
	}

	return result, err
}

func NewExecutor(callback func(Result, error), middlewares ...Middleware) *Executor {
	ylog.Debugf(logModule, "creating new executor with %d middlewares", len(middlewares))
	core := func(task Task, conn connection.ProtocolDriver, ctx TaskContext) (Result, error) {
		if err := task.ValidateParams(ctx.Params); err != nil {
			ylog.Errorf(logModule, "parameter validation failed for %s on %s: %v", ctx.TaskType, ctx.Platform, err)
			return Result{
				Success: false,
				Error:   err.Error(),
				Data:    map[string]interface{}{"status": StatusExecutionError},
			}, err
		}

		// 验证连接能力
		if err := validateCapability(conn, ctx); err != nil {
			ylog.Errorf(logModule, "capability validation failed for %s on %s: %v", ctx.TaskType, ctx.Platform, err)
			return Result{
				Success: false,
				Error:   err.Error(),
				Data:    map[string]interface{}{"status": StatusConnectionError},
			}, err
		}

		cmd, err := task.BuildCommand(ctx)
		if err != nil {
			ylog.Errorf(logModule, "failed to build command for %s on %s: %v", ctx.TaskType, ctx.Platform, err)
			return Result{
				Success: false,
				Error:   err.Error(),
				Data:    map[string]interface{}{"status": StatusExecutionError},
			}, err
		}
		ylog.Debugf(logModule, "built command for %s: type=%s", ctx.TaskType, cmd.Type)

		// 使用标准的ProtocolDriver接口
		var payload interface{}
		switch v := cmd.Payload.(type) {
		case []string:
			payload = v
		case []*channel.SendInteractiveEvent:
			payload = v
		default:
			return Result{
				Success: false,
				Error:   "unsupported payload type",
				Data:    map[string]interface{}{"status": StatusExecutionError},
			}, fmt.Errorf("unsupported payload type")
		}

		ylog.Debugf(logModule, "executing %s command on %s with payload: %+v", cmd.Type, ctx.Platform, payload)
		resp, err := conn.Execute(ctx.Ctx, &connection.ProtocolRequest{
			CommandType: cmd.Type,
			Payload:     payload,
		})
		if err != nil {
			ylog.Errorf(logModule, "execution failed for %s on %s: %v", ctx.TaskType, ctx.Platform, err)
			// 检查错误类型，设置相应的状态
			status := StatusExecutionError
			if ctx.Ctx != nil && ctx.Ctx.Err() == context.DeadlineExceeded {
				status = StatusCheckTimeout
			} else if strings.Contains(strings.ToLower(err.Error()), "connection") ||
				strings.Contains(strings.ToLower(err.Error()), "connect") ||
				strings.Contains(strings.ToLower(err.Error()), "timeout") {
				status = StatusConnectionError
			}
			return Result{
				Success: false,
				Error:   err.Error(),
				Data:    map[string]interface{}{"status": status},
			}, err
		}
		ylog.Debugf(logModule, "received %d bytes of response from %s", len(resp.RawData), ctx.Platform)

		// 记录响应内容详情
		if len(resp.RawData) > 0 {
			outputPreview := string(resp.RawData)
			if len(outputPreview) > 500 {
				outputPreview = outputPreview[:500] + "...[truncated]"
			}
			ylog.Debugf(logModule, "response content from %s: %s", ctx.Platform, outputPreview)
		}

		result, err := task.ParseOutput(ctx, resp.RawData)
		if err != nil {
			ylog.Errorf(logModule, "failed to parse output for %s on %s: %v", ctx.TaskType, ctx.Platform, err)
			// 如果解析失败，确保有status字段
			if result.Data == nil {
				result.Data = make(map[string]interface{})
			}
			if _, hasStatus := result.Data["status"]; !hasStatus {
				result.Data["status"] = StatusParseFailed
			}
		}
		return result, err
	}
	for i := len(middlewares) - 1; i >= 0; i-- {
		core = middlewares[i](core)
		ylog.Debugf(logModule, "applied middleware %d", i)
	}

	return &Executor{core: core, callback: callback}
}

func (e *Executor) Execute(task Task, conn connection.ProtocolDriver, ctx TaskContext) (Result, error) {
	ylog.Debugf(logModule, "executing task %s on %s via executor", ctx.TaskType, ctx.Platform)
	res, err := e.core(task, conn, ctx)
	if e.callback != nil {
		ylog.Debugf(logModule, "invoking callback for task %s on %s", ctx.TaskType, ctx.Platform)
		e.callback(res, err)
	}
	return res, err
}

// 中间件实现保持不变（需更新类型）
func WithTimeout(d time.Duration) Middleware {
	return func(next ExecutorFunc) ExecutorFunc {
		return func(task Task, conn connection.ProtocolDriver, ctx TaskContext) (Result, error) {
			timeoutCtx, cancel := context.WithTimeout(ctx.Ctx, d)
			defer cancel()
			return next(task, conn, ctx.WithContext(timeoutCtx))
		}
	}
}
func WithSmartTimeout(defaultTimeout time.Duration) Middleware {
	return func(next ExecutorFunc) ExecutorFunc {
		return func(task Task, conn connection.ProtocolDriver, ctx TaskContext) (Result, error) {
			// 优先使用任务参数中的超时
			var timeout time.Duration
			if timeoutParam, ok := ctx.Params["timeout"].(time.Duration); ok {
				timeout = timeoutParam
			} else {
				timeout = defaultTimeout
			}

			timeoutCtx, cancel := context.WithTimeout(ctx.Ctx, timeout)
			defer cancel()
			return next(task, conn, ctx.WithContext(timeoutCtx))
		}
	}
}

func WithRetry(maxRetries int, delay time.Duration) Middleware {
	return func(next ExecutorFunc) ExecutorFunc {
		return func(task Task, conn connection.ProtocolDriver, ctx TaskContext) (Result, error) {
			var lastErr error
			var lastResult Result

			for attempt := 0; attempt <= maxRetries; attempt++ {
				result, err := next(task, conn, ctx)
				if err == nil && result.Success {
					return result, nil
				}

				lastResult = result
				lastErr = err

				// Don't delay after the last attempt
				if attempt < maxRetries {
					time.Sleep(delay)
				}
			}

			return lastResult, lastErr
		}
	}
}

func WithLogging(logger func(level, message string)) Middleware {
	return func(next ExecutorFunc) ExecutorFunc {
		return func(task Task, conn connection.ProtocolDriver, ctx TaskContext) (Result, error) {
			start := time.Now()
			logger("INFO", fmt.Sprintf("Starting task %s for platform %s", ctx.TaskType, ctx.Platform))

			result, err := next(task, conn, ctx)

			duration := time.Since(start)
			if err != nil {
				logger("ERROR", fmt.Sprintf("Task %s failed after %v: %v", ctx.TaskType, duration, err))
			} else if result.Success {
				logger("INFO", fmt.Sprintf("Completed task %s successfully in %v", ctx.TaskType, duration))
			} else {
				logger("WARN", fmt.Sprintf("Task %s completed with failure in %v: %s", ctx.TaskType, duration, result.Error))
			}

			return result, err
		}
	}
}

func WithMetrics(collector func(taskType string, platform connection.Platform, success bool, duration time.Duration)) Middleware {
	return func(next ExecutorFunc) ExecutorFunc {
		return func(task Task, conn connection.ProtocolDriver, ctx TaskContext) (Result, error) {
			start := time.Now()

			result, err := next(task, conn, ctx)

			duration := time.Since(start)
			success := err == nil && result.Success
			collector(string(ctx.TaskType), ctx.Platform, success, duration)

			return result, err
		}
	}
}

// task/executor.go
func validateCapability(driver connection.ProtocolDriver, ctx TaskContext) error {
	caps := driver.GetCapability()
	ylog.Debugf(logModule, "validating capabilities for %s on %s: supported platforms=%v, command types=%v",
		ctx.TaskType, ctx.Platform, caps.PlatformSupport, caps.CommandTypesSupport)

	// 检查平台支持
	if !slices.Contains(caps.PlatformSupport, ctx.Platform) {
		err := fmt.Errorf("platform %s not supported (supported: %v)", ctx.Platform, caps.PlatformSupport)
		ylog.Errorf(logModule, "capability validation failed: %v", err)
		return err
	}

	// 检查命令类型支持
	if !caps.SupportsCommandType(ctx.CommandType) {
		err := fmt.Errorf("command type %s not supported", ctx.CommandType)
		ylog.Errorf(logModule, "capability validation failed: %v", err)
		return err
	}

	return nil
}

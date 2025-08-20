package task

import (
	"context"
	"fmt"
	"slices"
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
		return Result{Error: err.Error()}, err
	}
	ylog.Debugf(logModule, "built command for %s: type=%s, payload=%T", ctx.TaskType, cmd.Type, cmd.Payload)

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
		return Result{Error: "unsupported payload type"}, nil
	}
	ylog.Debugf(logModule, "executing %s command on %s", cmd.Type, ctx.Platform)
	resp, err := conn.Execute(ctx.Ctx, &connection.ProtocolRequest{
		CommandType: cmd.Type,
		Payload:     payload,
	})

	if err != nil {
		duration := time.Since(start)
		ylog.Errorf(logModule, "execution failed for %s on %s after %v: %v", ctx.TaskType, ctx.Platform, duration, err)
		return Result{Error: err.Error()}, err
	}

	// 统一使用原始数据解析
	ylog.Debugf(logModule, "task %s completed on %s, parsing %d bytes of output", ctx.TaskType, ctx.Platform, len(resp.RawData))
	result, err := task.ParseOutput(ctx, resp.RawData)
	duration := time.Since(start)

	if err != nil {
		ylog.Errorf(logModule, "failed to parse output for %s on %s after %v: %v", ctx.TaskType, ctx.Platform, duration, err)
	} else if !result.Success {
		ylog.Warnf(logModule, "task %s on %s completed with failure after %v: %s", ctx.TaskType, ctx.Platform, duration, result.Error)
	} else {
		ylog.Infof(logModule, "task %s on %s completed successfully in %v", ctx.TaskType, ctx.Platform, duration)
	}

	return result, err
}

func NewExecutor(callback func(Result, error), middlewares ...Middleware) *Executor {
	ylog.Debugf(logModule, "creating new executor with %d middlewares", len(middlewares))
	core := func(task Task, conn connection.ProtocolDriver, ctx TaskContext) (Result, error) {
		if err := task.ValidateParams(ctx.Params); err != nil {
			ylog.Errorf(logModule, "parameter validation failed for %s on %s: %v", ctx.TaskType, ctx.Platform, err)
			return Result{Error: err.Error()}, err
		}

		// 验证连接能力
		if err := validateCapability(conn, ctx); err != nil {
			ylog.Errorf(logModule, "capability validation failed for %s on %s: %v", ctx.TaskType, ctx.Platform, err)
			return Result{Error: err.Error()}, err
		}

		cmd, err := task.BuildCommand(ctx)
		if err != nil {
			ylog.Errorf(logModule, "failed to build command for %s on %s: %v", ctx.TaskType, ctx.Platform, err)
			return Result{Error: err.Error()}, err
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
			return Result{Error: "unsupported payload type"}, fmt.Errorf("unsupported payload type")
		}

		ylog.Debugf(logModule, "executing %s command on %s", cmd.Type, ctx.Platform)
		resp, err := conn.Execute(ctx.Ctx, &connection.ProtocolRequest{
			CommandType: cmd.Type,
			Payload:     payload,
		})
		if err != nil {
			ylog.Errorf(logModule, "execution failed for %s on %s: %v", ctx.TaskType, ctx.Platform, err)
			return Result{Error: err.Error()}, err
		}
		ylog.Debugf(logModule, "received %d bytes of response from %s", len(resp.RawData), ctx.Platform)

		result, err := task.ParseOutput(ctx, resp.RawData)
		if err != nil {
			ylog.Errorf(logModule, "failed to parse output for %s on %s: %v", ctx.TaskType, ctx.Platform, err)
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

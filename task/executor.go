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
	ylog.Debugf("executor", "executing task %s on %s (%s)", ctx.TaskType, ctx.Platform, ctx.Protocol)
	cmd, err := task.BuildCommand(ctx)
	if err != nil {
		ylog.Errorf("executor", "failed to build command for %s: %v", ctx.TaskType, err)
		return Result{Error: err.Error()}, err
	}

	// 类型安全转换
	var payload interface{}
	switch v := cmd.Payload.(type) {
	case []string:
		ylog.Debugf("executor", "sending commands: %v", v)
		payload = v
	case []*channel.SendInteractiveEvent:
		ylog.Debugf("executor", "sending interactive events: %d events", len(v))
		payload = v
	default:
		ylog.Errorf("executor", "unsupported payload type: %T", cmd.Payload)
		return Result{Error: "unsupported payload type"}, nil
	}
	resp, err := conn.Execute(&connection.ProtocolRequest{
		CommandType: cmd.Type,
		Payload:     payload,
	})

	if err != nil {
		ylog.Errorf("executor", "execution failed for %s: %v", ctx.TaskType, err)
		return Result{Error: err.Error()}, err
	}

	// 统一使用原始数据解析
	ylog.Debugf("executor", "task %s completed, parsing output", ctx.TaskType)
	return task.ParseOutput(ctx, resp.RawData)
}

func NewExecutor(callback func(Result, error), middlewares ...Middleware) *Executor {
	core := func(task Task, conn connection.ProtocolDriver, ctx TaskContext) (Result, error) {
		if err := task.ValidateParams(ctx.Params); err != nil {
			return Result{Error: err.Error()}, err
		}

		// 验证连接能力
		if err := validateCapability(conn, ctx); err != nil {
			return Result{Error: err.Error()}, err
		}

		cmd, err := task.BuildCommand(ctx)
		if err != nil {
			return Result{Error: err.Error()}, err
		}

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

		resp, err := conn.Execute(&connection.ProtocolRequest{
			CommandType: cmd.Type,
			Payload:     payload,
		})
		if err != nil {
			return Result{Error: err.Error()}, err
		}

		return task.ParseOutput(ctx, resp.RawData)
	}
	for i := len(middlewares) - 1; i >= 0; i-- {
		core = middlewares[i](core)
	}

	return &Executor{core: core, callback: callback}
}

func (e *Executor) Execute(task Task, conn connection.ProtocolDriver, ctx TaskContext) (Result, error) {
	res, err := e.core(task, conn, ctx)
	if e.callback != nil {
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

	// 检查平台支持
	if !slices.Contains(caps.PlatformSupport, ctx.Platform) {
		return fmt.Errorf("platform %s not supported", ctx.Platform)
	}

	// 检查命令类型支持
	if !caps.SupportsCommandType(ctx.CommandType) {
		return fmt.Errorf("command type %s not supported", ctx.CommandType)
	}

	return nil
}

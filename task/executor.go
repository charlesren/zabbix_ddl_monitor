package task

import (
	"context"
	"fmt"
	"slices"
	"time"

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
	cmd, err := task.BuildCommand(ctx)
	if err != nil {
		return Result{Error: err.Error()}, err
	}

	// 类型安全转换
	var payload interface{}
	switch v := cmd.Payload.(type) {
	case []string:
		payload = v
	case []*channel.SendInteractiveEvent:
		payload = v
	default:
		return Result{Error: "unsupported payload type"}, nil
	}
	resp, err := conn.Execute(&connection.ProtocolRequest{
		CommandType: cmd.Type,
		Payload:     payload,
		Timeout:     ctx.Timeout,
	})

	if err != nil {
		return Result{Error: err.Error()}, err
	}

	// 统一使用原始数据解析
	return task.ParseOutput(ctx, resp.RawData)
}

func NewExecutor(callback func(Result, error), middlewares ...Middleware) *Executor {
	core := func(task Task, conn connection.ProtocolDriver, ctx TaskContext) (Result, error) {
		if err := task.ValidateParams(ctx.Params); err != nil {
			return Result{Error: err.Error()}, err
		}
		cmd, err := task.BuildCommand(ctx)
		if err != nil {
			return Result{Error: err.Error()}, err
		}
		var raw interface{}
		switch cmd.Type {
		case TypeCommands:
			raw, err = conn.(interface {
				SendCommands([]string) (string, error)
			}).SendCommands(cmd.Payload.([]string))
		case TypeInteractiveEvent:
			raw, err = conn.(interface {
				SendInteractive([]interface{}) (string, error)
			}).SendInteractive(cmd.Payload.([]interface{}))
		default:
			return Result{Error: "unknown command type"}, fmt.Errorf("unknown command type")
		}
		if err != nil {
			return Result{Error: err.Error()}, err
		}
		return task.ParseOutput(ctx, raw)
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

// task/executor.go
func validateCapability(driver ProtocolDriver, ctx TaskContext) error {
	caps := driver.GetCapability()

	// 检查平台支持
	if !slices.Contains(caps.PlatformSupport, ctx.Platform) {
		return fmt.Errorf("platform %s not supported", ctx.Platform)
	}

	// 检查命令类型支持
	if !caps.SupportsCommandType(ctx.CommandType) {
		return fmt.Errorf("command type %s not supported", ctx.CommandType)
	}

	// 检查超时限制
	if ctx.Timeout > caps.Timeout && caps.Timeout > 0 {
		return fmt.Errorf("requested timeout exceeds protocol limit")
	}

	return nil
}

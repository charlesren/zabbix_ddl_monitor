package task

import (
	"context"
	"time"

	"github.com/charlesren/zabbix_ddl_monitor/connection"
)

type (
	ExecutorFunc func(Task, connection.ProtocolDriver, TaskContext) (Result, error)
	Middleware   func(ExecutorFunc) ExecutorFunc
)

type Executor struct {
	core     ExecutorFunc
	callback func(Result, error)
}

func New(callback func(Result, error), middlewares ...Middleware) *Executor {
	core := func(task Task, conn connection.ProtocolDriver, ctx TaskContext) (Result, error) {

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

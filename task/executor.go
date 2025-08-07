type Executor struct {
	maxRetries int           // 最大重试次数
	timeout    time.Duration // 超时时间
}

// Execute 执行任务（仅关注执行逻辑）
func (e *Executor) Execute(task Task, conn ProtocolDriver, ctx TaskContext) (Result, error) {
	// 1. 参数校验
	if err := task.ValidateParams(ctx.Params); err != nil {
		return Result{Success: false, Error: fmt.Sprintf("invalid params: %v", err)}, nil
	}

	// 2. 执行任务（带超时和重试）
	var result Result
	for i := 0; i < e.maxRetries; i++ {
		timeoutCtx, cancel := context.WithTimeout(context.Background(), e.timeout)
		defer cancel()

		result, err := task.Execute(timeoutCtx, conn, ctx)
		if err == nil {
			return result, nil
		}
	}
	return Result{Success: false, Error: "max retries exceeded"}, nil
}

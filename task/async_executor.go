package task

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/charlesren/ylog"
	"github.com/charlesren/zabbix_ddl_monitor/connection"
)

const (
	asyncExecutorModule = "async_executor"
)

// AsyncTaskRequest 异步任务请求
type AsyncTaskRequest struct {
	Task     Task
	Conn     connection.ProtocolDriver
	Context  TaskContext
	Callback func(Result, error)
}

// AsyncExecutor 异步任务执行器
type AsyncExecutor struct {
	taskChan chan AsyncTaskRequest
	workers  int
	wg       sync.WaitGroup
	stopChan chan struct{}
	executor *Executor
	ctx      context.Context
	cancel   context.CancelFunc
}

// NewAsyncExecutor 创建新的异步执行器
func NewAsyncExecutor(workers int, middlewares ...Middleware) *AsyncExecutor {
	ctx, cancel := context.WithCancel(context.Background())

	executor := NewExecutor(nil, middlewares...)

	return &AsyncExecutor{
		taskChan: make(chan AsyncTaskRequest, workers*2), // 缓冲队列
		workers:  workers,
		stopChan: make(chan struct{}),
		executor: executor,
		ctx:      ctx,
		cancel:   cancel,
	}
}

// Start 启动异步执行器
func (e *AsyncExecutor) Start() {
	ylog.Infof(asyncExecutorModule, "starting async executor with %d workers and buffer size %d", e.workers, cap(e.taskChan))
	for i := 0; i < e.workers; i++ {
		e.wg.Add(1)
		go e.worker(i)
	}
	ylog.Infof(asyncExecutorModule, "started %d workers", e.workers)
}

// Stop 停止异步执行器
func (e *AsyncExecutor) Stop() {
	ylog.Infof(asyncExecutorModule, "stopping async executor with %d queued tasks", len(e.taskChan))

	// 关闭停止通道
	close(e.stopChan)

	// 取消上下文
	e.cancel()

	// 等待所有worker完成
	e.wg.Wait()

	// 关闭任务通道
	close(e.taskChan)

	ylog.Infof(asyncExecutorModule, "async executor stopped, all workers terminated")
}

// Submit 提交异步任务
func (e *AsyncExecutor) Submit(task Task, conn connection.ProtocolDriver, ctx TaskContext, callback func(Result, error)) error {
	if conn == nil {
		ylog.Errorf(asyncExecutorModule, "connection driver is nil for task %s on %s", ctx.TaskType, ctx.Platform)
		return fmt.Errorf("connection driver is nil")
	}

	select {
	case e.taskChan <- AsyncTaskRequest{
		Task:     task,
		Conn:     conn,
		Context:  ctx,
		Callback: callback,
	}:
		ylog.Debugf(asyncExecutorModule, "submitted task %s for %s to queue (queue length: %d)", ctx.TaskType, ctx.Platform, len(e.taskChan))
		return nil
	case <-e.ctx.Done():
		ylog.Warnf(asyncExecutorModule, "failed to submit task %s for %s: executor context cancelled", ctx.TaskType, ctx.Platform)
		return e.ctx.Err()
	default:
		ylog.Warnf(asyncExecutorModule, "failed to submit task %s for %s: queue full (capacity: %d, current: %d)", ctx.TaskType, ctx.Platform, cap(e.taskChan), len(e.taskChan))
		return ErrQueueFull
	}
}

// SubmitWithTimeout 提交异步任务，带超时
func (e *AsyncExecutor) SubmitWithTimeout(task Task, conn connection.ProtocolDriver, ctx TaskContext, callback func(Result, error), timeout time.Duration) error {
	if conn == nil {
		ylog.Errorf(asyncExecutorModule, "connection driver is nil for task %s on %s", ctx.TaskType, ctx.Platform)
		return fmt.Errorf("connection driver is nil")
	}
	select {
	case e.taskChan <- AsyncTaskRequest{
		Task:     task,
		Conn:     conn,
		Context:  ctx,
		Callback: callback,
	}:
		ylog.Debugf(asyncExecutorModule, "submitted task %s for %s with timeout %v (queue length: %d)", ctx.TaskType, ctx.Platform, timeout, len(e.taskChan))
		return nil
	case <-time.After(timeout):
		ylog.Warnf(asyncExecutorModule, "submit timeout for task %s for %s after %v (queue length: %d)", ctx.TaskType, ctx.Platform, timeout, len(e.taskChan))
		return ErrSubmitTimeout
	case <-e.ctx.Done():
		ylog.Warnf(asyncExecutorModule, "failed to submit task %s for %s: executor context cancelled", ctx.TaskType, ctx.Platform)
		return e.ctx.Err()
	}
}

// worker 工作goroutine
func (e *AsyncExecutor) worker(id int) {
	defer e.wg.Done()

	ylog.Infof(asyncExecutorModule, "worker %d started", id)

	for {
		select {
		case req, ok := <-e.taskChan:
			if !ok {
				ylog.Debugf(asyncExecutorModule, "worker %d: task channel closed", id)
				return
			}

			ylog.Debugf(asyncExecutorModule, "worker %d received task %s for %s (remaining queue: %d)",
				id, req.Context.TaskType, req.Context.Platform, len(e.taskChan))
			e.processTask(id, req)

		case <-e.stopChan:
			ylog.Debugf(asyncExecutorModule, "worker %d: received stop signal", id)
			return
		}
	}
}

// processTask 处理单个任务
func (e *AsyncExecutor) processTask(workerID int, req AsyncTaskRequest) {
	ylog.Debugf(asyncExecutorModule, "worker %d task context: platform=%s, protocol=%s, taskType=%s",
		workerID, req.Context.Platform, req.Context.Protocol, req.Context.TaskType)
	start := time.Now()
	ylog.Infof(asyncExecutorModule, "worker %d processing %s task for %s",
		workerID, req.Context.TaskType, req.Context.Platform)
	// 为任务添加超时上下文
	taskCtx := req.Context
	if taskCtx.Ctx == nil {
		taskCtx.Ctx = e.ctx
		ylog.Debugf(asyncExecutorModule, "worker %d: using executor context for task %s", workerID, req.Context.TaskType)
	}

	ylog.Debugf(asyncExecutorModule, "worker %d: executing task %s via executor", workerID, req.Context.TaskType)
	ylog.Debugf(asyncExecutorModule, "worker %d task details: platform=%s, protocol=%s, params=%+v",
		workerID, req.Context.Platform, req.Context.Protocol, req.Context.Params)

	// 执行任务
	result, err := e.executor.Execute(req.Task, req.Conn, taskCtx)

	duration := time.Since(start)

	// 记录执行结果
	if err != nil {
		ylog.Errorf(asyncExecutorModule, "worker %d task %s for %s failed after %v: %v",
			workerID, req.Context.TaskType, req.Context.Platform, duration, err)
		ylog.Debugf(asyncExecutorModule, "worker %d task failure details: error=%v, result=%+v",
			workerID, err, result)
	} else if !result.Success {
		ylog.Warnf(asyncExecutorModule, "worker %d task %s for %s completed with failure after %v: %s",
			workerID, req.Context.TaskType, req.Context.Platform, duration, result.Error)
		ylog.Debugf(asyncExecutorModule, "worker %d task failure result: data=%+v",
			workerID, result.Data)
	} else {
		ylog.Infof(asyncExecutorModule, "worker %d task %s for %s completed successfully in %v",
			workerID, req.Context.TaskType, req.Context.Platform, duration)
		ylog.Debugf(asyncExecutorModule, "worker %d task success result: data=%+v",
			workerID, result.Data)
	}

	// 调用回调函数
	if req.Callback != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					ylog.Errorf(asyncExecutorModule, "worker %d callback panic for task %s: %v", workerID, req.Context.TaskType, r)
				}
			}()
			ylog.Debugf(asyncExecutorModule, "worker %d invoking callback for task %s", workerID, req.Context.TaskType)
			req.Callback(result, err)
		}()
	} else {
		ylog.Debugf(asyncExecutorModule, "worker %d task %s completed with no callback", workerID, req.Context.TaskType)
	}
}

// GetQueueLength 获取当前队列长度
func (e *AsyncExecutor) GetQueueLength() int {
	return len(e.taskChan)
}

// GetWorkerCount 获取工作goroutine数量
func (e *AsyncExecutor) GetWorkerCount() int {
	return e.workers
}

// 错误定义
var (
	ErrQueueFull     = fmt.Errorf("task queue is full")
	ErrSubmitTimeout = fmt.Errorf("submit task timeout")
)

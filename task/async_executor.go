package task

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/charlesren/ylog"
	"github.com/charlesren/zabbix_ddl_monitor/connection"
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
	for i := 0; i < e.workers; i++ {
		e.wg.Add(1)
		go e.worker(i)
	}
	ylog.Infof("async_executor", "started %d workers", e.workers)
}

// Stop 停止异步执行器
func (e *AsyncExecutor) Stop() {
	ylog.Infof("async_executor", "stopping async executor")

	// 关闭停止通道
	close(e.stopChan)

	// 取消上下文
	e.cancel()

	// 等待所有worker完成
	e.wg.Wait()

	// 关闭任务通道
	close(e.taskChan)

	ylog.Infof("async_executor", "async executor stopped")
}

// Submit 提交异步任务
func (e *AsyncExecutor) Submit(task Task, conn connection.ProtocolDriver, ctx TaskContext, callback func(Result, error)) error {
	select {
	case e.taskChan <- AsyncTaskRequest{
		Task:     task,
		Conn:     conn,
		Context:  ctx,
		Callback: callback,
	}:
		return nil
	case <-e.ctx.Done():
		return e.ctx.Err()
	default:
		return ErrQueueFull
	}
}

// SubmitWithTimeout 提交异步任务，带超时
func (e *AsyncExecutor) SubmitWithTimeout(task Task, conn connection.ProtocolDriver, ctx TaskContext, callback func(Result, error), timeout time.Duration) error {
	select {
	case e.taskChan <- AsyncTaskRequest{
		Task:     task,
		Conn:     conn,
		Context:  ctx,
		Callback: callback,
	}:
		return nil
	case <-time.After(timeout):
		return ErrSubmitTimeout
	case <-e.ctx.Done():
		return e.ctx.Err()
	}
}

// worker 工作goroutine
func (e *AsyncExecutor) worker(id int) {
	defer e.wg.Done()

	ylog.Debugf("async_executor", "worker %d started", id)

	for {
		select {
		case req, ok := <-e.taskChan:
			if !ok {
				ylog.Debugf("async_executor", "worker %d: task channel closed", id)
				return
			}

			e.processTask(id, req)

		case <-e.stopChan:
			ylog.Debugf("async_executor", "worker %d: received stop signal", id)
			return
		}
	}
}

// processTask 处理单个任务
func (e *AsyncExecutor) processTask(workerID int, req AsyncTaskRequest) {
	start := time.Now()
	ylog.Debugf("async_executor", "worker %d processing %s task for %s",
		workerID, req.Context.TaskType, req.Context.Platform)
	// 为任务添加超时上下文
	taskCtx := req.Context
	if taskCtx.Ctx == nil {
		taskCtx.Ctx = e.ctx
	}

	ylog.Debugf("async_executor", "worker %d: executing task %s", workerID, req.Context.TaskType)

	// 执行任务
	result, err := e.executor.Execute(req.Task, req.Conn, taskCtx)

	duration := time.Since(start)

	// 记录执行结果
	if err != nil {
		ylog.Errorf("async_executor", "worker %d task failed: %s (duration: %v, error: %v)",
			workerID, req.Context.TaskType, duration, err)
	} else if !result.Success {
		ylog.Warnf("async_executor", "worker %d task completed with failure: %s (duration: %v, error: %s)",
			workerID, req.Context.TaskType, duration, result.Error)
	} else {
		ylog.Debugf("async_executor", "worker %d task success: %s (duration: %v)",
			workerID, req.Context.TaskType, duration)
	}

	// 调用回调函数
	if req.Callback != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					ylog.Errorf("async_executor", "worker %d callback panic: %v", workerID, r)
				}
			}()
			req.Callback(result, err)
		}()
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

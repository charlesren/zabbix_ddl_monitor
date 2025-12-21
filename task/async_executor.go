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
		taskChan: make(chan AsyncTaskRequest, workers*10), // 缓冲队列
		workers:  workers,
		stopChan: make(chan struct{}),
		executor: executor,
		ctx:      ctx,
		cancel:   cancel,
	}
}

// Start 启动异步执行器
func (e *AsyncExecutor) Start() {
	ylog.Infof(asyncExecutorModule, "启动异步执行器 with %d workers and buffer size %d", e.workers, cap(e.taskChan))
	for i := 0; i < e.workers; i++ {
		e.wg.Add(1)
		go e.worker(i)
	}
	ylog.Infof(asyncExecutorModule, "已启动 %d 个工作线程", e.workers)
}

// Stop 停止异步执行器
func (e *AsyncExecutor) Stop() {
	ylog.Infof(asyncExecutorModule, "正在停止异步执行器，剩余 %d 个排队任务", len(e.taskChan))

	// 关闭停止通道
	close(e.stopChan)

	// 取消上下文
	e.cancel()

	// 等待所有worker完成
	e.wg.Wait()

	// 关闭任务通道
	close(e.taskChan)

	ylog.Infof(asyncExecutorModule, "异步执行器已停止，所有工作线程已终止")
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
		ylog.Debugf(asyncExecutorModule, "已提交任务 %s for %s 到队列 (队列长度: %d)", ctx.TaskType, ctx.Platform, len(e.taskChan))
		return nil
	case <-e.ctx.Done():
		ylog.Warnf(asyncExecutorModule, "failed to submit task %s for %s: executor context cancelled", ctx.TaskType, ctx.Platform)
		return e.ctx.Err()
	default:
		ylog.Warnf(asyncExecutorModule, "提交任务失败 %s for %s: 队列已满 (容量: %d, 当前: %d)", ctx.TaskType, ctx.Platform, cap(e.taskChan), len(e.taskChan))
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
		ylog.Debugf(asyncExecutorModule, "已提交任务 %s for %s 带超时 %v (队列长度: %d)", ctx.TaskType, ctx.Platform, timeout, len(e.taskChan))
		return nil
	case <-time.After(timeout):
		ylog.Warnf(asyncExecutorModule, "提交任务超时 %s for %s after %v (队列长度: %d)", ctx.TaskType, ctx.Platform, timeout, len(e.taskChan))
		return ErrSubmitTimeout
	case <-e.ctx.Done():
		ylog.Warnf(asyncExecutorModule, "failed to submit task %s for %s: executor context cancelled", ctx.TaskType, ctx.Platform)
		return e.ctx.Err()
	}
}

// worker 工作goroutine
func (e *AsyncExecutor) worker(id int) {
	defer e.wg.Done()

	ylog.Infof(asyncExecutorModule, "工作线程 %d 已启动", id)

	for {
		select {
		case req, ok := <-e.taskChan:
			if !ok {
				ylog.Debugf(asyncExecutorModule, "工作线程 %d: 任务通道已关闭", id)
				return
			}

			ylog.Debugf(asyncExecutorModule, "工作线程 %d 收到任务 %s for %s (剩余队列: %d)",
				id, req.Context.TaskType, req.Context.Platform, len(e.taskChan))
			e.processTask(id, req)

		case <-e.stopChan:
			ylog.Debugf(asyncExecutorModule, "工作线程 %d: 收到停止信号", id)
			return
		}
	}
}

// processTask 处理单个任务
func (e *AsyncExecutor) processTask(workerID int, req AsyncTaskRequest) {
	ylog.Debugf(asyncExecutorModule, "工作线程 %d 任务上下文: platform=%s, protocol=%s, taskType=%s",
		workerID, req.Context.Platform, req.Context.Protocol, req.Context.TaskType)
	start := time.Now()
	ylog.Infof(asyncExecutorModule, "工作线程 %d 正在处理 %s 任务 for %s",
		workerID, req.Context.TaskType, req.Context.Platform)
	// 为任务添加超时上下文
	taskCtx := req.Context
	if taskCtx.Ctx == nil {
		taskCtx.Ctx = e.ctx
		ylog.Debugf(asyncExecutorModule, "工作线程 %d: 使用执行器上下文 for task %s", workerID, req.Context.TaskType)
	}

	ylog.Debugf(asyncExecutorModule, "工作线程 %d: 通过执行器执行任务 %s", workerID, req.Context.TaskType)
	ylog.Debugf(asyncExecutorModule, "工作线程 %d 任务详情: platform=%s, protocol=%s, params=%+v",
		workerID, req.Context.Platform, req.Context.Protocol, req.Context.Params)

	// 执行任务
	result, err := e.executor.Execute(req.Task, req.Conn, taskCtx)

	duration := time.Since(start)

	// 记录执行结果
	if err != nil {
		ylog.Errorf(asyncExecutorModule, "工作线程 %d 任务 %s for %s 执行失败 after %v: %v",
			workerID, req.Context.TaskType, req.Context.Platform, duration, err)
		ylog.Debugf(asyncExecutorModule, "工作线程 %d 任务失败详情: error=%v, result=%+v",
			workerID, err, result)
	} else if !result.Success {
		ylog.Warnf(asyncExecutorModule, "工作线程 %d 任务 %s for %s 完成但失败 after %v: %s",
			workerID, req.Context.TaskType, req.Context.Platform, duration, result.Error)
		ylog.Debugf(asyncExecutorModule, "工作线程 %d 任务失败结果: data=%+v",
			workerID, result.Data)
	} else {
		ylog.Infof(asyncExecutorModule, "工作线程 %d 任务 %s for %s 执行成功 in %v",
			workerID, req.Context.TaskType, req.Context.Platform, duration)
		ylog.Debugf(asyncExecutorModule, "工作线程 %d 任务成功结果: data=%+v",
			workerID, result.Data)
	}

	// 调用回调函数
	if req.Callback != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					ylog.Errorf(asyncExecutorModule, "工作线程 %d 回调函数发生panic for task %s: %v", workerID, req.Context.TaskType, r)
				}
			}()
			ylog.Debugf(asyncExecutorModule, "工作线程 %d 调用回调函数 for task %s", workerID, req.Context.TaskType)
			req.Callback(result, err)
		}()
	} else {
		ylog.Debugf(asyncExecutorModule, "工作线程 %d 任务 %s 完成但无回调函数", workerID, req.Context.TaskType)
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

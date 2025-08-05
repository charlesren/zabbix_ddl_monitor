package task

import (
	"context"
	"fmt"
	"sync"

	"github.com/charlesren/zabbix_ddl_monitor/connection"
)

type AsyncExecutor struct {
	taskChan chan asyncTask // 任务通道
	workers  int            // 并发worker数量
	wg       sync.WaitGroup // 任务组同步
	stopChan chan struct{}  // 停止信号
}

type asyncTask struct {
	ctx      context.Context
	conn     connection.ProtocolDriver // 由外部传入连接
	platform string
	taskType string
	params   map[string]interface{}
	callback func(Result)
}

func NewAsyncExecutor(workers int, queueSize int) *AsyncExecutor {
	e := &AsyncExecutor{
		taskChan: make(chan asyncTask, queueSize),
		workers:  workers,
		stopChan: make(chan struct{}),
	}
	e.startWorkers()
	return e
}

// Submit 提交异步任务（非阻塞）
func (e *AsyncExecutor) Submit(
	ctx context.Context,
	conn connection.ProtocolDriver,
	platform string,
	taskType string,
	params map[string]interface{},
	callback func(Result),
) error {
	select {
	case e.taskChan <- asyncTask{
		ctx:      ctx,
		conn:     conn,
		platform: platform,
		taskType: taskType,
		params:   params,
		callback: callback,
	}:
		return nil
	default:
		return fmt.Errorf("task queue full")
	}
}

func (e *AsyncExecutor) startWorkers() {
	for i := 0; i < e.workers; i++ {
		e.wg.Add(1)
		go e.worker()
	}
}

func (e *AsyncExecutor) worker() {
	defer e.wg.Done()

	for {
		select {
		case task := <-e.taskChan:
			// 复用SyncExecutor执行逻辑
			result, err := NewSyncExecutor().Execute(
				task.ctx,
				task.conn,
				task.platform,
				task.taskType,
				task.params,
			)
			if err != nil {
				result.Error = err.Error()
			}
			if task.callback != nil {
				task.callback(result)
			}
		case <-e.stopChan:
			return
		}
	}
}

func (e *AsyncExecutor) Stop() {
	close(e.stopChan)
	e.wg.Wait()
}

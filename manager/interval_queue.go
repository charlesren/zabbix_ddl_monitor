package manager

import (
	"sync"
	"time"

	"github.com/charlesren/zabbix_ddl_monitor/syncer"
)

type IntervalTaskQueue struct {
	interval   time.Duration
	lines      []syncer.Line // 改为值存储
	mu         sync.Mutex
	execNotify chan struct{} // 执行信号通道
	stopChan   chan struct{} // 新增停止通道
	ticker     *time.Ticker  // 内置调度器
}

// 初始化时启动调度协程

func NewIntervalTaskQueue(interval time.Duration) *IntervalTaskQueue {
	// 确保间隔至少为1纳秒以避免time.NewTicker panic
	if interval <= 0 {
		interval = time.Nanosecond
	}
	q := &IntervalTaskQueue{
		interval:   interval,
		execNotify: make(chan struct{}, 1), // 缓冲防止阻塞
		ticker:     time.NewTicker(interval),
		stopChan:   make(chan struct{}),
	}
	go q.schedule()
	return q
}

// 内部调度循环
func (q *IntervalTaskQueue) schedule() {
	for {
		select {
		case <-q.ticker.C:
			select {
			case q.execNotify <- struct{}{}: // 非阻塞发送信号
			default:
			}
		case <-q.stopChan:
			return
		}
	}
}

// 暴露执行信号通道（供RouterScheduler监听）
func (q *IntervalTaskQueue) ExecNotify() <-chan struct{} {
	return q.execNotify
}

// 安全获取任务快照（执行时调用）
func (q *IntervalTaskQueue) GetTasksSnapshot() []syncer.Line {
	q.mu.Lock()
	defer q.mu.Unlock()
	snapshot := make([]syncer.Line, len(q.lines))
	copy(snapshot, q.lines)
	return snapshot
}
func (q *IntervalTaskQueue) IsEmpty() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.lines) == 0
}

func (q *IntervalTaskQueue) Remove(lineID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, line := range q.lines {
		if line.ID == lineID {
			q.lines = append(q.lines[:i], q.lines[i+1:]...)
			break
		}
	}
}

func (q *IntervalTaskQueue) Contains(lineID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, line := range q.lines {
		if line.ID == lineID {
			return true
		}
	}
	return false
}

func (q *IntervalTaskQueue) Stop() {
	close(q.stopChan)
}

func (q *IntervalTaskQueue) Add(line syncer.Line) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.lines = append(q.lines, line)
}

// 更新任务参数
func (q *IntervalTaskQueue) UpdateTask(line syncer.Line) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i, t := range q.lines {
		if t.ID == line.ID {
			q.lines[i] = line
			break
		}
	}
}

// 批量替换任务（供OnLineReset使用）
func (q *IntervalTaskQueue) ReplaceAll(lines []syncer.Line) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.lines = make([]syncer.Line, len(lines))
	copy(q.lines, lines)
}

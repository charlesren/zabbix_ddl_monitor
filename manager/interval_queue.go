package manager

import (
	"sync"
	"time"

	"github.com/charlesren/zabbix_ddl_monitor/syncer"
)

type IntervalTaskQueue struct {
	interval time.Duration
	tasks    []*syncer.Line
	nextRun  time.Time
	mu       sync.Mutex
}

func NewIntervalTaskQueue(interval time.Duration) *IntervalTaskQueue {
	return &IntervalTaskQueue{
		interval: interval,
		nextRun:  time.Now(),
	}
}

func (q *IntervalTaskQueue) Add(line *syncer.Line) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.tasks = append(q.tasks, line)
}

func (q *IntervalTaskQueue) ShouldRun(now time.Time) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return !now.Before(q.nextRun)
}

func (q *IntervalTaskQueue) Run(executor TaskExecutor) []Result {
	q.mu.Lock()
	defer func() {
		q.nextRun = time.Now().Add(q.interval)
		q.mu.Unlock()
	}()

	return executor.Execute(q.tasks)
}

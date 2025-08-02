package scheduler

import (
	"sync"
	"time"
)

type IntervalTaskQueue struct {
	interval time.Duration
	tasks    []*config.Line
	nextRun  time.Time
	mu       sync.Mutex
}

func NewIntervalTaskQueue(interval time.Duration) *IntervalTaskQueue {
	return &IntervalTaskQueue{
		interval: interval,
		nextRun:  time.Now(),
	}
}

func (q *IntervalTaskQueue) Add(line *config.Line) {
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

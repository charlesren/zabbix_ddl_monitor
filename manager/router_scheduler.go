package manager

import (
	"log"
	"sync"
	"time"

	"github.com/charlesren/zabbix_ddl_monitor/connection"
	"github.com/charlesren/zabbix_ddl_monitor/syncer"
)

type Scheduler interface {
	OnLineCreated(line syncer.Line)     // 专线创建
	OnLineUpdated(old, new syncer.Line) // 专线更新（提供新旧值）
	OnLineDeleted(line syncer.Line)     // 专线删除
	OnLineReset(lines []syncer.Line)    // 专线重置
	Stop()
	Start()
}
type RouterScheduler struct {
	router     *syncer.Router
	lines      []syncer.Line
	connection *connection.Connection
	queues     map[time.Duration]*IntervalTaskQueue
	stopChan   chan struct{}
	wg         sync.WaitGroup
	mu         sync.Mutex
}

func NewRouterScheduler(router *syncer.Router, initialLines []syncer.Line) *RouterScheduler {
	return &RouterScheduler{
		router:     router,
		lines:      initialLines,
		connection: connection.NewConnection(router),
		queues:     make(map[time.Duration]*IntervalTaskQueue),
		stopChan:   make(chan struct{}),
	}
}

// 添加专线到对应间隔队列
func (s *RouterScheduler) AddLine(line syncer.Line) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.queues[line.Interval]; !exists {
		s.queues[line.Interval] = NewIntervalTaskQueue(line.Interval)
	}
	s.queues[line.Interval].Add(line)
}

// 启动调度循环
func (s *RouterScheduler) Start() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.runPendingTasks()
		case <-s.stopChan:
			return
		}
	}
}

// 执行到期任务
func (s *RouterScheduler) runPendingTasks() {
	s.mu.Lock()
	defer s.mu.Unlock()

	conn, err := s.connection.Get()
	if err != nil {
		log.Printf("[%s] 获取连接失败: %v", s.router.IP, err)
		return
	}

	now := time.Now()
	for _, queue := range s.queues {
		if !queue.ShouldRun(now) {
			continue
		}

		s.wg.Add(1)
		go func(q *IntervalTaskQueue) {
			defer s.wg.Done()
			q.Execute(conn, s.router.Platform)
		}(queue)
	}
}

// 停止调度器（阻塞等待所有任务完成）
func (s *RouterScheduler) Stop() {
	close(s.stopChan)
	s.wg.Wait()
	_ = s.connection.Close()
}

func (s *RouterScheduler) OnLineCreated(line syncer.Line)     {}
func (s *RouterScheduler) OnLineUpdated(old, new syncer.Line) {}
func (s *RouterScheduler) OnLineDeleted(line syncer.Line)     {}
func (s *RouterScheduler) OnLineReset(lines []syncer.Line) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch event.Type {
	case syncer.LineCreate:
		s.lines = append(s.lines, event.Line)
	case syncer.LineUpdate:
		for i, line := range s.lines {
			if line.ID == event.Line.ID {
				s.lines[i] = event.Line
				break
			}
		}
	case syncer.LineDelete:
		for i, line := range s.lines {
			if line.ID == event.Line.ID {
				s.lines = append(s.lines[:i], s.lines[i+1:]...)
				break
			}
		}
	}
}

package scheduler

import (
	"log"
	"sync"
	"time"

	"github.com/yourusername/zabbix_ddl_monitor/internal/config"
	"github.com/yourusername/zabbix_ddl_monitor/internal/router"
)

type RouterScheduler struct {
	router     *router.Router
	connection *router.Connection
	queues     map[time.Duration]*IntervalQueue
	closeChan  chan struct{}
	wg         sync.WaitGroup
	mu         sync.Mutex
}

func NewRouterScheduler(router *router.Router) *RouterScheduler {
	return &RouterScheduler{
		router:     router,
		connection: router.NewConnection(router),
		queues:     make(map[time.Duration]*IntervalQueue),
		closeChan:  make(chan struct{}),
	}
}

// 添加专线到对应间隔队列
func (s *RouterScheduler) AddLine(line config.Line) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.queues[line.Interval]; !exists {
		s.queues[line.Interval] = NewIntervalQueue(line.Interval)
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
		case <-s.closeChan:
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
		go func(q *IntervalQueue) {
			defer s.wg.Done()
			q.Execute(conn, s.router.Platform)
		}(queue)
	}
}

// 停止调度器（阻塞等待所有任务完成）
func (s *RouterScheduler) Stop() {
	close(s.closeChan)
	s.wg.Wait()
	_ = s.connection.Close()
}

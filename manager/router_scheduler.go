package manager

import (
	"sync"
	"time"

	"github.com/charlesren/ylog"
	"github.com/charlesren/zabbix_ddl_monitor/connection"
	"github.com/charlesren/zabbix_ddl_monitor/syncer"
	"github.com/charlesren/zabbix_ddl_monitor/task"
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
	connection *connection.ConnectionPool
	queues     map[time.Duration]*IntervalTaskQueue
	executor   *task.Executor
	stopChan   chan struct{}
	wg         sync.WaitGroup
	mu         sync.Mutex
}

func NewRouterScheduler(router *syncer.Router, initialLines []syncer.Line) *RouterScheduler {
	scheduler := &RouterScheduler{
		router:     router,
		lines:      initialLines,
		connection: connection.NewConnection(router),
		queues:     make(map[time.Duration]*IntervalTaskQueue),
		stopChan:   make(chan struct{}),
	}

	// 初始化时构建队列
	scheduler.initializeQueues()
	return scheduler
}

func (s *RouterScheduler) initializeQueues() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, line := range s.lines {
		// 创建间隔队列（如果不存在）
		if _, exists := s.queues[line.Interval]; !exists {
			s.queues[line.Interval] = NewIntervalTaskQueue(line.Interval)
			ylog.Infof("scheduler", "initialized queue for interval %v (router=%s)",
				line.Interval, s.router.IP)
		}
		// 添加任务
		s.queues[line.Interval].Add(line)
	}

	ylog.Infof("scheduler", "router %s initialized with %d queues (total lines=%d)",
		s.router.IP, len(s.queues), len(s.lines))
}

// 启动调度循环
func (s *RouterScheduler) Start() {
	for _, q := range s.queues {
		go func(q *IntervalTaskQueue) {
			for range q.ExecNotify() { // 监听执行信号
				s.wg.Add(1)
				go s.executeTasks(q)
			}
		}(q)
	}
}

func (s *RouterScheduler) executeTasks(q *IntervalTaskQueue) {
	defer s.wg.Done()

	// 1. 获取连接（带重试和超时）
	conn, err := s.connection.GetWithRetry(3, 2*time.Second)
	if err != nil {
		ylog.Errorf("scheduler", "router=%s get connection failed: %v", s.router.IP, err)
		return
	}
	defer s.connection.Release(conn)

	// 2. 获取任务快照（无锁阻塞）
	tasks := q.GetTasksSnapshot()
	if len(tasks) == 0 {
		return
	}

	// 3. 通过Executor执行
	results := s.executor.ExecuteWithConn(conn, s.router.Platform, tasks)
	s.handleResults(results) // 上报结果
}

// 停止调度器（阻塞等待所有任务完成）
func (s *RouterScheduler) Stop() {
	close(s.stopChan)
	s.wg.Wait()
	_ = s.connection.Close()
}

func (s *RouterScheduler) OnLineCreated(line syncer.Line) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 检查专线是否已存在（防御性编程）
	if s.lineExistsLocked(line.ID) {
		ylog.Warnf("scheduler", "duplicate line creation ignored (id=%s, router=%s)",
			line.ID, s.router.IP)
		return
	}

	// 创建或获取对应间隔队列
	if _, exists := s.queues[line.Interval]; !exists {
		s.queues[line.Interval] = NewIntervalTaskQueue(line.Interval)
		ylog.Infof("scheduler", "created new queue for interval %v (router=%s)",
			line.Interval, s.router.IP)
	}

	// 添加任务并记录
	s.queues[line.Interval].Add(line)
	s.lines = append(s.lines, line)
	ylog.Debugf("scheduler", "added line %s to %v queue (router=%s)",
		line.ID, line.Interval, s.router.IP)
}

// 辅助方法：检查专线是否存在（需在锁内调用）
func (s *RouterScheduler) lineExistsLocked(lineID string) bool {
	for _, l := range s.lines {
		if l.ID == lineID {
			return true
		}
	}
	return false
}

func (s *RouterScheduler) OnLineUpdated(oldLine, newLine syncer.Line) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 场景1：间隔时间变化 → 迁移队列
	if oldLine.Interval != newLine.Interval {
		// 从旧队列移除
		if oldQueue, exists := s.queues[oldLine.Interval]; exists {
			oldQueue.Remove(oldLine.ID)
			ylog.Debugf("scheduler", "moved line %s from %v to %v queue (router=%s)",
				oldLine.ID, oldLine.Interval, newLine.Interval, s.router.IP)
		}

		// 添加到新队列
		if _, exists := s.queues[newLine.Interval]; !exists {
			s.queues[newLine.Interval] = NewIntervalTaskQueue(newLine.Interval)
		}
		s.queues[newLine.Interval].Add(newLine)
	} else {
		// 场景2：仅更新参数 → 原地更新
		if queue, exists := s.queues[oldLine.Interval]; exists {
			queue.UpdateTask(newLine)
		}
	}

	// 更新专线列表
	for i, l := range s.lines {
		if l.IP == oldLine.IP {
			s.lines[i] = newLine
			break
		}
	}
	ylog.Debugf("scheduler", "updated line %s (router=%s)", newLine.ID, s.router.IP)
}

// 专线删除处理（带防御性检查）
func (s *RouterScheduler) OnLineDeleted(line syncer.Line) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if queue, exists := s.queues[line.Interval]; exists {
		// 防御性检查：确保任务存在
		if !queue.Contains(line.ID) {
			ylog.Warnf("scheduler", "line %s not found in queue (router=%s)",
				line.ID, s.router.IP)
			return
		}

		queue.Remove(line.ID)
		ylog.Debugf("scheduler", "removed line %s from %v queue (router=%s)",
			line.ID, line.Interval, s.router.IP)

		// 延迟销毁空队列（10分钟）
		if queue.IsEmpty() {
			time.AfterFunc(10*time.Minute, func() {
				s.mu.Lock()
				defer s.mu.Unlock()
				if q, exists := s.queues[line.Interval]; exists && q.IsEmpty() {
					q.Stop() // 停止队列内部定时器
					delete(s.queues, line.Interval)
					ylog.Infof("scheduler", "destroyed empty queue for interval %v (router=%s)",
						line.Interval, s.router.IP)
				}
			})
		}
	}
}

func (s *RouterScheduler) OnLineReset(newLines []syncer.Line) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 停止所有旧队列
	for _, q := range s.queues {
		q.Stop()
	}

	// 重建队列和专线列表
	s.queues = make(map[time.Duration]*IntervalTaskQueue)
	s.lines = make([]syncer.Line, 0, len(newLines))

	for _, line := range newLines {
		if _, exists := s.queues[line.Interval]; !exists {
			s.queues[line.Interval] = NewIntervalTaskQueue(line.Interval)
		}
		s.queues[line.Interval].Add(line)
		s.lines = append(s.lines, line)
	}

	ylog.Infof("scheduler", "reset all queues for router %s (total_lines=%d, queues=%d)",
		s.router.IP, len(newLines), len(s.queues))
}

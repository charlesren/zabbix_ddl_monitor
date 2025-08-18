package manager

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/charlesren/ylog"
	"github.com/charlesren/zabbix_ddl_monitor/connection"
	"github.com/charlesren/zabbix_ddl_monitor/syncer"
	"github.com/charlesren/zabbix_ddl_monitor/task"
)

// 预热参数配置
var (
	warmUpConnectionCount = 3 // 预热连接数
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
	manager        *Manager
	router         *syncer.Router
	lines          []syncer.Line
	connection     *connection.ConnectionPool
	connCapability *connection.ProtocolCapability //预加载的连接能力信息
	capabilityMu   sync.RWMutex                   // 能力信息的读写锁
	queues         map[time.Duration]*IntervalTaskQueue
	asyncExecutor  *task.AsyncExecutor
	stopChan       chan struct{}
	wg             sync.WaitGroup
	mu             sync.Mutex
}

func NewRouterScheduler(router *syncer.Router, initialLines []syncer.Line, manager *Manager) *RouterScheduler {
	scheduler := &RouterScheduler{
		router:     router,
		lines:      initialLines,
		connection: connection.NewConnectionPool(router.ToConnectionConfig()),
		queues:     make(map[time.Duration]*IntervalTaskQueue),
		manager:    manager,
		stopChan:   make(chan struct{}),
	}
	ylog.Debugf("scheduler", "connection config: %+v", router.ToConnectionConfig())

	//  预热连接池
	if err := scheduler.connection.WarmUp(scheduler.router.Protocol, warmUpConnectionCount); err != nil {
		ylog.Warnf("scheduler", "connection pool warm-up failed: %v (router=%s)",
			err, scheduler.router.IP)
	} else {
		ylog.Infof("scheduler", "successfully warmed up %d connections (router=%s)",
			warmUpConnectionCount, scheduler.router.IP)
	}
	// 同步预加载Connection能力
	conn, err := scheduler.connection.Get(scheduler.router.Protocol)
	if err != nil {
		ylog.Warnf("scheduler", "preload capability failed: %v", err)
		// 设置默认能力以防止nil指针
		defaultCapability := connection.ProtocolCapability{
			CommandTypesSupport: []connection.CommandType{"commands"},
		}
		scheduler.capabilityMu.Lock()
		scheduler.connCapability = &defaultCapability
		scheduler.capabilityMu.Unlock()
	} else {
		capability := conn.GetCapability()
		scheduler.capabilityMu.Lock()
		scheduler.connCapability = &capability
		scheduler.capabilityMu.Unlock()
		scheduler.connection.Release(conn)
		ylog.Debugf("scheduler", "preloaded capabilities for %s", scheduler.router.IP)
	}

	scheduler.initializeQueues()

	// 创建异步执行器
	scheduler.asyncExecutor = task.NewAsyncExecutor(3, // 3个工作goroutine
		task.WithTimeout(30*time.Second), // 30秒超时
	)

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
	// 启动异步执行器和聚合器
	ylog.Infof("scheduler", "starting router scheduler (router=%s, queues=%d)", s.router.IP, len(s.queues))
	s.asyncExecutor.Start()

	for _, q := range s.queues {
		s.wg.Add(1)
		go func(q *IntervalTaskQueue) {
			ylog.Debugf("scheduler", "queue worker started (router=%s, interval=%v)", s.router.IP, q.interval)
			defer s.wg.Done()
			for {
				select {
				case <-s.stopChan:
					ylog.Debugf("scheduler", "queue worker stopped (router=%s, interval=%v)", s.router.IP, q.interval)
					return
				case <-q.ExecNotify(): // 监听执行信号
					ylog.Debugf("scheduler", "executing tasks (router=%s, interval=%v)", s.router.IP, q.interval)
					s.executeTasksAsync(q)
				}
			}
		}(q)
	}
}

func (s *RouterScheduler) executeTasksAsync(q *IntervalTaskQueue) {
	// 获取任务快照
	lines := q.GetTasksSnapshot()
	if len(lines) == 0 {
		ylog.Debugf("scheduler", "no tasks to execute (router=%s)", s.router.IP)
		return
	}

	// 获取预加载的能力信息（带降级逻辑）
	var supportedCmdTypes []connection.CommandType
	s.capabilityMu.RLock()
	if s.connCapability != nil {
		supportedCmdTypes = s.connCapability.CommandTypesSupport
	}
	s.capabilityMu.RUnlock()

	// 降级：预加载失败时实时获取
	if supportedCmdTypes == nil {
		conn, err := s.connection.Get(s.router.Protocol)
		if err != nil {
			ylog.Errorf("scheduler", "router=%s get connection failed: %v", s.router.IP, err)
			return
		}
		supportedCmdTypes = conn.GetCapability().CommandTypesSupport
		s.connection.Release(conn)
	}

	// 发现匹配的任务
	var matchedTask task.Task
	var matchedCmdType task.CommandType
	for _, cmdType := range supportedCmdTypes {
		t, err := s.manager.registry.Discover(
			"ping",
			s.router.Platform,
			s.router.Protocol,
			cmdType,
		)
		if err == nil {
			matchedTask = t
			matchedCmdType = cmdType
			break
		}
	}

	if matchedTask == nil {
		ylog.Errorf("scheduler", "no matching task for router=%s (supported=%v)",
			s.router.IP, supportedCmdTypes)
		return
	}

	// 合并所有专线IP为批量ping任务
	targetIPs := make([]string, len(lines))
	for i, line := range lines {
		targetIPs[i] = line.IP
	}

	// 获取连接
	conn, err := s.connection.Get(s.router.Protocol)
	if err != nil {
		ylog.Errorf("scheduler", "router=%s get connection failed: %v", s.router.IP, err)
		return
	}

	// 创建批量任务上下文
	taskCtx := task.TaskContext{
		TaskType:    "ping",
		Platform:    s.router.Platform,
		Protocol:    s.router.Protocol,
		CommandType: matchedCmdType,
		Params: map[string]interface{}{
			"target_ips": targetIPs, // 使用复数形式表示批量IP
			"repeat":     5,
			"timeout":    10 * time.Second,
		},
		Ctx: context.Background(),
	}

	// 记录开始时间
	startTime := time.Now()

	ylog.Infof("scheduler", "submitting batch ping task for %d IPs on router %s", len(targetIPs), s.router.IP)

	// 异步提交批量任务
	err = s.asyncExecutor.Submit(matchedTask, conn, taskCtx, func(result task.Result, err error) {
		duration := time.Since(startTime)

		// 释放连接
		s.connection.Release(conn)

		// 处理结果错误
		if err != nil {
			ylog.Errorf("scheduler", "batch ping task execution failed for router %s: %v", s.router.IP, err)
			// 为所有专线创建失败结果
			for _, line := range lines {
				failedResult := task.Result{
					Success: false,
					Error:   err.Error(),
					Data:    make(map[string]interface{}),
				}
				if aggErr := s.manager.aggregator.SubmitTaskResult(line, "ping", failedResult, duration); aggErr != nil {
					ylog.Errorf("scheduler", "failed to submit failed result to aggregator for %s: %v", line.IP, aggErr)
				}
			}
			return
		}

		// 解析批量结果并分发到各个专线
		s.distributeBatchResult(lines, result, duration)
	})

	if err != nil {
		ylog.Errorf("scheduler", "failed to submit batch ping task for router %s: %v", s.router.IP, err)
		s.connection.Release(conn)
	}
}

// distributeBatchResult 分发批量ping结果到各个专线
func (s *RouterScheduler) distributeBatchResult(lines []syncer.Line, batchResult task.Result, duration time.Duration) {
	// 检查批量结果中是否包含每个IP的详细结果
	if batchResults, ok := batchResult.Data["batch_results"].(map[string]task.Result); ok {
		// 如果有详细的批量结果，按IP分发
		for _, line := range lines {
			if ipResult, exists := batchResults[line.IP]; exists {
				if aggErr := s.manager.aggregator.SubmitTaskResult(line, "ping", ipResult, duration); aggErr != nil {
					ylog.Errorf("scheduler", "failed to submit result to aggregator for %s: %v", line.IP, aggErr)
				}
			} else {
				// 如果某个IP没有结果，创建未知状态结果
				unknownResult := task.Result{
					Success: false,
					Error:   "no result found in batch response",
					Data:    map[string]interface{}{"status": "unknown"},
				}
				if aggErr := s.manager.aggregator.SubmitTaskResult(line, "ping", unknownResult, duration); aggErr != nil {
					ylog.Errorf("scheduler", "failed to submit unknown result to aggregator for %s: %v", line.IP, aggErr)
				}
			}
		}
	} else {
		// 如果没有详细结果，所有专线共享同一个结果
		ylog.Warnf("scheduler", "batch result does not contain per-IP details, using overall result for all lines")
		for _, line := range lines {
			// 为每个专线创建单独的结果副本
			lineResult := task.Result{
				Success: batchResult.Success,
				Error:   batchResult.Error,
				Data:    make(map[string]interface{}),
			}

			// 复制数据，添加专线特定信息
			for k, v := range batchResult.Data {
				lineResult.Data[k] = v
			}
			lineResult.Data["line_ip"] = line.IP
			lineResult.Data["batch_mode"] = true

			if aggErr := s.manager.aggregator.SubmitTaskResult(line, "ping", lineResult, duration); aggErr != nil {
				ylog.Errorf("scheduler", "failed to submit result to aggregator for %s: %v", line.IP, aggErr)
			}
		}
	}
}

// mergeLinesIP 合并所有line的IP
func mergeLinesIP(lines []syncer.Line) string {
	ips := make([]string, len(lines))
	for i, line := range lines {
		ips[i] = line.IP
	}
	return strings.Join(ips, ",")
}

// 停止调度器（阻塞等待所有任务完成）
func (s *RouterScheduler) Stop() {
	// 先关闭stopChan通知所有goroutine退出
	ylog.Infof("scheduler", "stopping router scheduler (router=%s)", s.router.IP)
	select {
	case <-s.stopChan:
		// Already closed
	default:
		close(s.stopChan)
	}

	// 停止异步执行器和聚合器
	if s.asyncExecutor != nil {
		s.asyncExecutor.Stop()
		ylog.Debugf("scheduler", "async executor stopped (router=%s)", s.router.IP)
	}

	// 停止所有队列
	s.mu.Lock()
	for _, q := range s.queues {
		q.Stop()
	}
	s.mu.Unlock()

	// 等待所有goroutine完成
	s.wg.Wait()
	ylog.Infof("scheduler", "router scheduler fully stopped (router=%s)", s.router.IP)
	// 关闭连接池
	if s.connection != nil {
		_ = s.connection.Close()
	}
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

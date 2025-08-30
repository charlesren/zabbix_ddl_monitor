package manager

import (
	"context"
	"fmt"
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
	connection     connection.ConnectionPoolInterface
	connCapability *connection.ProtocolCapability //预加载的连接能力信息
	capabilityMu   sync.RWMutex                   // 能力信息的读写锁
	queues         map[time.Duration]*IntervalTaskQueue
	asyncExecutor  *task.AsyncExecutor
	stopChan       chan struct{}
	wg             sync.WaitGroup
	mu             sync.Mutex
	routerCtx      context.Context // 路由器级上下文
}

func NewRouterScheduler(parentCtx context.Context, router *syncer.Router, initialLines []syncer.Line, manager *Manager) (*RouterScheduler, error) {
	// 1. 使用Builder直接构造配置
	config, err := connection.NewConfigBuilder().
		WithBasicAuth(router.IP, router.Username, router.Password).
		WithProtocol(router.Protocol, router.Platform).
		WithMetadata("platform", router.Platform).
		WithMetadata("protocol", router.Protocol).
		Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build connection config: %w", err)
	}

	pool := connection.NewEnhancedConnectionPool(parentCtx, config)

	// 4. 创建调度器实例
	scheduler := &RouterScheduler{
		connection: pool,
		router:     router,
		lines:      initialLines,
		manager:    manager,
		queues:     make(map[time.Duration]*IntervalTaskQueue),
		stopChan:   make(chan struct{}),
		routerCtx:  parentCtx,
	}

	// 5. 预热连接池
	if err := scheduler.connection.WarmUp(router.Protocol, warmUpConnectionCount); err != nil {
		ylog.Warnf("scheduler", "connection pool warm-up failed: %v (router=%s)",
			err, router.IP)
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

	// 6. 初始化队列
	scheduler.initializeQueues()

	// 7. 创建异步执行器
	scheduler.asyncExecutor = task.NewAsyncExecutor(
		2,
		task.WithSmartTimeout(30*time.Second),
	)
	// 可选：启用调试和事件监听
	//scheduler.connection.EnableDebug()
	//go scheduler.listenToPoolEvents()

	return scheduler, nil

}

func (s *RouterScheduler) initializeQueues() {
	s.mu.Lock()
	defer s.mu.Unlock()

	ylog.Infof("scheduler", "initializing queues for router %s with %d lines",
		s.router.IP, len(s.lines))

	for _, line := range s.lines {
		ylog.Debugf("scheduler", "processing line: id=%s, ip=%s, interval=%v",
			line.ID, line.IP, line.Interval)

		// 创建间隔队列（如果不存在）
		if _, exists := s.queues[line.Interval]; !exists {
			s.queues[line.Interval] = NewIntervalTaskQueue(line.Interval)
			ylog.Infof("scheduler", "initialized queue for interval %v (router=%s)",
				line.Interval, s.router.IP)
		}
		// 添加任务
		s.queues[line.Interval].Add(line)
		ylog.Debugf("scheduler", "added line %s to queue %v", line.ID, line.Interval)
	}

	// 验证队列分布
	totalQueued := 0
	for interval, queue := range s.queues {
		lines := queue.GetTasksSnapshot()
		totalQueued += len(lines)
		ylog.Infof("scheduler", "queue %v has %d lines (router=%s)",
			interval, len(lines), s.router.IP)
	}

	if totalQueued != len(s.lines) {
		ylog.Errorf("scheduler", "queue distribution mismatch: expected %d lines, got %d in queues (router=%s)",
			len(s.lines), totalQueued, s.router.IP)
	}

	ylog.Infof("scheduler", "router %s initialized with %d queues (total lines=%d, queued=%d)",
		s.router.IP, len(s.queues), len(s.lines), totalQueued)
}

// 启动调度循环
func (s *RouterScheduler) Start() {
	// 启动异步执行器和聚合器
	ylog.Infof("scheduler", "starting router scheduler (router=%s, queues=%d)", s.router.IP, len(s.queues))
	s.asyncExecutor.Start()

	// 记录每个队列的详细信息
	for interval, q := range s.queues {
		lines := q.GetTasksSnapshot()
		ylog.Infof("scheduler", "queue details: router=%s, interval=%v, lines=%d",
			s.router.IP, interval, len(lines))
		for _, line := range lines {
			ylog.Debugf("scheduler", "  line: id=%s, ip=%s, interval=%v",
				line.ID, line.IP, line.Interval)
		}
	}

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
	ylog.Infof("scheduler", "executeTasksAsync called: router=%s, interval=%v, lines=%d",
		s.router.IP, q.interval, len(lines))

	if len(lines) == 0 {
		ylog.Debugf("scheduler", "no tasks to execute (router=%s, interval=%v)", s.router.IP, q.interval)
		return
	}

	// 记录此次执行的所有IP
	var ips []string
	for _, line := range lines {
		ips = append(ips, line.IP)
	}
	ylog.Infof("scheduler", "executing individual ping tasks for IPs: %v (router=%s, interval=%v)",
		ips, s.router.IP, q.interval)

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
	//当前为pingTask指定cmdType
	t, err := s.manager.registry.Discover(
		"ping",
		s.router.Platform,
		s.router.Protocol,
		connection.CommandTypeCommands,
	)
	if err == nil {
		matchedTask = t
		matchedCmdType = connection.CommandTypeCommands
	}

	if matchedTask == nil {
		ylog.Errorf("scheduler", "no matching task for router=%s (supported=%v)",
			s.router.IP, supportedCmdTypes)
		return
	}

	// 为每个专线执行单独的ping任务
	for _, line := range lines {
		s.executeIndividualPing(line, matchedTask, matchedCmdType)
	}
}

// executeIndividualPing 执行单个专线的ping任务
func (s *RouterScheduler) executeIndividualPing(line syncer.Line, matchedTask task.Task, matchedCmdType task.CommandType) {
	// 获取连接
	conn, err := s.connection.Get(s.router.Protocol)
	if err != nil {
		ylog.Errorf("scheduler", "router=%s get connection failed for line %s: %v", s.router.IP, line.IP, err)
		return
	}

	// 创建单个任务上下文
	taskCtx := task.TaskContext{
		TaskType:    "ping",
		Platform:    s.router.Platform,
		Protocol:    s.router.Protocol,
		CommandType: matchedCmdType,
		Params: map[string]interface{}{
			"target_ip": line.IP, // 单个IP
			"repeat":    5,
			"timeout":   10 * time.Second,
		},
		Ctx: s.routerCtx,
	}

	// 记录开始时间
	startTime := time.Now()

	ylog.Debugf("scheduler", "submitting ping task for IP %s on router %s", line.IP, s.router.IP)

	// 异步提交任务
	err = s.asyncExecutor.Submit(matchedTask, conn, taskCtx, func(result task.Result, err error) {
		duration := time.Since(startTime)

		// 释放连接
		s.connection.Release(conn)

		// 处理结果
		if err != nil {
			ylog.Errorf("scheduler", "ping task execution failed for line %s on router %s: %v", line.IP, s.router.IP, err)
			// 创建失败结果
			failedResult := task.Result{
				Success: false,
				Error:   err.Error(),
				Data: map[string]interface{}{
					"target_ip": line.IP,
				},
			}
			if aggErr := s.manager.aggregator.SubmitTaskResult(line, "ping", failedResult, duration); aggErr != nil {
				ylog.Errorf("scheduler", "failed to submit failed result to aggregator for %s: %v", line.IP, aggErr)
			}
			return
		}

		// 提交成功结果
		ylog.Debugf("scheduler", "ping task completed for line %s on router %s: success=%v", line.IP, s.router.IP, result.Success)
		if aggErr := s.manager.aggregator.SubmitTaskResult(line, "ping", result, duration); aggErr != nil {
			ylog.Errorf("scheduler", "failed to submit result to aggregator for %s: %v", line.IP, aggErr)
		}
	})

	if err != nil {
		ylog.Errorf("scheduler", "failed to submit ping task for line %s on router %s: %v", line.IP, s.router.IP, err)
		s.connection.Release(conn)
	}
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

// GetSchedulerHealth 获取调度器健康状态（用于调试）
func (s *RouterScheduler) GetSchedulerHealth() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	health := map[string]interface{}{
		"router_ip":    s.router.IP,
		"total_lines":  len(s.lines),
		"total_queues": len(s.queues),
		"queues":       make(map[string]interface{}),
	}

	for interval, queue := range s.queues {
		lines := queue.GetTasksSnapshot()
		queueHealth := map[string]interface{}{
			"interval":     interval.String(),
			"line_count":   len(lines),
			"is_empty":     queue.IsEmpty(),
			"line_details": make([]map[string]string, 0),
		}

		for _, line := range lines {
			lineDetail := map[string]string{
				"id":       line.ID,
				"ip":       line.IP,
				"interval": line.Interval.String(),
			}
			queueHealth["line_details"] = append(
				queueHealth["line_details"].([]map[string]string),
				lineDetail,
			)
		}

		health["queues"].(map[string]interface{})[interval.String()] = queueHealth
	}

	return health
}

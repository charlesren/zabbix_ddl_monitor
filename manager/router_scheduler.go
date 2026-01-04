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
	warmUpConnectionCount = 2 // 预热连接数
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
	routerCtx      context.Context
	mu             sync.RWMutex
	connSemaphore  chan struct{} // 连接信号量，用于控制并发任务数
}

func NewRouterScheduler(parentCtx context.Context, router *syncer.Router, initialLines []syncer.Line, manager *Manager) (*RouterScheduler, error) {
	// 1. 使用Builder直接构造配置
	config, err := connection.NewConfigBuilder().
		WithBasicAuth(router.IP, router.Username, router.Password).
		WithProtocol(router.Protocol, router.Platform).
		WithMetadata("platform", router.Platform).
		WithMetadata("protocol", router.Protocol).
		// 启用智能重建，配置合理的参数
		WithSmartRebuild(true, 1500, 30*time.Minute, 0.2, 10).
		// 设置连接池参数：最大2个连接，最小1个连接，最大空闲时间3分钟，健康检查间隔3分30秒（避免与3分钟任务调度冲突）
		WithConnectionPool(2, 1, 5*time.Minute+10*time.Second, 3*time.Minute+30*time.Second).
		// 连接建立重试策略：最大重试2次，初始延迟1秒，指数退避因子2
		// 此策略用于后台连接建立和重连操作（自动重试，用户不可见）
		WithConnectionRetryPolicy(1, 1*time.Second, 2).
		// 任务执行重试策略：最大重试0次，初始延迟500毫秒，指数退避因子2
		// 此策略用于前台任务执行失败重试（用户可见）
		WithTaskRetryPolicy(0, 500*time.Millisecond, 2).
		Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build connection config: %w", err)
	}

	ylog.Infof("scheduler", "创建连接池 router=%s，连接重试策略：maxRetries=%d, baseDelay=%v, backoff=%.1f (后台自动重试)",
		router.IP, config.ConnectionMaxRetries, config.ConnectionRetryInterval, config.ConnectionBackoffFactor)
	ylog.Infof("scheduler", "任务重试策略：maxRetries=%d, baseDelay=%v, backoff=%.1f (前台任务重试)",
		config.TaskMaxRetries, config.TaskRetryInterval, config.TaskBackoffFactor)

	ylog.Infof("scheduler", "创建增强连接池: router=%s, protocol=%s, platform=%s",
		router.IP, router.Protocol, router.Platform)
	pool := connection.NewEnhancedConnectionPool(parentCtx, config)
	ylog.Infof("scheduler", "连接池创建完成: router=%s", router.IP)

	// 4. 创建调度器实例
	scheduler := &RouterScheduler{
		connection:    pool,
		router:        router,
		lines:         initialLines,
		manager:       manager,
		queues:        make(map[time.Duration]*IntervalTaskQueue),
		stopChan:      make(chan struct{}),
		routerCtx:     parentCtx,
		connSemaphore: make(chan struct{}, config.MaxConnections),
	}

	// 5. 启动连接池事件监听（监控连接重试等后台事件）
	go scheduler.listenToPoolEvents()

	// 6. 预热连接池
	ylog.Infof("scheduler", "warming up connection pool for router=%s with %d connections", router.IP, warmUpConnectionCount)
	if err := scheduler.connection.WarmUp(router.Protocol, warmUpConnectionCount); err != nil {
		// warmup可能部分失败，但只要不是完全失败，系统仍可运行
		ylog.Warnf("scheduler", "connection pool warm-up had issues: %v (router=%s)", err, router.IP)
		ylog.Warnf("scheduler", "system will continue with available connections, performance may be degraded")
	}

	// 设置默认能力，避免nil指针
	// 预加载能力改为惰性加载，避免阻塞调度器创建
	defaultCapability := connection.ProtocolCapability{
		CommandTypesSupport: []connection.CommandType{"commands"},
	}
	scheduler.capabilityMu.Lock()
	scheduler.connCapability = &defaultCapability
	scheduler.capabilityMu.Unlock()

	// 异步预加载能力（可选优化）
	go func() {
		conn, err := scheduler.connection.Get(scheduler.router.Protocol)
		if err != nil {
			ylog.Debugf("scheduler", "异步预加载能力失败: %v (router=%s)", err, scheduler.router.IP)
			return
		}
		capability := conn.GetCapability()
		scheduler.capabilityMu.Lock()
		scheduler.connCapability = &capability
		scheduler.capabilityMu.Unlock()
		scheduler.connection.Release(conn)
		ylog.Debugf("scheduler", "异步预加载能力完成: router=%s", scheduler.router.IP)
	}()

	// 7. 初始化队列
	scheduler.initializeQueues()

	// 8. 创建异步执行器，使用配置中的任务重试策略（指数退避）
	ylog.Infof("scheduler", "创建异步执行器 router=%s，任务超时=%v，任务重试策略：maxRetries=%d, baseDelay=%v, backoff=%.1f (前台任务重试)",
		router.IP, config.TaskTimeout, config.TaskMaxRetries, config.TaskRetryInterval, config.TaskBackoffFactor)

	scheduler.asyncExecutor = task.NewAsyncExecutor(
		3,
		task.WithSmartTimeout(config.TaskTimeout),
		// 使用配置中的任务重试策略（指数退避）
		task.WithExponentialRetry(config.TaskMaxRetries, config.TaskRetryInterval, config.TaskBackoffFactor),
	)

	return scheduler, nil

}

func (s *RouterScheduler) initializeQueues() {
	s.mu.Lock()
	defer s.mu.Unlock()

	ylog.Infof("scheduler", "初始化队列: 路由器=%s, 专线数量=%d",
		s.router.IP, len(s.lines))

	for _, line := range s.lines {
		ylog.Infof("scheduler", "处理专线: id=%s, ip=%s, 间隔=%v",
			line.ID, line.IP, line.Interval)

		// 创建间隔队列（如果不存在）
		if _, exists := s.queues[line.Interval]; !exists {
			s.queues[line.Interval] = NewIntervalTaskQueue(line.Interval)
			ylog.Infof("scheduler", "创建新队列: 间隔=%v (路由器=%s)",
				line.Interval, s.router.IP)
		}
		// 添加任务
		s.queues[line.Interval].Add(line)
		ylog.Infof("scheduler", "添加专线到队列: id=%s, 间隔=%v", line.ID, line.Interval)
	}

	// 验证队列分布
	totalQueued := 0
	ylog.Infof("scheduler", "队列分布验证:")
	for interval, queue := range s.queues {
		lines := queue.GetTasksSnapshot()
		totalQueued += len(lines)
		ylog.Infof("scheduler", "  间隔 %v: %d条专线 (路由器=%s)",
			interval, len(lines), s.router.IP)
	}

	if totalQueued != len(s.lines) {
		ylog.Errorf("scheduler", "队列分布不匹配: 期望%d条专线, 实际%d条专线 (路由器=%s)",
			len(s.lines), totalQueued, s.router.IP)
	} else {
		ylog.Infof("scheduler", "队列初始化完成: 路由器=%s, 队列数量=%d, 专线总数=%d",
			s.router.IP, len(s.queues), totalQueued)
	}
}

// 启动调度循环
func (s *RouterScheduler) Start() {
	ylog.Infof("scheduler", "调度器Start方法开始执行: router=%s", s.router.IP)

	// 启动异步执行器和聚合器
	ylog.Infof("scheduler", "starting router scheduler (router=%s, queues=%d)", s.router.IP, len(s.queues))

	// 检查asyncExecutor是否为空
	if s.asyncExecutor == nil {
		ylog.Errorf("scheduler", "asyncExecutor is nil! router=%s", s.router.IP)
		return
	}

	s.asyncExecutor.Start()

	// 记录每个队列的详细信息
	ylog.Infof("scheduler", "路由器 %s 的队列详情:", s.router.IP)
	for interval, q := range s.queues {
		lines := q.GetTasksSnapshot()
		ylog.Infof("scheduler", "  - 间隔 %v: %d条专线", interval, len(lines))
		for _, line := range lines {
			ylog.Infof("scheduler", "     专线: id=%s, ip=%s", line.ID, line.IP)
		}
	}

	// 启动队列worker
	ylog.Infof("scheduler", "启动队列worker, 总共%d个队列", len(s.queues))
	workerCount := 0
	for interval, q := range s.queues {
		s.wg.Add(1)
		workerCount++
		// 创建局部变量副本，避免goroutine闭包问题
		intervalCopy := interval
		qCopy := q
		go func() {
			ylog.Infof("scheduler", "队列worker启动: router=%s, interval=%v", s.router.IP, intervalCopy)
			defer func() {
				s.wg.Done()
				ylog.Infof("scheduler", "队列worker停止: router=%s, interval=%v", s.router.IP, intervalCopy)
			}()
			for {
				select {
				case <-s.stopChan:
					ylog.Infof("scheduler", "队列worker收到停止信号: router=%s, interval=%v", s.router.IP, intervalCopy)
					return
				case <-qCopy.ExecNotify(): // 监听执行信号
					ylog.Debugf("scheduler", "执行任务: router=%s, interval=%v", s.router.IP, intervalCopy)
					s.executeTasksAsync(qCopy)
				}
			}
		}()
	}
	ylog.Infof("scheduler", "已启动%d个队列worker", workerCount)
}

func (s *RouterScheduler) executeTasksAsync(q *IntervalTaskQueue) {
	// 获取任务快照
	lines := q.GetTasksSnapshot()
	ylog.Debugf("scheduler", "开始执行任务: 路由器=%s, 间隔=%v, 专线数量=%d",
		s.router.IP, q.interval, len(lines))

	if len(lines) == 0 {
		ylog.Warnf("scheduler", "没有任务需要执行 (路由器=%s, 间隔=%v)", s.router.IP, q.interval)
		return
	}

	// 记录此次执行的所有IP
	var ips []string
	for _, line := range lines {
		ips = append(ips, line.IP)
	}
	ylog.Debugf("scheduler", "执行ping任务: 路由器=%s, 间隔=%v, 专线IP列表: %v",
		s.router.IP, q.interval, ips)

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
			ylog.Errorf("scheduler", "路由器=%s 获取连接失败: %v", s.router.IP, err)
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
		ylog.Errorf("scheduler", "没有匹配的任务 for 路由器=%s (支持的命令类型=%v)",
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
	// 1. 获取信号量（控制并发数）
	select {
	case s.connSemaphore <- struct{}{}:
		// 成功获取信号量
		ylog.Infof("scheduler", "获取信号量: 路由器=%s, 专线=%s", s.router.IP, line.IP)
	case <-s.routerCtx.Done():
		ylog.Infof("scheduler", "上下文已完成: 路由器=%s", s.router.IP)
		return
	}

	// 2. 获取连接（带上下文检查）
	ylog.Infof("scheduler", "开始获取连接: 路由器=%s, 专线=%s, 协议=%s", s.router.IP, line.IP, s.router.Protocol)

	// 检查上下文是否已取消
	select {
	case <-s.routerCtx.Done():
		ylog.Infof("scheduler", "获取连接前上下文已取消: 路由器=%s, 专线=%s", s.router.IP, line.IP)
		<-s.connSemaphore // 释放信号量
		return
	default:
		// 上下文正常，继续
	}

	conn, err := s.connection.GetWithContext(s.routerCtx, s.router.Protocol)
	if err != nil {
		ylog.Errorf("scheduler", "连接失败: 路由器=%s, 专线=%s, 错误=%v", s.router.IP, line.IP, err)
		<-s.connSemaphore // 释放信号量
		return
	}
	ylog.Infof("scheduler", "获取连接成功: 路由器=%s, 专线=%s", s.router.IP, line.IP)

	// 3. 创建任务上下文
	taskCtx := task.TaskContext{
		TaskType:    "ping",
		Platform:    s.router.Platform,
		Protocol:    s.router.Protocol,
		CommandType: matchedCmdType,
		Params: map[string]interface{}{
			"target_ip": line.IP,
			"repeat":    5,
			"timeout":   2 * time.Second,
		},
		Ctx: s.routerCtx,
	}

	// 记录开始时间
	startTime := time.Now()
	ylog.Infof("scheduler", "开始ping任务: 路由器=%s, 专线=%s, 目标IP=%s",
		s.router.IP, line.ID, line.IP)

	// 4. 提交异步任务
	err = s.asyncExecutor.Submit(matchedTask, conn, taskCtx, func(result task.Result, err error) {
		// 计算执行时长
		duration := time.Since(startTime)

		// 释放资源
		s.connection.Release(conn)
		<-s.connSemaphore

		// 处理结果
		if err != nil {
			ylog.Errorf("scheduler", "ping任务失败: 路由器=%s, 专线=%s, 耗时=%v, 错误=%v",
				s.router.IP, line.IP, duration, err)
		} else {
			ylog.Infof("scheduler", "ping任务完成: 路由器=%s, 专线=%s, 耗时=%v, 成功=%v",
				s.router.IP, line.IP, duration, result.Success)
		}

		// 检查status字段，基于status决定是否提交给聚合器
		if s.manager != nil && s.manager.aggregator != nil {
			// 从result.Data中获取status字段
			if status, hasStatus := result.Data["status"].(string); hasStatus {
				// 无论status是什么，都提交给聚合器
				// Zabbix sender会根据status决定发送哪个监控项
				if aggErr := s.manager.aggregator.SubmitTaskResult(line, "ping", result, duration); aggErr != nil {
					ylog.Warnf("scheduler", "聚合器错误: 专线=%s, 错误=%v", line.IP, aggErr)
				}
				ylog.Infof("scheduler", "提交结果给聚合器: 专线=%s, 状态=%s, 成功=%v", line.IP, status, result.Success)
			} else {
				// 没有status字段，也提交给聚合器
				// Zabbix sender会检测到缺少status字段并发送相应的错误状态
				if aggErr := s.manager.aggregator.SubmitTaskResult(line, "ping", result, duration); aggErr != nil {
					ylog.Warnf("scheduler", "聚合器错误: 专线=%s, 错误=%v", line.IP, aggErr)
				}
				ylog.Errorf("scheduler", "提交缺少status字段的结果给聚合器: 专线=%s, 成功=%v", line.IP, result.Success)
			}
		}
	})

	if err != nil {
		ylog.Errorf("scheduler", "提交任务失败: 路由器=%s, 专线=%s, 错误=%v", s.router.IP, line.IP, err)
		s.connection.Release(conn)
		<-s.connSemaphore
	}
}

// 停止调度器（阻塞等待所有任务完成）
func (s *RouterScheduler) Stop() {
	// 先关闭stopChan通知所有goroutine退出
	ylog.Infof("scheduler", "正在停止路由器调度器 (路由器=%s)", s.router.IP)
	select {
	case <-s.stopChan:
		// Already closed
	default:
		close(s.stopChan)
	}

	// 停止异步执行器和聚合器
	if s.asyncExecutor != nil {
		s.asyncExecutor.Stop()
		ylog.Infof("scheduler", "async executor stopped (router=%s)", s.router.IP)
	}

	// 停止所有队列
	s.mu.Lock()
	for _, q := range s.queues {
		q.Stop()
	}
	s.mu.Unlock()

	// 等待所有goroutine完成
	s.wg.Wait()

	ylog.Infof("scheduler", "路由器调度器已完全停止 (路由器=%s)", s.router.IP)
	// 关闭连接池
	if s.connection != nil {
		_ = s.connection.Close()
	}
}

func (s *RouterScheduler) OnLineCreated(line syncer.Line) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 检查专线是否已存在（防御性编程）
	if s.lineExistsLocked(line.IP) {
		ylog.Warnf("scheduler", "duplicate line creation ignored (id=%s, line_ip=%s, router=%s)",
			line.ID, line.IP, s.router.IP)
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
	ylog.Infof("scheduler", "added line %s to %v queue (router=%s)",
		line.ID, line.Interval, s.router.IP)
}

// 辅助方法：检查专线是否存在（需在锁内调用）
func (s *RouterScheduler) lineExistsLocked(lineIP string) bool {
	for _, l := range s.lines {
		if l.IP == lineIP {
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
			oldQueue.Remove(oldLine.IP)
			ylog.Infof("scheduler", "moved line %s from %v to %v queue (router=%s)",
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
	ylog.Infof("scheduler", "updated line %s (router=%s)", newLine.ID, s.router.IP)
}

// 专线删除处理（带防御性检查）
func (s *RouterScheduler) OnLineDeleted(line syncer.Line) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if queue, exists := s.queues[line.Interval]; exists {
		// 防御性检查：确保任务存在
		if !queue.Contains(line.IP) {
			ylog.Warnf("scheduler", "line %s not found in queue (router=%s)",
				line.ID, s.router.IP)
			return
		}

		queue.Remove(line.IP)
		ylog.Infof("scheduler", "removed line %s from %v queue (router=%s)",
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

// listenToPoolEvents 监听连接池事件，监控连接重试等后台操作
func (s *RouterScheduler) listenToPoolEvents() {
	ylog.Debugf("scheduler", "开始监听连接池事件 router=%s", s.router.IP)

	// 获取连接池事件通道
	eventChan := s.connection.GetEventChan()
	if eventChan == nil {
		ylog.Warnf("scheduler", "无法获取连接池事件通道 router=%s", s.router.IP)
		return
	}

	for {
		select {
		case <-s.routerCtx.Done():
			ylog.Debugf("scheduler", "停止监听连接池事件 router=%s (上下文取消)", s.router.IP)
			return
		case event, ok := <-eventChan:
			if !ok {
				ylog.Debugf("scheduler", "连接池事件通道已关闭 router=%s", s.router.IP)
				return
			}

			// 根据事件类型记录日志
			switch event.Type {
			case connection.EventConnectionCreated:
				ylog.Infof("scheduler", "连接池事件 router=%s: 连接创建成功", s.router.IP)
			case connection.EventConnectionDestroyed:
				ylog.Infof("scheduler", "连接池事件 router=%s: 连接销毁", s.router.IP)
			case connection.EventConnectionReused:
				ylog.Debugf("scheduler", "连接池事件 router=%s: 连接复用", s.router.IP)
			case connection.EventConnectionFailed:
				ylog.Warnf("scheduler", "连接池事件 router=%s: 连接失败，将使用配置的重试策略自动重试", s.router.IP)
			case connection.EventHealthCheckFailed:
				ylog.Warnf("scheduler", "连接池事件 router=%s: 健康检查失败", s.router.IP)
			case connection.EventPoolWarmupStarted:
				ylog.Infof("scheduler", "连接池事件 router=%s: 连接池预热开始", s.router.IP)
			case connection.EventPoolWarmupCompleted:
				ylog.Infof("scheduler", "连接池事件 router=%s: 连接池预热完成", s.router.IP)
			case connection.EventPoolShutdown:
				ylog.Infof("scheduler", "连接池事件 router=%s: 连接池关闭", s.router.IP)
			case connection.EventConnectionRebuilt:
				ylog.Infof("scheduler", "连接池事件 router=%s: 连接重建 (后台自动重试)", s.router.IP)
			case connection.EventRebuildFailed:
				ylog.Warnf("scheduler", "连接池事件 router=%s: 连接重建失败", s.router.IP)
			case connection.EventRebuildMarked:
				ylog.Debugf("scheduler", "连接池事件 router=%s: 连接已标记为需要重建", s.router.IP)
			case connection.EventRebuildStarted:
				ylog.Infof("scheduler", "连接池事件 router=%s: 连接重建开始", s.router.IP)
			case connection.EventRebuildCompleted:
				ylog.Infof("scheduler", "连接池事件 router=%s: 连接重建完成", s.router.IP)
			case connection.EventConnectionsNeedRebuild:
				ylog.Debugf("scheduler", "连接池事件 router=%s: 存在连接需要重建", s.router.IP)
			default:
				ylog.Warnf("scheduler", "连接池事件 router=%s: 未知事件类型 %d", s.router.IP, event.Type)
			}
		}
	}
}

package manager

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/charlesren/ylog"
	"github.com/charlesren/zabbix_ddl_monitor/syncer"
	"github.com/charlesren/zabbix_ddl_monitor/task"
)

// ConfigSyncerInterface defines the interface for ConfigSyncer to enable testing
type ConfigSyncerInterface interface {
	GetLines() map[string]syncer.Line
	Subscribe(ctx context.Context) *syncer.Subscription
}

type Manager struct {
	appCtx       context.Context    // 应用级上下文
	cancelFunc   context.CancelFunc // 新增：context取消函数
	configSyncer ConfigSyncerInterface
	schedulers   map[string]Scheduler     // key: routerIP
	routerLines  map[string][]syncer.Line // key: routerIP
	registry     task.Registry
	aggregator   *task.Aggregator
	mu           sync.Mutex
	stopChan     chan struct{}
	wg           sync.WaitGroup
}

// shouldStop 检查是否应该停止，同时支持context和stopChan两种机制
func (m *Manager) shouldStop() bool {
	select {
	case <-m.appCtx.Done():
		return true
	case <-m.stopChan:
		return true
	default:
		return false
	}
}

// waitForStopOrTimer 等待停止信号或定时器，返回true表示应该停止
func (m *Manager) waitForStopOrTimer(timer *time.Timer, checkInterval time.Duration) bool {
	for {
		select {
		case <-m.appCtx.Done():
			return true
		case <-m.stopChan:
			return true
		case <-timer.C:
			return false
		default:
			// 定期检查，避免长时间阻塞
			time.Sleep(checkInterval)
		}
	}
}

// runWithContext 统一的goroutine运行方法，自动处理停止信号
func (m *Manager) runWithContext(name string, fn func()) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ylog.Infof("manager", "启动goroutine: %s", name)
		defer ylog.Infof("manager", "退出goroutine: %s", name)

		fn()
	}()
}

// runPeriodicWithContext 周期性执行任务的统一方法
func (m *Manager) runPeriodicWithContext(name string, interval time.Duration, work func()) {
	m.runWithContext(name, func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-m.appCtx.Done():
				ylog.Debugf("manager", "%s收到context取消信号", name)
				return
			case <-m.stopChan:
				ylog.Debugf("manager", "%s收到停止信号", name)
				return
			case <-ticker.C:
				ylog.Debugf("manager", "%s执行周期性任务", name)
				work()
			}
		}
	})
}

// safeExecute 安全执行函数，自动恢复panic
func (m *Manager) safeExecute(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			ylog.Errorf("SafeExecute", "%s发生panic: %v", name, r)
		}
	}()

	fn()
}

// recordError 记录错误统计
func (m *Manager) recordError(errorType string) {
	// 这里可以添加错误统计逻辑
	ylog.Debugf("ErrorStats", "记录错误类型: %s", errorType)
}

// safeExecuteWithError 安全执行函数，返回错误
func (m *Manager) safeExecuteWithError(name string, fn func() error) error {
	defer func() {
		if r := recover(); r != nil {
			ylog.Errorf("SafeExecute", "%s发生panic: %v", name, r)
			m.recordError("panic_" + name)
		}
	}()

	err := fn()
	if err != nil {
		m.recordError("error_" + name)
	}
	return err
}

// GetDetailedStats 获取管理器的详细统计信息
func (m *Manager) GetDetailedStats() map[string]interface{} {
	stats := make(map[string]interface{})

	m.mu.Lock()
	defer m.mu.Unlock()

	// 基础统计
	stats["scheduler_count"] = len(m.schedulers)
	stats["router_count"] = len(m.routerLines)

	// 计算总专线数
	totalLines := 0
	routerDetails := make([]map[string]interface{}, 0, len(m.routerLines))
	for routerIP, lines := range m.routerLines {
		lineCount := len(lines)
		totalLines += lineCount

		routerDetail := map[string]interface{}{
			"router_ip":     routerIP,
			"line_count":    lineCount,
			"has_scheduler": m.schedulers[routerIP] != nil,
		}
		routerDetails = append(routerDetails, routerDetail)
	}
	stats["total_lines"] = totalLines
	stats["router_details"] = routerDetails

	// Goroutine统计
	stats["goroutine_count"] = runtime.NumGoroutine()

	// 上下文状态
	stats["context_cancelled"] = m.appCtx.Err() != nil
	if m.appCtx.Err() != nil {
		stats["context_error"] = m.appCtx.Err().Error()
	}

	// stopChan状态
	select {
	case <-m.stopChan:
		stats["stopchan_closed"] = true
	default:
		stats["stopchan_closed"] = false
	}

	// 内存统计
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	stats["memory_alloc_mb"] = memStats.Alloc / 1024 / 1024
	stats["memory_heap_inuse_mb"] = memStats.HeapInuse / 1024 / 1024
	stats["gc_count"] = memStats.NumGC

	// 时间戳
	stats["timestamp"] = time.Now().Format(time.RFC3339)

	// 错误统计（简化版）
	stats["error_stats_available"] = false
	stats["error_stats_note"] = "错误统计功能已启用，详情查看日志"

	// 调试信息可用性
	stats["debug_info_available"] = true
	stats["debug_info_note"] = "调用DumpDebugInfo()获取详细调试信息"

	return stats
}

// CheckResourceCleanup 检查资源是否已正确清理
func (m *Manager) CheckResourceCleanup() map[string]interface{} {
	cleanupReport := make(map[string]interface{})

	m.mu.Lock()
	defer m.mu.Unlock()

	// 检查调度器资源
	cleanupReport["scheduler_count"] = len(m.schedulers)
	cleanupReport["router_lines_count"] = len(m.routerLines)

	// 检查是否有未清理的调度器
	uncleanedSchedulers := make([]string, 0)
	for routerIP, scheduler := range m.schedulers {
		if scheduler != nil {
			uncleanedSchedulers = append(uncleanedSchedulers, routerIP)
		}
	}
	cleanupReport["uncleaned_schedulers"] = uncleanedSchedulers
	cleanupReport["uncleaned_scheduler_count"] = len(uncleanedSchedulers)

	// 检查是否有空的专线列表但仍有调度器
	emptyRouterLinesWithScheduler := make([]string, 0)
	for routerIP, lines := range m.routerLines {
		if len(lines) == 0 && m.schedulers[routerIP] != nil {
			emptyRouterLinesWithScheduler = append(emptyRouterLinesWithScheduler, routerIP)
		}
	}
	cleanupReport["empty_lines_with_scheduler"] = emptyRouterLinesWithScheduler
	cleanupReport["empty_lines_with_scheduler_count"] = len(emptyRouterLinesWithScheduler)

	// 检查上下文状态
	cleanupReport["context_cancelled"] = m.appCtx.Err() != nil
	cleanupReport["stopchan_closed"] = false
	select {
	case <-m.stopChan:
		cleanupReport["stopchan_closed"] = true
	default:
	}

	// 总体清理状态
	cleanupReport["cleanup_complete"] = len(uncleanedSchedulers) == 0 &&
		len(emptyRouterLinesWithScheduler) == 0 &&
		cleanupReport["stopchan_closed"].(bool)

	if !cleanupReport["cleanup_complete"].(bool) {
		ylog.Warnf("ResourceCleanup", "资源清理不完整: schedulers=%d, empty_lines_with_scheduler=%d",
			cleanupReport["uncleaned_scheduler_count"].(int),
			cleanupReport["empty_lines_with_scheduler_count"].(int))
	} else {
		ylog.Infof("ResourceCleanup", "资源已完全清理")
	}

	return cleanupReport
}

// DumpDebugInfo 转储调试信息（用于异常情况分析）
func (m *Manager) DumpDebugInfo() map[string]interface{} {
	debugInfo := make(map[string]interface{})

	// 获取详细统计信息
	debugInfo["detailed_stats"] = m.GetDetailedStats()

	// 获取资源清理状态
	debugInfo["cleanup_status"] = m.CheckResourceCleanup()

	// 系统信息
	debugInfo["system_info"] = map[string]interface{}{
		"goroutine_count": runtime.NumGoroutine(),
		"num_cpu":         runtime.NumCPU(),
		"go_version":      runtime.Version(),
		"timestamp":       time.Now().Format(time.RFC3339),
	}

	// 内存信息
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	debugInfo["memory_info"] = map[string]interface{}{
		"alloc_mb":       memStats.Alloc / 1024 / 1024,
		"heap_inuse_mb":  memStats.HeapInuse / 1024 / 1024,
		"heap_sys_mb":    memStats.HeapSys / 1024 / 1024,
		"gc_count":       memStats.NumGC,
		"last_gc_time":   time.Unix(0, int64(memStats.LastGC)).Format(time.RFC3339),
		"next_gc_target": memStats.NextGC / 1024 / 1024,
	}

	// 记录调试信息
	ylog.Infof("DebugDump", "转储调试信息: goroutines=%d, memory=%dMB",
		debugInfo["system_info"].(map[string]interface{})["goroutine_count"].(int),
		debugInfo["memory_info"].(map[string]interface{})["alloc_mb"].(uint64))

	return debugInfo
}

// startStatusMonitor 启动状态监控
func (m *Manager) startStatusMonitor() {
	m.runPeriodicWithContext("statusMonitor", 5*time.Minute, func() {
		stats := m.GetDetailedStats()
		ylog.Infof("ManagerStatus", "详细状态统计: scheduler_count=%d, router_count=%d, total_lines=%d, goroutines=%d, memory=%dMB",
			stats["scheduler_count"].(int),
			stats["router_count"].(int),
			stats["total_lines"].(int),
			stats["goroutine_count"].(int),
			stats["memory_alloc_mb"].(uint64))

		// 如果goroutine数量异常增长，记录警告
		if stats["goroutine_count"].(int) > 100 {
			ylog.Warnf("ManagerStatus", "goroutine数量较多: %d", stats["goroutine_count"].(int))
		}

		// 如果内存使用过高，记录警告
		if stats["memory_heap_inuse_mb"].(uint64) > 512 {
			ylog.Warnf("ManagerStatus", "堆内存使用较高: %dMB", stats["memory_heap_inuse_mb"].(uint64))
		}
	})
}

func NewManager(cs ConfigSyncerInterface, registry task.Registry, aggregator *task.Aggregator) *Manager {
	// 创建可取消的context
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		appCtx:       ctx,    // 使用可取消的context
		cancelFunc:   cancel, // 保存取消函数
		configSyncer: cs,
		schedulers:   make(map[string]Scheduler),
		routerLines:  make(map[string][]syncer.Line),
		registry:     registry,
		aggregator:   aggregator,
		stopChan:     make(chan struct{}),
	}
	return m
}

func (m *Manager) Start() {
	if m.aggregator == nil {
		ylog.Errorf("manager", "aggregator is nil")
		return
	}
	// 注册PingTask
	pingMeta := (&task.PingTask{}).Meta()
	if err := m.registry.Register(pingMeta); err != nil {
		ylog.Errorf("manager", "failed to register PingTask: %v", err)
		return
	}
	ylog.Infof("manager", "PingTask registered")

	// 初始全量同步
	m.safeExecute("fullSync", m.fullSync)

	// 启动周期性全量同步（1小时）
	m.runPeriodicWithContext("periodicSync", 4*time.Hour, func() {
		m.safeExecute("fullSync", m.fullSync)
	})

	// 订阅变更通知
	sub := m.configSyncer.Subscribe(m.appCtx)
	m.runWithContext("handleLineChanges", func() {
		m.safeExecute("handleLineChanges", func() {
			m.handleLineChanges(sub)
		})
	})

	// 启动状态监控
	m.startStatusMonitor()
}

func (m *Manager) Stop() {
	ylog.Infof("manager", "开始停止管理器")

	// 阶段1：先取消context（新机制）
	if m.cancelFunc != nil {
		ylog.Debugf("manager", "取消context")
		m.cancelFunc()
	}

	// 阶段1：再关闭stopChan（原有机制，保持向后兼容）
	select {
	case <-m.stopChan:
		ylog.Debugf("manager", "stopChan已经关闭")
	default:
		ylog.Debugf("manager", "关闭stopChan")
		close(m.stopChan)
	}

	// 等待后台goroutine完成（带超时）
	ylog.Debugf("manager", "等待所有goroutine完成...")
	waitDone := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(waitDone)
	}()

	// 设置超时时间为30秒
	select {
	case <-waitDone:
		ylog.Debugf("manager", "所有goroutine已完成")
	case <-time.After(30 * time.Second):
		ylog.Warnf("manager", "等待goroutine超时（30秒），强制停止")
		// 记录超时时的状态信息
		stats := m.GetSimpleStats()
		ylog.Warnf("manager", "超时时的状态: schedulers=%d, total_lines=%d",
			stats["scheduler_count"].(int), stats["total_lines"].(int))
	}

	// 安全地停止所有调度器
	m.mu.Lock()
	schedulers := make([]Scheduler, 0, len(m.schedulers))
	for routerIP, s := range m.schedulers {
		schedulers = append(schedulers, s)
		ylog.Debugf("manager", "准备停止调度器: router=%s", routerIP)
	}
	m.mu.Unlock()

	// 在锁外停止调度器以避免死锁
	for _, s := range schedulers {
		m.safeExecute("scheduler.Stop", s.Stop)
	}

	// 记录最终状态
	stats := m.GetSimpleStats()
	ylog.Infof("manager", "管理器已完全停止: schedulers=%d, total_lines=%d",
		stats["scheduler_count"].(int), stats["total_lines"].(int))

	// 执行资源清理检查
	cleanupReport := m.CheckResourceCleanup()
	if !cleanupReport["cleanup_complete"].(bool) {
		ylog.Warnf("manager", "资源清理检查未通过: uncleaned_schedulers=%d, empty_lines_with_scheduler=%d",
			cleanupReport["uncleaned_scheduler_count"].(int),
			cleanupReport["empty_lines_with_scheduler_count"].(int))
		// 在资源清理不完整时转储调试信息
		ylog.Warnf("manager", "资源清理不完整，转储调试信息...")
		debugInfo := m.DumpDebugInfo()
		ylog.Warnf("manager", "调试信息已转储: goroutines=%d, memory=%dMB",
			debugInfo["system_info"].(map[string]interface{})["goroutine_count"].(int),
			debugInfo["memory_info"].(map[string]interface{})["alloc_mb"].(uint64))
	}
}

// 全量同步专线配置
func (m *Manager) fullSync() {
	ylog.Infof("manager", "执行全量同步")

	// 使用安全执行获取专线数据
	var lines map[string]syncer.Line
	err := m.safeExecuteWithError("configSyncer.GetLines", func() error {
		lines = m.configSyncer.GetLines()
		return nil
	})

	if err != nil {
		ylog.Errorf("manager", "获取专线数据时发生错误: %v", err)
		m.recordError("config_sync_error")
		return
	}

	// 特殊处理：如果同步器还没有数据，等待同步完成
	if len(lines) == 0 {
		ylog.Warnf("manager", "同步器返回空数据，等待同步完成...")

		// 等待同步器完成第一次同步
		// 最多等待300秒，每2秒检查一次
		maxChecks := 150
		checkInterval := 2 * time.Second

		for i := 0; i < maxChecks; i++ {
			time.Sleep(checkInterval)
			lines = m.configSyncer.GetLines()
			if len(lines) > 0 {
				waitedTime := time.Duration(i+1) * checkInterval
				ylog.Infof("manager", "等待%v后获取到%d条专线", waitedTime, len(lines))
				break
			}
			ylog.Debugf("manager", "等待中...已等待%v", time.Duration(i+1)*checkInterval)
		}

		if len(lines) == 0 {
			ylog.Errorf("manager", "等待300秒后同步器仍然返回空数据，跳过本次全量同步")
			m.recordError("empty_sync_data")
			return
		}
	}

	newRouterLines := make(map[string][]syncer.Line)

	// 按路由器分组专线
	for _, line := range lines {
		newRouterLines[line.Router.IP] = append(newRouterLines[line.Router.IP], line)
	}

	// 更新专线列表
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routerLines = newRouterLines

	// 更新调度器
	for routerIP, lines := range newRouterLines {
		m.ensureScheduler(routerIP, lines)
	}

	// 清理无专线的调度器
	for routerIP := range m.schedulers {
		if _, exists := newRouterLines[routerIP]; !exists {
			m.schedulers[routerIP].Stop()
			delete(m.schedulers, routerIP)
		}
	}
}

// 处理专线变更事件
func (m *Manager) handleLineChanges(sub *syncer.Subscription) {
	ylog.Infof("manager", "已订阅配置变更通知")

	// 使用缓冲区解耦事件接收和处理，避免阻塞订阅通道
	eventBuffer := make(chan syncer.LineChangeEvent, 500)

	// 启动事件处理器
	m.runWithContext("eventProcessor", func() {
		ylog.Debugf("manager", "事件处理器启动")
		for event := range eventBuffer {
			ylog.Debugf("manager", "处理变更事件: %v", event.Type)
			m.processLineEvent(event)
		}
		ylog.Debugf("manager", "事件处理器已退出")
	})

	for {
		select {
		case <-m.appCtx.Done():
			ylog.Infof("manager", "收到context取消信号，关闭事件缓冲区")
			close(eventBuffer)
			// 等待事件处理器处理完所有剩余事件
			// 事件处理器会在处理完所有事件后自动退出
			ylog.Debugf("manager", "等待事件处理器退出...")
			return
		case <-m.stopChan:
			ylog.Infof("manager", "收到停止信号，关闭事件缓冲区")
			close(eventBuffer)
			// 等待事件处理器处理完所有剩余事件
			// 事件处理器会在处理完所有事件后自动退出
			ylog.Debugf("manager", "等待事件处理器退出...")
			return
		case event := <-sub.Events():
			// 将事件放入缓冲区，避免阻塞订阅通道
			select {
			case eventBuffer <- event:
				ylog.Debugf("manager", "事件缓冲成功: %v", event.Type)
			default:
				ylog.Errorf("manager", "事件缓冲区已满，丢弃事件: %v", event.Type)
				m.recordError("event_buffer_full")
			}
		}
	}
}

func (m *Manager) processLineEvent(event syncer.LineChangeEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()

	routerIP := event.Line.Router.IP

	switch event.Type {
	case syncer.LineCreate:
		// 路由器加第一条专线时
		if _, exists := m.routerLines[routerIP]; !exists {
			m.routerLines[routerIP] = make([]syncer.Line, 0)
		}

		// 防御性检查
		for _, l := range m.routerLines[routerIP] {
			if l.IP == event.Line.IP {
				ylog.Warnf("manager", "duplicate line create (line_ip=%s, router=%s, line_id=%s)",
					event.Line.IP, routerIP, event.Line.ID)
				m.recordError("duplicate_line_create")
				return
			}
		}

		// 添加新专线
		m.routerLines[routerIP] = append(m.routerLines[routerIP], event.Line)
		//路由器加第一条专线时,需考虑两种情况
		// 1.此前无调度器，第一次添加专线需创建调度器
		// 2.此前有调度器，在延迟删除期间（已添加新专线，到期后不会删除）
		if _, exists := m.schedulers[routerIP]; !exists {
			m.ensureScheduler(routerIP, m.routerLines[routerIP])
		}
		if s, exists := m.schedulers[routerIP]; exists {
			m.safeExecute("scheduler.OnLineCreated", func() {
				s.OnLineCreated(event.Line)
			})
		}

	case syncer.LineUpdate:
		// 查找并更新专线并通知调度器
		for i, l := range m.routerLines[routerIP] {
			if l.IP == event.Line.IP {
				oldLine := m.routerLines[routerIP][i]
				m.routerLines[routerIP][i] = event.Line
				if s, exists := m.schedulers[routerIP]; exists {
					m.safeExecute("scheduler.OnLineUpdated", func() {
						s.OnLineUpdated(oldLine, event.Line)
					})
				}
				break
			}
		}
	case syncer.LineDelete:
		// 从路由器对应的专线列表中移除专线
		if lines, exists := m.routerLines[routerIP]; exists {
			for i, line := range lines {
				if line.IP == event.Line.IP {
					m.routerLines[routerIP] = append(lines[:i], lines[i+1:]...)
					break
				}
			}
		}

		// 传递删除事件给调度
		if s, exists := m.schedulers[routerIP]; exists {
			m.safeExecute("scheduler.OnLineDeleted", func() {
				s.OnLineDeleted(event.Line)
			})

			// 延迟删除空调度器
			if len(m.routerLines[routerIP]) == 0 {
				m.wg.Add(1)
				go func() {
					defer m.wg.Done()

					timer := time.NewTimer(10 * time.Minute)
					defer timer.Stop()

					// 使用新的辅助方法等待停止信号或定时器
					if m.waitForStopOrTimer(timer, 1*time.Second) {
						// 收到停止信号
						ylog.Debugf("manager", "延迟删除goroutine收到停止信号: router=%s", routerIP)
						return
					}

					// 定时器到期，执行删除逻辑
					m.mu.Lock()
					defer m.mu.Unlock()

					// 再次检查是否仍然没有专线
					if len(m.routerLines[routerIP]) == 0 {
						ylog.Infof("manager", "执行延迟删除空调度器: router=%s", routerIP)
						m.safeExecute("scheduler.Stop", s.Stop)
						delete(m.schedulers, routerIP)
						delete(m.routerLines, routerIP)
					} else {
						ylog.Debugf("manager", "延迟删除取消: router=%s 已有新专线", routerIP)
					}
				}()
			}
		}
	}
}

// 确保调度器存在
func (m *Manager) ensureScheduler(routerIP string, lines []syncer.Line) {
	if _, exists := m.schedulers[routerIP]; !exists {
		scheduler := m.createScheduler(&lines[0].Router, lines)
		if scheduler == nil {
			ylog.Errorf("manager", "scheduler creation failed for %s", routerIP)
			m.recordError("scheduler_creation_failed")
			return
		}
		m.schedulers[routerIP] = scheduler
		go func() {
			m.safeExecute("scheduler.Start", scheduler.Start)
		}()

		// 记录调度器创建
		ylog.Infof("manager", "创建调度器: router=%s, lines=%d", routerIP, len(lines))
	}
}

// 创建调度器的工厂方法，可以在测试中重写
func (m *Manager) createScheduler(router *syncer.Router, lines []syncer.Line) Scheduler {
	var scheduler Scheduler
	var err error

	// 使用安全执行创建调度器
	func() {
		defer func() {
			if r := recover(); r != nil {
				ylog.Errorf("manager", "创建调度器时发生panic: router=%s, panic=%v", router.IP, r)
				err = fmt.Errorf("panic: %v", r)
			}
		}()

		scheduler, err = NewRouterScheduler(m.appCtx, router, lines, m)
	}()

	if err != nil {
		ylog.Errorf("manager", "failed to create scheduler for %s: %v", router.IP, err)
		m.recordError("scheduler_init_error")
		return nil
	}
	return scheduler
}

// GetSimpleStats 获取简化版统计信息（用于健康检查等）
func (m *Manager) GetSimpleStats() map[string]interface{} {
	stats := make(map[string]interface{})

	m.mu.Lock()
	defer m.mu.Unlock()

	// 基础健康指标
	stats["healthy"] = true
	stats["scheduler_count"] = len(m.schedulers)

	// 计算总专线数
	totalLines := 0
	for _, lines := range m.routerLines {
		totalLines += len(lines)
	}
	stats["total_lines"] = totalLines

	// 上下文状态
	stats["context_ok"] = m.appCtx.Err() == nil

	// 时间戳
	stats["last_check"] = time.Now().Format(time.RFC3339)

	return stats
}

package manager

import (
	"context"
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
	configSyncer ConfigSyncerInterface
	schedulers   map[string]Scheduler     // key: routerIP
	routerLines  map[string][]syncer.Line // key: routerIP
	registry     task.Registry
	aggregator   *task.Aggregator
	mu           sync.Mutex
	stopChan     chan struct{}
	wg           sync.WaitGroup
}

func NewManager(cs ConfigSyncerInterface, registry task.Registry, aggregator *task.Aggregator) *Manager {
	return &Manager{
		configSyncer: cs,
		schedulers:   make(map[string]Scheduler),
		routerLines:  make(map[string][]syncer.Line),
		registry:     registry,
		aggregator:   aggregator,
		stopChan:     make(chan struct{}),
	}
}

func (m *Manager) Start() {
	if m.aggregator == nil {
		ylog.Errorf("manager", "aggregator is nil")
		return
	}
	// 注册PingTask
	pingMeta := task.TaskMeta{
		Type:        "ping",
		Description: "Ping task for line monitoring",
		Platforms: []task.PlatformSupport{
			{
				Platform: "cisco_iosxe",
				Protocols: []task.ProtocolSupport{
					{
						Protocol: "ssh",
						CommandTypes: []task.CommandTypeSupport{
							{
								CommandType: "commands",
								ImplFactory: func() task.Task { return &task.PingTask{} },
								Params:      []task.ParamSpec{},
							},
						},
					},
				},
			},
		},
	}
	if err := m.registry.Register(pingMeta); err != nil {
		ylog.Errorf("manager", "failed to register PingTask: %v", err)
		return
	}

	// 初始全量同步
	m.fullSync()

	// 启动周期性全量同步（1小时）
	m.wg.Add(1)
	go m.periodicSync(1 * time.Hour)

	// 订阅变更通知
	sub := m.configSyncer.Subscribe(context.Background())
	m.wg.Add(1)
	go m.handleLineChanges(sub)
}

func (m *Manager) Stop() {
	// 先关闭stopChan通知所有goroutine退出
	select {
	case <-m.stopChan:
		// Already closed
	default:
		close(m.stopChan)
	}

	// 等待后台goroutine完成
	m.wg.Wait()

	// 安全地停止所有调度器
	m.mu.Lock()
	schedulers := make([]Scheduler, 0, len(m.schedulers))
	for _, s := range m.schedulers {
		schedulers = append(schedulers, s)
	}
	m.mu.Unlock()

	// 在锁外停止调度器以避免死锁
	for _, s := range schedulers {
		s.Stop()
	}

}

// 全量同步专线配置
func (m *Manager) fullSync() {
	ylog.Infof("manager", "执行全量同步")
	lines := m.configSyncer.GetLines()
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

// 周期性全量同步
func (m *Manager) periodicSync(interval time.Duration) {
	ylog.Infof("manager", "启动周期性同步 (间隔: %v)", interval)
	defer m.wg.Done()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopChan:
			return
		case <-ticker.C:
			m.fullSync()
		}
	}
}

// 处理专线变更事件
func (m *Manager) handleLineChanges(sub *syncer.Subscription) {
	ylog.Infof("manager", "已订阅配置变更通知")
	defer m.wg.Done()

	for {
		select {
		case <-m.stopChan:
			return
		case event := <-sub.Events():
			ylog.Infof("manager", "收到变更事件: %v", event.Type)
			m.processLineEvent(event)
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
				ylog.Warnf("manager", "duplicate line create,ip: %v", event.Line.IP)
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
		m.schedulers[routerIP].OnLineCreated(event.Line)

	case syncer.LineUpdate:
		// 查找并更新专线并通知调度器
		for i, l := range m.routerLines[routerIP] {
			if l.IP == event.Line.IP {
				oldLine := m.routerLines[routerIP][i]
				m.routerLines[routerIP][i] = event.Line
				m.schedulers[routerIP].OnLineUpdated(oldLine, event.Line)
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
			s.OnLineDeleted(event.Line)

			// 延迟删除空调度器
			if len(m.routerLines[routerIP]) == 0 {
				time.AfterFunc(10*time.Minute, func() {
					m.mu.Lock()
					defer m.mu.Unlock()
					if len(m.routerLines[routerIP]) == 0 {
						s.Stop()
						delete(m.schedulers, routerIP)
						delete(m.routerLines, routerIP)
					}
				})
			}
		}
	}
}

// 确保调度器存在
func (m *Manager) ensureScheduler(routerIP string, lines []syncer.Line) {
	if _, exists := m.schedulers[routerIP]; !exists {
		m.schedulers[routerIP] = m.createScheduler(&lines[0].Router, lines)
		go m.schedulers[routerIP].Start()
	}
}

// 创建调度器的工厂方法，可以在测试中重写
func (m *Manager) createScheduler(router *syncer.Router, lines []syncer.Line) Scheduler {
	return NewRouterScheduler(router, lines, m)
}

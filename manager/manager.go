package manager

import (
	"context"
	"sync"
	"time"

	"github.com/charlesren/zabbix_ddl_monitor/syncer"
)

type Manager struct {
	configSyncer *syncer.ConfigSyncer
	schedulers   map[string]*RouterScheduler // key: routerIP
	routerLines  map[string][]syncer.Line    // key: routerIP
	mu           sync.Mutex
	stopChan     chan struct{}
	wg           sync.WaitGroup
}

func NewManager(cs *syncer.ConfigSyncer) *Manager {
	return &Manager{
		configSyncer: cs,
		schedulers:   make(map[string]*RouterScheduler),
		routerLines:  make(map[string][]syncer.Line),
		stopChan:     make(chan struct{}),
	}
}

func (m *Manager) Start() {
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
	close(m.stopChan)
	m.wg.Wait()
	for _, s := range m.schedulers {
		s.Stop()
	}
}

// 全量同步专线配置
func (m *Manager) fullSync() {
	m.mu.Lock()
	defer m.mu.Unlock()

	lines := m.configSyncer.GetLines()
	newRouterLines := make(map[string][]syncer.Line)

	// 按路由器分组专线
	for _, line := range lines {
		newRouterLines[line.Router.IP] = append(newRouterLines[line.Router.IP], line)
	}

	// 更新专线列表
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
	defer m.wg.Done()

	for {
		select {
		case <-m.stopChan:
			return
		case event := <-sub.Events():
			m.processLineEvent(event)
		}
	}
}

func (m *Manager) processLineEvent(event syncer.LineChangeEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()

	routerIP := event.Line.Router.IP

	switch event.Type {
	case syncer.LineCreate, syncer.LineUpdate:
		// 更新专线列表
		m.updateLineList(routerIP, event.Line)

		// 确保调度器存在并传递事件
		m.ensureScheduler(routerIP, m.routerLines[routerIP])
		m.schedulers[routerIP].OnLineChange(event)

	case syncer.LineDelete:
		// 更新专线列表
		if lines, exists := m.routerLines[routerIP]; exists {
			for i, line := range lines {
				if line.IP == event.Line.IP {
					m.routerLines[routerIP] = append(lines[:i], lines[i+1:]...)
					break
				}
			}
		}

		// 传递删除事件给调度器
		if s, exists := m.schedulers[routerIP]; exists {
			s.OnLineChange(event)

			// 延迟删除空调度器
			if len(m.routerLines[routerIP]) == 0 {
				time.AfterFunc(10*time.Minute, func() {
					m.mu.Lock()
					defer m.mu.Unlock()
					if len(m.routerLines[routerIP]) == 0 {
						m.schedulers[routerIP].Stop()
						delete(m.schedulers, routerIP)
						delete(m.routerLines, routerIP)
					}
				})
			}
		}
	}
}

// 更新专线列表
func (m *Manager) updateLineList(routerIP string, line syncer.Line) {
	lines := m.routerLines[routerIP]
	for i, l := range lines {
		if l.IP == line.IP {
			lines[i] = line // 更新现有专线
			return
		}
	}
	m.routerLines[routerIP] = append(lines, line) // 新增专线
}

// 确保调度器存在
func (m *Manager) ensureScheduler(routerIP string, lines []syncer.Line) {
	if _, exists := m.schedulers[routerIP]; !exists {
		m.schedulers[routerIP] = NewRouterScheduler(&lines[0].Router, lines)
		go m.schedulers[routerIP].Start()
	}
}

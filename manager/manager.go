package manager

import (
	"sync"

	"github.com/charlesren/zabbix_ddl_monitor/syncer"
	"github.com/charlesren/zabbix_ddl_monitor/connection"
	"github.com/charlesren/zabbix_ddl_monitor/scheduler"
)

type Manager struct {
	configSyncer *syncer.ConfigSyncer
	schedulers   map[string]*scheduler.RouterScheduler // key: routerIP
	routerCache  map[string]*connection.Router   // 路由器信息缓存
	mu           sync.Mutex
}

func NewManager(zabbixURL, username, password string) (*Manager, error) {
	syncer, err := syncer.NewConfigSyncer(zabbixURL, username, password)
	if err != nil {
		return nil, err
	}

	mgr := &Manager{
		configSyncer: syncer,
		schedulers:   make(map[string]*scheduler.RouterScheduler),
		routerCache:  make(map[string]*connection.Router),
	}

	// 订阅配置变更
	go mgr.watchConfigChanges()
	return mgr, nil
}

func (m *Manager) watchConfigChanges() {
	ch := m.configSyncer.Subscribe()
	for range ch {
		m.updateSchedulers()
	}
}

func (m *Manager) updateSchedulers() {
	lines := m.configSyncer.GetLines()
	m.mu.Lock()
	defer m.mu.Unlock()

	// 更新或创建调度器
	for _, line := range lines {
		if _, exists := m.schedulers[line.RouterIP]; !exists {
			routerInfo, err := connection.FetchRouterDetails(line.RouterIP)
			if err != nil {
				log.Printf("获取路由器信息失败: %s, err: %v", line.RouterIP, err)
				continue
			}
			m.routerCache[line.RouterIP] = routerInfo
			m.schedulers[line.RouterIP] = scheduler.NewRouterScheduler(routerInfo)
		}
		// 注意：这里假设scheduler.RouterScheduler有UpdateLine方法
		// 如果没有，需要相应调整代码
	}
}

func (m *Manager) hasLineForRouter(ip string, lines map[string]syncer.Line) bool {
	for _, line := range lines {
		if line.RouterIP == ip {
			return true
		}
	}
	return false
}

func (m *Manager) Start() {
	go m.configSyncer.Start()
}

func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.schedulers {
		s.Stop()
	}
}

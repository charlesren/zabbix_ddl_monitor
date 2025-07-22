package scheduler

import (
	"log"
	"sync"

	"github.com/yourusername/zabbix_ddl_monitor/internal/config"
	"github.com/yourusername/zabbix_ddl_monitor/internal/router"
)

type Manager struct {
	configSyncer *config.ConfigSyncer
	schedulers   map[string]*RouterScheduler // key: routerIP
	routerCache  map[string]*router.Router   // 路由器信息缓存
	mu           sync.Mutex
}

func NewManager(zabbixURL, username, password string) (*Manager, error) {
	syncer, err := config.NewConfigSyncer(zabbixURL, username, password)
	if err != nil {
		return nil, err
	}

	mgr := &Manager{
		configSyncer: syncer,
		schedulers:   make(map[string]*RouterScheduler),
		routerCache:  make(map[string]*router.Router),
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
			routerInfo, err := router.FetchRouterDetails(line.RouterIP)
			if err != nil {
				log.Printf("获取路由器信息失败: %s, err: %v", line.RouterIP, err)
				continue
			}
			m.routerCache[line.RouterIP] = routerInfo
			m.schedulers[line.RouterIP] = NewRouterScheduler(routerInfo)
		}
		m.schedulers[line.RouterIP].UpdateLine(line)
	}

	// 清理无效调度器
	for ip := range m.schedulers {
		if !m.hasLineForRouter(ip, lines) {
			m.schedulers[ip].Stop()
			delete(m.schedulers, ip)
			delete(m.routerCache, ip)
		}
	}
}

func (m *Manager) hasLineForRouter(ip string, lines map[string]config.Line) bool {
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

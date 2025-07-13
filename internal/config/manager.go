package config

import (
	"log"
	"sync"
	"time"

	"github.com/zabbix/zabbix-api-go"
)

// Line 专线配置
type Line struct {
	ID       string
	IP       string
	Interval time.Duration
	Router   Router
}

// Router 路由器连接信息
type Router struct {
	ID       string //预留，暂不使用
	IP       string //通过ID识别Router
	Username string
	Password string
	Platform string // 平台类型：cisco_iosxe/huawei_vrp 等
}

// ConfigManager 配置管理器
type ConfigManager struct {
	client       *zabbix.Client
	lines        map[string]Line
	routers      map[string]*Router //通过zabbix api 获取lines信息后，根据各个line附带的信息生成router，多个line可以用同一个router
	version      int64              // 配置版本号
	subscribers  []chan struct{}    // 配置变更订阅者
	mu           sync.Mutex         // 保护并发访问
	cacheEnabled bool               // 是否启用缓存
	lastFetch    time.Time          // 上次获取配置时间
}

// NewConfigManager 创建配置管理器
func NewConfigManager(zabbixURL, username, password string) (*ConfigManager, error) {
	client, err := zabbix.NewClient(zabbixURL, username, password)
	if err != nil {
		return nil, err
	}
	return &ConfigManager{
		client:      client,
		lines:       make(map[string]Line),
		routers:     make(map[string]*Router),
		subscribers: make([]chan struct{}, 0),
	}, nil
}

// Sync 启动配置同步
func (cm *ConfigManager) Sync() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := cm.fetchAndNotify(); err != nil {
				log.Printf("配置同步失败: %v (重试中...)", err)
				time.Sleep(30 * time.Second) // 指数退避可在此实现
				continue
			}
		}
	}
}

// Subscribe 订阅配置变更事件
func (cm *ConfigManager) Subscribe() <-chan struct{} {
	ch := make(chan struct{}, 1)
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.subscribers = append(cm.subscribers, ch)
	return ch
}

// fetchAndNotify 获取配置并通知变更
func (cm *ConfigManager) fetchAndNotify() error {
	newLines, err := cm.fetchFromZabbix()
	if err != nil {
		return err
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	if !cm.configChanged(newLines) {
		return nil // 无变更
	}

	newRouters := parseRoutersFromLines(newLines)
	cm.lines = newLines
	cm.routers = newRouters
	cm.version++
	cm.notifySubscribers()
	return nil
}

// parseRoutersFromLines  从Lines生成router
func parseRoutersFromLines(lines map[string]Line) map[string]*Router {
	routers := make(map[string]*Router)
	for _, line := range lines {
		if _, exists := routers[line.Router.IP]; !exists {
			routers[line.Router.IP] = &line.Router
		}
	}
	return routers
}

// fetchFromZabbix 从Zabbix获取专线和路由器配置
func (cm *ConfigManager) fetchFromZabbix() (map[string]Line, error) {
	// 伪代码：实际调用Zabbix API
	response, err := cm.client.DoRequest("ddl.get", map[string]interface{}{
		"output":      []string{"lineid", "ip", "interval", "routerid"},
		"selectHosts": []string{"hostid", "ip", "username", "password", "platform"}, // 扩展获取路由器信息
	})
	if err != nil {
		return nil, err
	}

	lines := make(map[string]Line)
	for _, item := range response.Result.([]interface{}) {
		data := item.(map[string]interface{})
		routerData := data["hosts"].([]interface{})[0].(map[string]interface{}) // 假设每个专线关联一个路由器

		lines[data["lineid"].(string)] = Line{
			ID:       data["lineid"].(string),
			IP:       data["ip"].(string),
			Interval: time.Duration(data["interval"].(float64)) * time.Second,
			Router: Router{
				IP:       routerData["ip"].(string),
				Username: routerData["username"].(string),
				Password: routerData["password"].(string),
				Platform: routerData["platform"].(string),
			},
		}
	}
	return lines, nil
}

// configChanged 检查配置是否变更
func (cm *ConfigManager) configChanged(newLines map[string]Line) bool {
	if len(cm.lines) != len(newLines) {
		return true
	}
	for id, line := range newLines {
		if oldLine, exists := cm.lines[id]; !exists || oldLine != line {
			return true
		}
	}
	return false
}

// notifySubscribers 通知所有订阅者
func (cm *ConfigManager) notifySubscribers() {
	for _, sub := range cm.subscribers {
		select {
		case sub <- struct{}{}:
		default: // 避免阻塞
		}
	}
}

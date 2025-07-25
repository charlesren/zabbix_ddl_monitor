package syncer

import (
	"log"
	"sync"
	"time"

	"github.com/charlesren/zapix"
)

// ConfigSyncer 配置同步器 (原ConfigManager重构)
type ConfigSyncer struct {
	client      *zapix.Client
	lines       map[string]Line
	version     int64
	subscribers []chan struct{} // 改用回调函数更灵活
	mu          sync.Mutex
}

// NewConfigSyncer 创建配置同步器
func NewConfigSyncer(zabbixURL, username, password string) (*ConfigSyncer, error) {
	client, err := zapix.NewClient(zabbixURL, username, password)
	if err != nil {
		return nil, err
	}
	return &ConfigSyncer{
		client: client,
		lines:  make(map[string]Line),
	}, nil
}

// Start 启动配置同步循环
func (cs *ConfigSyncer) Start() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := cs.sync(); err != nil {
				log.Printf("sync failed: %v (retrying...)", err)
				time.Sleep(30 * time.Second)
			}
		}
	}
}

// Subscribe 订阅配置变更 (返回取消函数)
func (cs *ConfigSyncer) Subscribe() (ch <-chan struct{}, cancel func()) {
	eventCh := make(chan struct{}, 1)
	cs.mu.Lock()
	cs.subscribers = append(cs.subscribers, eventCh)
	cs.mu.Unlock()

	return eventCh, func() {
		cs.mu.Lock()
		defer cs.mu.Unlock()
		for i, sub := range cs.subscribers {
			if sub == eventCh {
				cs.subscribers = append(cs.subscribers[:i], cs.subscribers[i+1:]...)
				close(eventCh)
				break
			}
		}
	}
}

// sync 执行同步逻辑
func (cs *ConfigSyncer) sync() error {
	newLines, err := cs.fetchFromZabbix()
	if err != nil {
		return err
	}

	cs.mu.Lock()
	defer cs.mu.Unlock()

	if !cs.isChanged(newLines) {
		return nil
	}

	cs.lines = newLines
	cs.version++
	cs.notify()
	return nil
}

// fetchLines 从Zabbix获取数据
func (cs *ConfigSyncer) fetchLines() (map[string]Line, error) {
	response, err := cs.client.DoRequest("ddl.get", map[string]interface{}{
		"output":      []string{"lineid", "ip", "interval", "routerid"},
		"selectHosts": []string{"hostid", "ip", "username", "password", "platform"},
	})
	if err != nil {
		return nil, err
	}

	lines := make(map[string]Line)
	for _, item := range response.Result.([]interface{}) {
		data := item.(map[string]interface{})
		host := data["hosts"].([]interface{})[0].(map[string]interface{})

		lines[data["lineid"].(string)] = Line{
			ID:       data["lineid"].(string),
			IP:       data["ip"].(string),
			Interval: time.Duration(data["interval"].(float64)) * time.Second,
			Router: Router{
				IP:       host["ip"].(string),
				Username: host["username"].(string),
				Password: host["password"].(string),
				Platform: host["platform"].(string),
			},
		}
	}
	return lines, nil
}

// parseRouters 生成路由器映射表
func (cs *ConfigSyncer) parseRouters(lines map[string]Line) map[string]*Router {
	routers := make(map[string]*Router)
	for _, line := range lines {
		if _, exists := routers[line.Router.IP]; !exists {
			routers[line.Router.IP] = &line.Router
		}
	}
	return routers
}

// isChanged 检查配置变更
func (cs *ConfigSyncer) isChanged(newLines map[string]Line) bool {
	if len(cs.lines) != len(newLines) {
		return true
	}
	for id, newLine := range newLines {
		if oldLine, ok := cs.lines[id]; !ok || oldLine != newLine {
			return true
		}
	}
	return false
}

// notify 通知订阅者
func (cs *ConfigSyncer) notify() {
	for _, sub := range cs.subscribers {
		select {
		case sub <- struct{}{}:
		default:
			log.Println("dropped config change event due to full channel")
		}
	}
}

// GetLines 获取当前专线配置 (线程安全)
func (cs *ConfigSyncer) GetLines() map[string]Line {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.lines
}

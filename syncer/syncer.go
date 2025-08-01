package syncer

import (
	"log"
	"reflect"
	"time"

	"github.com/charlesren/zapix"
)

func (cs *ConfigSyncer) notifyAll(events []LineChangeEvent) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	for _, event := range events {
		for _, sub := range cs.subscribers {
			select {
			case sub <- event:
			default:
				log.Printf("warn: subscriber channel full, dropped event %v", event)
			}
		}
	}
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
	newLines, err := cs.fetchLines()
	if err != nil {
		return err
	}

	cs.mu.Lock()
	defer cs.mu.Unlock()

	events := cs.detectChanges(newLines)
	if len(events) == 0 {
		return nil
	}

	cs.lines = newLines
	cs.version++

	// 为所有事件设置版本号
	for i := range events {
		events[i].Version = cs.version
	}

	go cs.notifyAll(events) // 异步通知
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

// 获取当前配置快照
func (cs *ConfigSyncer) Snapshot() map[string]Line {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	snapshot := make(map[string]Line, len(cs.lines))
	for k, v := range cs.lines {
		snapshot[k] = v
	}
	return snapshot
}

// 获取当前版本号
func (cs *ConfigSyncer) Version() int64 {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.version
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
	//     oldHash := hashLines(cs.lines)
	//    newHash := hashLines(newLines)
	//   return oldHash != newHash

	return false
}

// GetLines 获取当前专线配置 (线程安全)
func (cs *ConfigSyncer) GetLines() map[string]Line {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.lines
}
func (cs *ConfigSyncer) detectChanges(newLines map[string]Line) []LineChangeEvent {
	var events []LineChangeEvent

	// 检测删除和更新
	for oldID, oldLine := range cs.lines {
		if newLine, exists := newLines[oldID]; !exists {
			events = append(events, LineChangeEvent{
				Type: LineDelete,
				Line: oldLine,
			})
		} else if !reflect.DeepEqual(oldLine, newLine) {
			events = append(events, LineChangeEvent{
				Type: LineUpdate,
				Line: newLine,
			})
		}
	}

	// 检测新增
	for newID, newLine := range newLines {
		if _, exists := cs.lines[newID]; !exists {
			events = append(events, LineChangeEvent{
				Type: LineCreate,
				Line: newLine,
			})
		}
	}

	return events
}
func (cs *ConfigSyncer) Subscribe() (<-chan LineChangeEvent, func()) {
	ch := make(chan LineChangeEvent, 100)
	cs.mu.Lock()
	cs.subscribers = append(cs.subscribers, Subscriber(ch))
	cs.mu.Unlock()

	return ch, func() {
		cs.unsubscribe(ch)
	}
}

func (cs *ConfigSyncer) unsubscribe(ch <-chan LineChangeEvent) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	// ...取消订阅逻辑...
}

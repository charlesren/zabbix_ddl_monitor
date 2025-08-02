package syncer

import (
	"context"
	"log"
	"time"

	"github.com/charlesren/zapix"
)

func NewConfigSyncer(zc *zapix.ZabbixClient, interval time.Duration) (*ConfigSyncer, error) {
	ctx, cancel := context.WithCancel(context.Background())
	return &ConfigSyncer{
		client:       zc,
		lines:        make(map[string]Line),
		syncInterval: interval,
		ctx:          ctx,
		cancel:       cancel,
	}, nil
}
func (cs *ConfigSyncer) Start() {
	if cs.stopped {
		log.Println("warning: cannot start already stopped syncer")
		return
	}

	ticker := time.NewTicker(cs.syncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-cs.ctx.Done():
			log.Println("syncer stopped by context")
			return
		case <-ticker.C:
			cs.lastSyncTime = time.Now()
			if err := cs.checkHealth(); err != nil {
				log.Printf("health check failed: %v", err)
				continue
			}
			if err := cs.sync(); err != nil {
				log.Printf("sync failed: %v (retrying...)", err)
				time.Sleep(30 * time.Second)
			}
		}
	}
}

func (cs *ConfigSyncer) Stop() {
	cs.stopOnce.Do(func() {
		cs.cancel()

		cs.mu.Lock()
		defer cs.mu.Unlock()

		// 仅关闭未被取消的通道
		for _, sub := range cs.subscribers {
			select {
			case <-sub:
				// 通道已关闭（被unsubscribe关闭）
			default:
				close(sub)
			}
		}
		cs.subscribers = nil
		cs.stopped = true
	})
}

// sync 执行同步逻辑
func (cs *ConfigSyncer) sync() error {
	newLines, err := cs.fetchLines()
	if err != nil {
		return err
	}

	events := cs.detectChanges(newLines)
	if len(events) == 0 {
		return nil
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()

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

// 获取当前版本号
func (cs *ConfigSyncer) Version() int64 {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.version
}

// GetLines 获取当前专线配置 (线程安全)
func (cs *ConfigSyncer) GetLines() map[string]Line {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.lines
}

func (cs *ConfigSyncer) detectChanges(newLines map[string]Line) []LineChangeEvent {
	events := make([]LineChangeEvent, 0, len(cs.lines)+len(newLines))

	cs.mu.RLock()
	defer cs.mu.RUnlock()

	for oldID, oldLine := range cs.lines {
		if newLine, exists := newLines[oldID]; !exists {
			events = append(events, LineChangeEvent{Type: LineDelete, Line: oldLine})
		} else if oldLine.Hash != newLine.Hash {
			events = append(events, LineChangeEvent{Type: LineUpdate, Line: newLine})
		}
	}

	for newID, newLine := range newLines {
		if _, exists := cs.lines[newID]; !exists {
			events = append(events, LineChangeEvent{Type: LineCreate, Line: newLine})
		}
	}
	return events
}

func (cs *ConfigSyncer) Subscribe() (<-chan LineChangeEvent, func()) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	// 已停止时返回关闭的通道
	if cs.stopped {
		ch := make(chan LineChangeEvent)
		close(ch)
		return ch, func() {} // 空取消函数
	}

	ch := make(chan LineChangeEvent, 100)
	cs.subscribers = append(cs.subscribers, Subscriber(ch))

	return ch, func() {
		cs.Unsubscribe(ch)
	}
}

func (cs *ConfigSyncer) Unsubscribe(ch <-chan LineChangeEvent) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	// 查找并移除订阅者
	for i, sub := range cs.subscribers {
		if sub == Subscriber(ch) {
			// 从切片中移除
			cs.subscribers = append(cs.subscribers[:i], cs.subscribers[i+1:]...)
			// 关闭通道
			close(sub)
			return
		}
	}
}

// ConfigSyncer连接健康检查
func (cs *ConfigSyncer) checkHealth() error {
	//todo
	//_, err := cs.client.DoRequest("apiinfo.version", nil)
	//return err
	return nil
}

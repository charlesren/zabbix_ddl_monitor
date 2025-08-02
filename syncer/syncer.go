package syncer

import (
	"context"
	"fmt"
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
// 1.通过给定的proxy ip 查询proxy id
// 2.通过proxy id 和给定的tag，筛选出专线
func (cs *ConfigSyncer) fetchLines() (map[string]Line, error) {
	ProxyIP := "1.1.1.1" // TODO: 替换为配置或参数传入的实际 proxy host
	proxies, err := cs.client.GetProxyFormHost(ProxyIP)
	if err != nil {
		ylog.Errorf("syncer", "failed to fetch proxy info: %v", err)
	}
	if len(proxies) = 0 {
		ylog.Infof("syncer", "proxy %v not found ", ProxyIP)
		continue
	}
	proxyid = proxies[0].ProxyHostid

	params := zapix.HostGetParams{
		SelectTags:          zapix.SelectQuery("extend"),
		SelectInheritedTags: zapix.SelectQuery("extend"),
		ProxyIDs:            []int{proxyid},
		Tags: []zapix.HostTagObject{
			{
				Tag:   SelectTag,
				Value: SelectValue,
			},
		},
	}

	hosts, err := cs.client.HostGet(params)
	if err != nil {
		ylog.Errorf("syncer", "failed to fetch proxy info: %v", err)
	}

	// 解析专线信息
	lines := make(map[string]Line)
	for _, host := range hosts {
		line := Line{
			ID:       host.HostID,
			IP:       "",               // 需要从 host.Interfaces 获取 IP
			Interval: 30 * time.Second, // 默认间隔，可根据需求调整
			Router: Router{
				IP:       "", // 需要从 host.Interfaces 获取
				Username: "", // 需要从其他字段或配置获取
				Password: "", // 需要从其他字段或配置获取
				Platform: "", // 需要从 host.Tags 或其他字段获取
			},
		}
		line.ComputeHash()
		lines[line.ID] = line
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

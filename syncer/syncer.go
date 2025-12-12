package syncer

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/charlesren/ylog"
	"github.com/charlesren/zabbix_ddl_monitor/connection"
	"github.com/charlesren/zapix"
)

func NewConfigSyncer(zc *zapix.ZabbixClient, interval time.Duration, proxyName string) (*ConfigSyncer, error) {
	ctx, cancel := context.WithCancel(context.Background())
	return &ConfigSyncer{
		client:       zc,
		lines:        make(map[string]Line),
		syncInterval: interval,
		ctx:          ctx,
		cancel:       cancel,
		proxyName:    proxyName,
	}, nil
}
func (cs *ConfigSyncer) Start() {
	if cs.stopped {
		ylog.Warnf("syncer", "cannot start already stopped syncer")
		return
	}
	ylog.Infof("syncer", "starting syncer...")
	//获取并设置代理ID,后续不需重复获取
	cs.handleProxyId()
	//启动时先同步一次
	cs.sync()
	ticker := time.NewTicker(cs.syncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-cs.ctx.Done():
			ylog.Infof("syncer", "syncer stopped by context")
			return
		case <-ticker.C:
			cs.lastSyncTime = time.Now()
			if err := cs.checkHealth(); err != nil {
				ylog.Errorf("syncer", "health check failed: %v", err)
				continue
			}
			if err := cs.sync(); err != nil {
				ylog.Errorf("syncer", "sync failed: %v (retrying...)", err)
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
		for _, sub := range cs.subscribers {
			close(sub)
		}
		cs.subscribers = nil
		cs.stopped = true
	})
}

// sync 执行同步逻辑
func (cs *ConfigSyncer) sync() error {
	ylog.Infof("syncer", "starting sync (proxy: %s)", cs.proxyName)
	newLines, err := cs.fetchLines()
	if err != nil {
		ylog.Errorf("syncer", "sync failed: %v", err)
		return err
	}

	events := cs.detectChanges(newLines)

	// 如果是第一次同步（cs.lines为空），过滤掉创建事件
	// 因为第一次同步是初始化，不应该被视为"变更"
	cs.mu.RLock()
	isInitialSync := len(cs.lines) == 0
	cs.mu.RUnlock()

	if isInitialSync {
		filteredEvents := make([]LineChangeEvent, 0, len(events))
		for _, event := range events {
			if event.Type != LineCreate {
				filteredEvents = append(filteredEvents, event)
			} else {
				ylog.Debugf("syncer", "过滤初始同步的创建事件: line_ip=%s, router=%s",
					event.Line.IP, event.Line.Router.IP)
			}
		}
		events = filteredEvents
		ylog.Infof("syncer", "初始同步完成，过滤了创建事件，剩余事件数: %d", len(events))
	}

	if len(events) == 0 {
		ylog.Debugf("syncer", "no config changes detected")
		return nil
	}

	ylog.Infof("syncer", "detected changes: created=%d updated=%d deleted=%d",
		countEvents(events, LineCreate),
		countEvents(events, LineUpdate),
		countEvents(events, LineDelete))
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
func countEvents(events []LineChangeEvent, typ ChangeType) int {
	count := 0
	for _, e := range events {
		if e.Type == typ {
			count++
		}
	}
	return count
}
func (cs *ConfigSyncer) handleProxyId() error {
	proxies, err := cs.client.GetProxyFormHost(cs.proxyName)
	if err != nil {
		ylog.Errorf("syncer", "proxy query failed: %v", err)
		return fmt.Errorf("proxy query failed: %v", err)
	}
	if len(proxies) == 0 {
		ylog.Warnf("syncer", "proxy not found: %s", cs.proxyName)
		return fmt.Errorf("proxy not found")
	}
	proxyID, err := strconv.Atoi(proxies[0].Proxyid)
	if err != nil {
		ylog.Errorf("syncer", "invalid proxyID format: %v", err)
		return fmt.Errorf("invalid proxyID")
	}
	cs.proxyID = proxyID
	ylog.Debugf("syncer", "found proxy ID: %v", proxyID)
	return nil
}

// fetchLines 从Zabbix获取绑定到特定proxy的专线数据(采集间隔，路由器信息...)
// 这些数据通过宏定义在多个链接的模板上或主机上，也可能是全局宏
/*
# 专线主机上必需的宏
{$LINE_ID}: "unique-line-identifier"      # 唯一专线标识符
{$LINE_CHECK_INTERVAL}: "180"             # 检查间隔（秒）
{$LINE_ROUTER_IP}: "192.168.1.1"         # 路由器 IP
{$LINE_ROUTER_USERNAME}: "admin"          # 路由器用户名
{$LINE_ROUTER_PASSWORD}: "password"       # 路由器密码
{$LINE_ROUTER_PLATFORM}: "cisco_iosxe"   # 路由器平台
{$LINE_ROUTER_PROTOCOL}: "scrapli"       # 协议类型
一、目标
一次性找出「绑定到指定 Proxy 且带特定 Tag 的所有启用主机」的完整宏集合（主机级 + 任意层级模板级 + 全局级），并将 API 调用压到最低。

二、 宏优先级与深度定义（便于理解）
  - 主机自身：深度 0（最近）
  - 直接模板：深度 1
  - 再上一级模板：深度 2
  - … 依次递增

写入宏时按 深度从大到小倒序 处理，后写入覆盖先写入，即可精确匹配 Zabbix 官方优先级。

三、具体步骤如下
 0.检查proxyID是否为0，如果为0则调用handleProxyId方法获取proxyID
 1.查询用到的全局宏,所用主机共用.  → 缓存到 Map_G。
   usermacro.get
   params: {"globalmacro": true, "output": ["macro","value"]}
 2.通过proxy id 和给定的tag，筛选出专线的主机宏，另外缓存一级模板id等信息
   host.get
   params:
    proxyids: "<proxy-id>"
    filter: {"status": "0"}                # 仅启用
    tags: [{"tag":"<key>","value":"<value>","operator":"0"}]
    inheritedTags: true                    # 包含模板继承标签
    selectTags: "extend"
    selectInheritedTags: "extend"
    selectMacros: "extend"                 # 主机自身宏
    selectParentTemplates: "extend"        # 一级模板信息
   	//参数说明：
	// 1.通过SelectTag获取主机的tag
	// 2.通过inheritedtags:true包含主机继承的tag
	// 3.通过SelectMacros获取主机的宏信息
	// 4.通过proxyid 筛选出绑定到特定proxy主机
	// 5.通过status:0 filter 筛选出启用的主机
	// 6.通过tags筛选出含有专线特征的主机
	// 7.通过selectParentTemplates: "extend" 筛选出直接链接的模板信息



 3.获取模板宏处理
 a) 本地递归
 对每台主机，从直接模板开始，向上爬直到无父模板既len(t.ParentTemplates) == 0，得到有序链，记录 模板 ID → 深度（深度从 1 开始递增）。
 得到每个模板的宏和去重后的「模板 ID + 深度」列表。
  template.get
  paras:
  TemplateIDs: []string{templateID},
  SelectMacros: zapix.SelectExtendedOutput,
  SelectParentTemplates: zapix.SelectExtendedOutput,  //必须，len(t.ParentTemplates) == 0 表述终结
 b) 宏要按模板保存sharedTemplatesMacros，所有host共享。拉宏前，先去这里找，有则直接使用，无再拉取。//暂不实施。
4.本地合并与优先级处理
 创建 Map_R（macro → 最终值）。
 写入顺序（由低到高）：
 ① Map_G 全局宏
 ② 模板宏：按 深度降序（最大深度 → 1）写入
 ③ 主机宏（深度 0，最后写入）
 后写入自动覆盖，Map_R 中即为该主机最终生效的宏。
*/
type TemplateNode struct {
	ID        string
	Depth     int
	Macros    map[string]string
	ParentIDs []string
}

type HostTemplateData struct {
	Nodes      []TemplateNode
	HostMacros map[string]string
}

func (cs *ConfigSyncer) buildTemplateChain(host zapix.HostObject) ([]TemplateNode, error) {
	var chain []TemplateNode
	processed := make(map[string]bool)
	var mu sync.Mutex // 保证并发安全

	// 递归构建函数
	var build func(templateID string, depth int) error
	build = func(templateID string, depth int) error {
		// 检查循环依赖
		if processed[templateID] {
			ylog.Warnf("syncer", "circular dependency detected at template %s", templateID)
			return nil
		}
		processed[templateID] = true

		// 获取模板完整数据
		templates, err := cs.client.TemplateGet(zapix.TemplateGetParams{
			TemplateIDs:           []string{templateID},
			SelectMacros:          zapix.SelectExtendedOutput,
			SelectParentTemplates: zapix.SelectExtendedOutput,
		})
		if err != nil {
			return fmt.Errorf("failed to get template %s: %v", templateID, err)
		}
		if len(templates) == 0 {
			return fmt.Errorf("template %s not found", templateID)
		}
		t := templates[0]

		// 记录模板节点
		node := TemplateNode{
			ID:        t.TemplateID,
			Depth:     depth,
			Macros:    make(map[string]string),
			ParentIDs: make([]string, 0, len(t.ParentTemplates)),
		}

		// 提取宏
		for _, m := range t.Macros {
			node.Macros[m.Macro] = m.Value
		}

		// 记录父模板ID
		for _, p := range t.ParentTemplates {
			node.ParentIDs = append(node.ParentIDs, p.TemplateID)
		}

		// 线程安全写入
		mu.Lock()
		chain = append(chain, node)
		mu.Unlock()

		// 递归处理父模板
		for _, pid := range node.ParentIDs {
			if err := build(pid, depth+1); err != nil {
				return err
			}
		}
		return nil
	}

	// 从主机的直接模板开始构建
	for _, pt := range host.ParentTemplates {
		if err := build(pt.TemplateID, 1); err != nil {
			return nil, err
		}
	}

	return chain, nil
}
func (cs *ConfigSyncer) mergeMacros(
	globalMacros []zapix.UsermacroObject, // 新增全局宏参数
	chain []TemplateNode,
	hostMacros []zapix.UsermacroObject,
) map[string]string {
	finalMacros := make(map[string]string)

	// ① 写入全局宏（最低优先级）
	for _, m := range globalMacros {
		finalMacros[m.Macro] = m.Value
	}
	ylog.Debugf("syncer", "merged %d global macros", len(globalMacros))

	// ② 按深度降序写入模板宏
	sort.Slice(chain, func(i, j int) bool {
		return chain[i].Depth > chain[j].Depth // 深度大的优先
	})
	for _, node := range chain {
		for macro, value := range node.Macros {
			finalMacros[macro] = value // 覆盖全局宏
		}
	}
	ylog.Debugf("syncer", "merged %d template macros (max depth: %d)",
		len(chain), chain[0].Depth)

	// ③ 写入主机宏（最高优先级）
	for _, m := range hostMacros {
		finalMacros[m.Macro] = m.Value // 覆盖模板宏
	}
	ylog.Debugf("syncer", "merged %d host macros", len(hostMacros))

	return finalMacros
}

func (cs *ConfigSyncer) fetchLines() (map[string]Line, error) {
	ylog.Debugf("syncer", "fetching lines from proxy: %s", cs.proxyName)
	if cs.proxyID == 0 {
		if err := cs.handleProxyId(); err != nil {
			return nil, err
		}
	}

	// 1. 获取全局宏
	globalMacros, err := cs.client.UsermacroGet(zapix.UsermacroGetParams{
		Globalmacro: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch global macros: %v", err)
	}
	ylog.Debugf("syncer", "fetched %d global macros", len(globalMacros))
	// 2. 获取主机列表
	params := zapix.HostGetParams{
		SelectTags:            zapix.SelectQuery("extend"),
		SelectInheritedTags:   zapix.SelectQuery("extend"),
		SelectMacros:          zapix.SelectQuery("extend"),
		SelectParentTemplates: zapix.SelectExtendedOutput,
		ProxyIDs:              []int{cs.proxyID},
		InheritedTags:         true,
		Tags: []zapix.HostTagObject{
			{
				Tag:   LineSelectTag,
				Value: LineSelectValue,
			},
		},
		GetParameters: zapix.GetParameters{
			Filter: map[string]interface{}{
				"status": "0",
			},
		},
	}

	hosts, err := cs.client.HostGet(params)
	if err != nil {
		ylog.Errorf("syncer", "failed to fetch proxy info: %v", err)
		return nil, err
	}
	ylog.Debugf("syncer", "fetched %d hosts", len(hosts))
	// 3.处理每个主机
	lines := make(map[string]Line)
	for _, host := range hosts {
		// 3.1 构建模板链
		chain, err := cs.buildTemplateChain(host)
		if err != nil {
			ylog.Errorf("syncer", "host %s: template chain build failed: %v", host.Host, err)
			continue
		}

		// 3.2 合并宏（全局+模板+主机）
		finalMacros := cs.mergeMacros(globalMacros, chain, host.Macros)
		ylog.Debugf("syncer", "host %s: merged %d macros (g:%d,t:%d,h:%d)",
			host.Host,
			len(finalMacros),
			len(globalMacros),
			len(chain),
			len(host.Macros),
		)

		// 3.3 构造Line对象
		line := Line{
			ID:       finalMacros["{$LINE_ID}"],
			IP:       host.Host,
			Interval: parseDurationFromMacro(finalMacros["{$LINE_CHECK_INTERVAL}"], DefaultInterval),
			Router: Router{
				IP:       finalMacros["{$LINE_ROUTER_IP}"],
				Username: finalMacros["{$LINE_ROUTER_USERNAME}"],
				Password: finalMacros["{$LINE_ROUTER_PASSWORD}"],
				Platform: connection.Platform(finalMacros["{$LINE_ROUTER_PLATFORM}"]),
				Protocol: connection.Protocol(finalMacros["{$LINE_ROUTER_PROTOCOL}"]),
			},
		}
		line.ComputeHash()
		lines[line.IP] = line
		ylog.Debugf("syncer", "get lines ip: %v, associated router: %v", line.IP, line.Router.IP)
	}
	ylog.Infof("syncer", "processed %d/%d valid lines", len(lines), len(hosts))
	return lines, nil
}

// 辅助函数：从macro解析duration
func parseDurationFromMacro(value string, defaultVal time.Duration) time.Duration {
	if value == "" {
		return defaultVal
	}
	sec, err := strconv.ParseInt(value, 10, 64)
	if err != nil || sec < 0 {
		return defaultVal
	}
	return time.Duration(sec) * time.Second
}

func (cs *ConfigSyncer) notifyAll(events []LineChangeEvent) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	ylog.Infof("syncer", "notifying %d subscribers with %d events", len(cs.subscribers), len(events))

	for _, event := range events {
		for _, sub := range cs.subscribers {
			// Try to send with a short timeout to avoid blocking forever
			select {
			case sub <- event:
				ylog.Debugf("syncer", "event sent to subscriber: %+v", event)
			default:
				// Channel is full, try once more with a timeout
				ylog.Warnf("syncer", "subscriber channel full, retrying with timeout for event %v", event)
				select {
				case sub <- event:
					ylog.Debugf("syncer", "event sent to subscriber after retry: %+v", event)
				case <-time.After(100 * time.Millisecond):
					ylog.Errorf("syncer", "ERROR: subscriber channel still full after timeout, dropped event %v", event)
				}
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
			ylog.Infof("syncer", "line deleted: %s (router: %s)", oldLine.ID, oldLine.Router.IP)
			events = append(events, LineChangeEvent{Type: LineDelete, Line: oldLine})
		} else if oldLine.Hash != newLine.Hash {
			ylog.Infof("syncer", "line updated: %s (router: %s)", newLine.ID, newLine.Router.IP)
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

func (cs *ConfigSyncer) Subscribe(ctx context.Context) *Subscription {
	ch := make(chan LineChangeEvent, 1000)
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.subscribers = append(cs.subscribers, ch)

	subCtx, cancel := context.WithCancel(ctx)
	sub := &Subscription{
		events: ch,
		cs:     cs,
		cancel: cancel,
	}

	if ctx != context.Background() {
		// 仅当ctx非默认时启动监听
		go func() {
			<-subCtx.Done()
			sub.Close()
		}()
	}

	return sub
}

// Events 返回只读通道供用户使用
func (s *Subscription) Events() <-chan LineChangeEvent {
	return s.events
}

// Close 取消订阅并释放资源（幂等）
func (s *Subscription) Close() {
	s.once.Do(func() {
		s.cs.mu.Lock()
		defer s.cs.mu.Unlock()

		// 从订阅者列表中移除
		for i, sub := range s.cs.subscribers {
			if sub == s.events {
				s.cs.subscribers = append(s.cs.subscribers[:i], s.cs.subscribers[i+1:]...)
				close(sub)
				break
			}
		}
		s.cancel() // 取消关联的context
	})
}

// ConfigSyncer连接健康检查
func (cs *ConfigSyncer) checkHealth() error {
	//todo
	//_, err := cs.client.DoRequest("apiinfo.version", nil)
	//return err
	return nil
}

package syncer

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/charlesren/ylog"
	"github.com/charlesren/zabbix_ddl_monitor/connection"
	"github.com/charlesren/zapix"
)

func NewConfigSyncer(zc *zapix.ZabbixClient, interval time.Duration, proxyIP string) (*ConfigSyncer, error) {
	ctx, cancel := context.WithCancel(context.Background())
	return &ConfigSyncer{
		client:       zc,
		lines:        make(map[string]Line),
		syncInterval: interval,
		ctx:          ctx,
		cancel:       cancel,
		proxyIP:      proxyIP,
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
		for _, sub := range cs.subscribers {
			close(sub)
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
	proxies, err := cs.client.GetProxyFormHost(cs.proxyIP)
	if err != nil {
		ylog.Errorf("syncer", "failed to fetch proxy info: %v", err)
	}
	if len(proxies) == 0 {
		ylog.Infof("syncer", "proxy %v not found ", ProxyIP)
		return nil, fmt.Errorf("proxy not found")
	}
	proxyID, err := strconv.Atoi(proxies[0].Proxyid)
	if err != nil {
		ylog.Errorf("syncer", "invalid proxyID format: %v", err)
		return nil, fmt.Errorf("invalid proxyID")
	}
	//GetProxyFormHost 示例输出
	/*
			 {
		    "jsonrpc": "2.0",
		    "result": [
		        {
		            "proxy_hostid": "0",
		            "host": "10.10.10.10",
		            "status": "5",
		            "disable_until": "0",
		            "error": "",
		            "available": "0",
		            "errors_from": "0",
		            "lastaccess": "1754196272",
		            "ipmi_authtype": "-1",
		            "ipmi_privilege": "2",
		            "ipmi_username": "",
		            "ipmi_password": "",
		            "ipmi_disable_until": "0",
		            "ipmi_available": "0",
		            "snmp_disable_until": "0",
		            "snmp_available": "0",
		            "maintenanceid": "0",
		            "maintenance_status": "0",
		            "maintenance_type": "0",
		            "maintenance_from": "0",
		            "ipmi_errors_from": "0",
		            "snmp_errors_from": "0",
		            "ipmi_error": "",
		            "snmp_error": "",
		            "jmx_disable_until": "0",
		            "jmx_available": "0",
		            "jmx_errors_from": "0",
		            "jmx_error": "",
		            "name": "",
		            "flags": "0",
		            "templateid": "0",
		            "description": "",
		            "tls_connect": "1",
		            "tls_accept": "1",
		            "tls_issuer": "",
		            "tls_subject": "",
		            "tls_psk_identity": "",
		            "tls_psk": "",
		            "proxy_address": "10.10.10.10",
		            "auto_compress": "1",
		            "discover": "0",
		            "proxyid": "10452"
		        }
		    ],
		    "id": 1
		}
	*/

	params := zapix.HostGetParams{
		SelectTags:          zapix.SelectQuery("extend"),
		SelectInheritedTags: zapix.SelectQuery("extend"),
		SelectMacros:        zapix.SelectQuery("extend"),
		ProxyIDs:            []int{proxyID},
		Tags: []zapix.HostTagObject{
			{
				Tag:   LineSelectTag,
				Value: LineSelectValue,
			},
		},
	}

	// HostGet示例输出
	/*
			{
		  "jsonrpc": "2.0",
		  "result": [
		    {
		      "hostid": "10331",
		      "proxy_hostid": "10326",
		      "host": "10.10.10.11",
		      "status": "0",
		      "disable_until": "0",
		      "error": "",
		      "available": "1",
		      "errors_from": "0",
		      "lastaccess": "0",
		      "ipmi_authtype": "-1",
		      "ipmi_privilege": "2",
		      "ipmi_username": "",
		      "ipmi_password": "",
		      "ipmi_disable_until": "0",
		      "ipmi_available": "0",
		      "snmp_disable_until": "0",
		      "snmp_available": "2",
		      "maintenanceid": "0",
		      "maintenance_status": "0",
		      "maintenance_type": "0",
		      "maintenance_from": "0",
		      "ipmi_errors_from": "0",
		      "snmp_errors_from": "0",
		      "ipmi_error": "",
		      "snmp_error": "Timeout while connecting to \"10.10.10.11:20161\".",
		      "jmx_disable_until": "0",
		      "jmx_available": "0",
		      "jmx_errors_from": "0",
		      "jmx_error": "",
		      "name": "xxxx平台",
		      "flags": "0",
		      "templateid": "0",
		      "description": "",
		      "tls_connect": "1",
		      "tls_accept": "1",
		      "tls_issuer": "",
		      "tls_subject": "",
		      "tls_psk_identity": "",
		      "tls_psk": "",
		      "proxy_address": "",
		      "auto_compress": "1",
		      "discover": "0",
		      "inventory_mode": "1",
		      "macros": [
		        {
		          "hostmacroid": "2362",
		          "hostid": "10331",
		          "macro": "{$TCP_ESTAB_MAX}",
		          "value": "6000",
		          "description": "",
		          "type": "0"
		        },
		        {
		          "hostmacroid": "2579",
		          "hostid": "10331",
		          "macro": "{$IF_BANDWIDTH_WARN}",
		          "value": "90",
		          "description": "",
		          "type": "0"
		        },
		        {
		          "hostmacroid": "2635",
		          "hostid": "10331",
		          "macro": "{$CPU_UTIL_WARN}",
		          "value": "85",
		          "description": "",
		          "type": "0"
		        },
		        {
		          "hostmacroid": "5571",
		          "hostid": "10331",
		          "macro": "{$APP_PROCESSES}",
		          "value": "ReceiveAgent#3|desAgent#1",
		          "description": "应用进程关键字及数量，以#分隔；多个应用以|分隔，如zabbix_agentd#7|zabbix_serverd#1",
		          "type": "0"
		        },
		        {
		          "hostmacroid": "5572",
		          "hostid": "10331",
		          "macro": "{$APP_PORT_NUMBERS}",
		          "value": "7075|7076|8000|8382",
		          "description": "应用端口号，多个端口号以|分隔，如8080|8443",
		          "type": "0"
		        }
		      ],
		      "tags": [
		        {
		          "tag": "OS_TCP",
		          "value": "TCP_6000"
		        },
		        {
		          "tag": "OS_NET",
		          "value": "OS_if_99"
		        },
		        {
		          "tag": "OS_CPU",
		          "value": "CPU_85"
		        }
		      ],
		      "inheritedTags": [
		        {
		          "tag": "TempType",
		          "value": "MID"
		        },
		        {
		          "tag": "MidType",
		          "value": "WEBLOGIC"
		        },
		        {
		          "tag": "TempType",
		          "value": "SVR"
		        },
		        {
		          "tag": "SvrType",
		          "value": "LINUX"
		        },
		        {
		          "tag": "TempType",
		          "value": "APP"
		        }
		      ]
		    }
		  ],
		  "id": 1
		}
	*/
	hosts, err := cs.client.HostGet(params)
	if err != nil {
		ylog.Errorf("syncer", "failed to fetch proxy info: %v", err)
		return nil, err
	}
	// 解析专线信息
	lines := make(map[string]Line)
	for _, host := range hosts {
		// 从macros中提取关键信息
		macros := make(map[string]string)
		for _, macro := range host.Macros {
			macros[macro.Macro] = macro.Value
		}

		// 构造Line对象
		line := Line{
			ID:       macros["{$LINE_ID}"],
			IP:       host.Host,
			Interval: parseDurationFromMacro(macros["{$LINE_CHECK_INTERVAL}"], DefaultInterval),
			Router: Router{
				IP:       macros["{$LINE_ROUTER_IP}"],
				Username: macros["{$LINE_ROUTER_USERNAME}"],
				Password: macros["{$LINE_ROUTER_PASSWORD}"],
				Platform: connection.Platform(macros["{$LINE_ROUTER_PLATFORM}"]),
				Protocol: connection.Protocol(macros["{$LINE_ROUTER_PROTOCOL}"]),
			},
		}
		line.ComputeHash()
		lines[line.IP] = line
	}
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

func (cs *ConfigSyncer) Subscribe(ctx context.Context) *Subscription {
	ch := make(chan LineChangeEvent, 100)
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

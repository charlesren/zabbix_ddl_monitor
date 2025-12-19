package task

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charlesren/ylog"
	"github.com/charlesren/zabbix_ddl_monitor/connection"
	"github.com/scrapli/scrapligo/channel"
)

// Ping任务状态常量
const (
	StatusCheckFinished   = "CheckFinished"     // 检查正常完成（无论ping结果）
	StatusCheckTimeout    = "CheckTimeout"      // 检查超时
	StatusParseFailed     = "ParseResultFailed" // 解析失败
	StatusConnectionError = "ConnectionError"   // 连接错误
	StatusExecutionError  = "ExecutionError"    // 执行错误（其他错误）
	StatusRequestTimeout  = "RequestTimeout"    // 请求超时
	StatusNoRouteToHost   = "NoRouteToHost"     // 无路由到主机
)

// Zabbix sender错误状态常量
const (
	StatusMissingStatusField    = "MissingStatusField"    // 事件缺少status字段
	StatusPacketLossOutOfRange  = "PacketLossOutOfRange"  // packet_loss值超出0-100范围
	StatusInvalidPacketLossData = "InvalidPacketLossData" // packet_loss数据无效（类型错误或不存在）
)

type PingTask struct{}

func (PingTask) Meta() TaskMeta {
	return TaskMeta{
		Type:        "ping",
		Description: "Ping task for network devices",
		Platforms: []PlatformSupport{
			{
				Platform: connection.PlatformCiscoIOSXE,
				Protocols: []ProtocolSupport{
					{
						Protocol: connection.ProtocolScrapli,
						CommandTypes: []CommandTypeSupport{
							{
								CommandType: connection.CommandTypeInteractiveEvent,
								ImplFactory: func() Task { return &PingTask{} },
								Params: []ParamSpec{
									{Name: "target_ip", Type: "string", Required: true},
									{Name: "repeat", Type: "int", Required: false, Default: 5},
									{Name: "timeout", Type: "duration", Required: false, Default: 2 * time.Second},
									{Name: "enable_password", Type: "string", Required: false, Default: ""},
								},
							},
							{
								CommandType: connection.CommandTypeCommands,
								ImplFactory: func() Task { return &PingTask{} },
								Params: []ParamSpec{
									{Name: "target_ip", Type: "string", Required: true},
									{Name: "repeat", Type: "int", Required: false, Default: 5},
									{Name: "timeout", Type: "duration", Required: false, Default: 2 * time.Second},
									{Name: "enable_password", Type: "string", Required: false, Default: ""},
								},
							},
						},
					},
				},
			},
			{
				Platform: connection.PlatformCiscoIOSXR,
				Protocols: []ProtocolSupport{
					{
						Protocol: connection.ProtocolScrapli,
						CommandTypes: []CommandTypeSupport{
							{
								CommandType: connection.CommandTypeInteractiveEvent,
								ImplFactory: func() Task { return &PingTask{} },
								Params: []ParamSpec{
									{Name: "target_ip", Type: "string", Required: true},
									{Name: "repeat", Type: "int", Required: false, Default: 5},
									{Name: "timeout", Type: "duration", Required: false, Default: 2 * time.Second},
									{Name: "enable_password", Type: "string", Required: false, Default: ""},
								},
							},
							{
								CommandType: connection.CommandTypeCommands,
								ImplFactory: func() Task { return &PingTask{} },
								Params: []ParamSpec{
									{Name: "target_ip", Type: "string", Required: true},
									{Name: "repeat", Type: "int", Required: false, Default: 5},
									{Name: "timeout", Type: "duration", Required: false, Default: 2 * time.Second},
									{Name: "enable_password", Type: "string", Required: false, Default: ""},
								},
							},
						},
					},
				},
			},
			{
				Platform: connection.PlatformCiscoNXOS,
				Protocols: []ProtocolSupport{
					{
						Protocol: connection.ProtocolScrapli,
						CommandTypes: []CommandTypeSupport{
							{
								CommandType: connection.CommandTypeInteractiveEvent,
								ImplFactory: func() Task { return &PingTask{} },
								Params: []ParamSpec{
									{Name: "target_ip", Type: "string", Required: true},
									{Name: "repeat", Type: "int", Required: false, Default: 5},
									{Name: "timeout", Type: "duration", Required: false, Default: 2 * time.Second},
									{Name: "enable_password", Type: "string", Required: false, Default: ""},
								},
							},
							{
								CommandType: connection.CommandTypeCommands,
								ImplFactory: func() Task { return &PingTask{} },
								Params: []ParamSpec{
									{Name: "target_ip", Type: "string", Required: true},
									{Name: "repeat", Type: "int", Required: false, Default: 5},
									{Name: "timeout", Type: "duration", Required: false, Default: 2 * time.Second},
									{Name: "enable_password", Type: "string", Required: false, Default: ""},
								},
							},
						},
					},
				},
			},
			{
				Platform: connection.PlatformHuaweiVRP,
				Protocols: []ProtocolSupport{
					{
						Protocol: Protocol(connection.ProtocolScrapli),
						CommandTypes: []CommandTypeSupport{
							{
								CommandType: connection.CommandTypeInteractiveEvent,
								ImplFactory: func() Task { return &PingTask{} },
								Params: []ParamSpec{
									{Name: "target_ip", Type: "string", Required: true},
									{Name: "repeat", Type: "int", Required: false, Default: 5},
									{Name: "timeout", Type: "duration", Required: false, Default: 2 * time.Second},
									{Name: "enable_password", Type: "string", Required: false, Default: ""},
								},
							},
							{
								CommandType: connection.CommandTypeCommands,
								ImplFactory: func() Task { return &PingTask{} },
								Params: []ParamSpec{
									{Name: "target_ip", Type: "string", Required: true},
									{Name: "repeat", Type: "int", Required: false, Default: 5},
									{Name: "timeout", Type: "duration", Required: false, Default: 2 * time.Second},
									{Name: "enable_password", Type: "string", Required: false, Default: ""},
								},
							},
						},
					},
				},
			},
			{
				Platform: connection.PlatformH3CComware,
				Protocols: []ProtocolSupport{
					{
						Protocol: connection.ProtocolScrapli,
						CommandTypes: []CommandTypeSupport{
							{
								CommandType: connection.CommandTypeInteractiveEvent,
								ImplFactory: func() Task { return &PingTask{} },
								Params: []ParamSpec{
									{Name: "target_ip", Type: "string", Required: true},
									{Name: "repeat", Type: "int", Required: false, Default: 5},
									{Name: "timeout", Type: "duration", Required: false, Default: 2 * time.Second},
									// H3C Comware通常不需要enable_password
								},
							},
							{
								CommandType: connection.CommandTypeCommands,
								ImplFactory: func() Task { return &PingTask{} },
								Params: []ParamSpec{
									{Name: "target_ip", Type: "string", Required: true},
									{Name: "repeat", Type: "int", Required: false, Default: 5},
									{Name: "timeout", Type: "duration", Required: false, Default: 2 * time.Second},
									// H3C Comware通常不需要enable_password
								},
							},
						},
					},
				},
			},
		},
	}
}

func (PingTask) ValidateParams(params map[string]interface{}) error {
	ylog.Debugf("PingTask", "开始验证参数: %+v", params)

	// 验证必需参数 - 只支持单个target_ip
	targetIP, hasTargetIP := params["target_ip"]

	if !hasTargetIP {
		ylog.Errorf("PingTask", "参数验证失败: target_ip 参数是必需的")
		return fmt.Errorf("target_ip parameter is required")
	}

	// 验证IP参数
	if targetIPStr, ok := targetIP.(string); ok {
		if targetIPStr == "" {
			ylog.Errorf("PingTask", "参数验证失败: target_ip 不能为空")
			return fmt.Errorf("target_ip cannot be empty")
		}
		ylog.Debugf("PingTask", "验证IP参数: %s", targetIPStr)
	} else {
		ylog.Errorf("PingTask", "参数验证失败: target_ip 必须是字符串类型")
		return fmt.Errorf("target_ip must be a string")
	}

	// 验证可选参数
	if repeat, ok := params["repeat"]; ok {
		if repeatInt, ok := repeat.(int); ok {
			if repeatInt <= 0 || repeatInt > 100 {
				ylog.Errorf("PingTask", "参数验证失败: repeat 必须在 1 到 100 之间")
				return fmt.Errorf("repeat must be between 1 and 100")
			}
			ylog.Debugf("PingTask", "验证repeat参数: %d", repeatInt)
		} else {
			ylog.Errorf("PingTask", "参数验证失败: repeat 必须是整数类型")
			return fmt.Errorf("repeat must be an integer")
		}
	}

	if timeout, ok := params["timeout"]; ok {
		if timeoutDur, ok := timeout.(time.Duration); ok {
			if timeoutDur <= 0 || timeoutDur > 60*time.Second {
				ylog.Errorf("PingTask", "参数验证失败: timeout 必须在 1ms 到 60s 之间")
				return fmt.Errorf("timeout must be between 1ms and 60s")
			}
			ylog.Debugf("PingTask", "验证timeout参数: %v", timeoutDur)
		} else {
			ylog.Errorf("PingTask", "参数验证失败: timeout 必须是时间间隔类型")
			return fmt.Errorf("timeout must be a time.Duration")
		}
	}

	// 验证enable_password如果提供
	if enablePwd, ok := params["enable_password"]; ok {
		if _, ok := enablePwd.(string); !ok {
			ylog.Errorf("PingTask", "参数验证失败: enable_password 必须是字符串类型")
			return fmt.Errorf("enable_password must be a string")
		}
		ylog.Debugf("PingTask", "enable_password 参数已提供")
	}

	ylog.Debugf("PingTask", "参数验证成功")
	return nil
}

func (PingTask) BuildCommand(ctx TaskContext) (Command, error) {
	ylog.Infof("PingTask", "开始构建命令, 平台: %s, 命令类型: %s", ctx.Platform, ctx.CommandType)

	// 获取目标IP
	targetIPRaw, ok := ctx.Params["target_ip"]
	if !ok {
		ylog.Errorf("PingTask", "构建命令失败: target_ip 参数缺失")
		return Command{}, fmt.Errorf("target_ip parameter is required")
	}

	targetIP, ok := targetIPRaw.(string)
	if !ok {
		ylog.Errorf("PingTask", "构建命令失败: target_ip 必须是字符串类型")
		return Command{}, fmt.Errorf("target_ip must be string")
	}

	ylog.Debugf("PingTask", "目标IP: %s", targetIP)

	repeat := 5
	if r, ok := ctx.Params["repeat"]; ok {
		if repeatInt, ok := r.(int); ok {
			repeat = repeatInt
			ylog.Debugf("PingTask", "设置ping重复次数: %d", repeat)
		} else {
			ylog.Errorf("PingTask", "构建命令失败: repeat 必须是整数类型")
			return Command{}, fmt.Errorf("repeat must be integer")
		}
	}

	timeout := 2 * time.Second
	if t, ok := ctx.Params["timeout"]; ok {
		if timeoutDur, ok := t.(time.Duration); ok {
			timeout = timeoutDur
			ylog.Debugf("PingTask", "设置ping超时时间: %v", timeout)
		} else {
			ylog.Errorf("PingTask", "构建命令失败: timeout 必须是时间间隔类型")
			return Command{}, fmt.Errorf("timeout must be time.Duration")
		}
	}

	// 获取enable密码（如果提供）
	enablePassword := ""
	if pwd, ok := ctx.Params["enable_password"]; ok {
		if pwdStr, ok := pwd.(string); ok {
			enablePassword = pwdStr
			ylog.Debugf("PingTask", "已获取enable密码")
		}
	}

	// 根据命令类型构建不同的命令
	switch ctx.CommandType {
	case connection.CommandTypeCommands:
		// 构建非交互式命令
		var commands []string
		switch ctx.Platform {
		case connection.PlatformCiscoIOSXE:
			commands = PingTask{}.buildCiscoCommand(targetIP, repeat, timeout, enablePassword)
		case connection.PlatformCiscoIOSXR:
			commands = PingTask{}.buildCiscoCommand(targetIP, repeat, timeout, enablePassword)
		case connection.PlatformCiscoNXOS:
			commands = PingTask{}.buildCiscoCommand(targetIP, repeat, timeout, enablePassword)
		case connection.PlatformHuaweiVRP:
			commands = PingTask{}.buildHuaweiCommand(targetIP, repeat, timeout)
		case connection.PlatformH3CComware:
			commands = PingTask{}.buildH3CCommand(targetIP, repeat, timeout)
		default:
			ylog.Errorf("PingTask", "不支持的平台类型: %s", ctx.Platform)
			return Command{}, fmt.Errorf("unsupported platform for commands: %s", ctx.Platform)
		}

		ylog.Infof("PingTask", "构建非交互式命令完成, 命令数量: %d", len(commands))
		return Command{
			Type:    connection.CommandTypeCommands,
			Payload: commands,
		}, nil

	case connection.CommandTypeInteractiveEvent:
		// 构建交互式事件
		var events []*channel.SendInteractiveEvent
		switch ctx.Platform {
		case connection.PlatformCiscoIOSXE:
			events = PingTask{}.buildCiscoEvent(targetIP, repeat, timeout, enablePassword)
		case connection.PlatformCiscoIOSXR:
			events = PingTask{}.buildCiscoEvent(targetIP, repeat, timeout, enablePassword)
		case connection.PlatformCiscoNXOS:
			events = PingTask{}.buildCiscoEvent(targetIP, repeat, timeout, enablePassword)
		case connection.PlatformHuaweiVRP:
			events = PingTask{}.buildHuaweiEvent(targetIP, repeat, timeout)
		case connection.PlatformH3CComware:
			events = PingTask{}.buildH3CEvent(targetIP, repeat, timeout)
		default:
			ylog.Errorf("PingTask", "不支持的平台类型: %s", ctx.Platform)
			return Command{}, fmt.Errorf("unsupported platform: %s", ctx.Platform)
		}

		ylog.Infof("PingTask", "构建交互式事件完成, 事件数量: %d", len(events))
		return Command{
			Type:    connection.CommandTypeInteractiveEvent,
			Payload: events,
		}, nil

	default:
		ylog.Errorf("PingTask", "不支持的命令类型: %s", ctx.CommandType)
		return Command{}, fmt.Errorf("unsupported command type: %s", ctx.CommandType)
	}
}

// buildCiscoEvent 构建Cisco平台的单个ping命令
func (PingTask) buildCiscoEvent(targetIP string, repeat int, timeout time.Duration, enablePassword string) []*channel.SendInteractiveEvent {
	ylog.Debugf("PingTask", "构建Cisco交互式事件, 目标IP: %s", targetIP)

	var events []*channel.SendInteractiveEvent

	// 确定命令提示符
	prompt := ">"
	if enablePassword != "" {
		prompt = "#"
	}

	// 创建ping命令
	pingCommand := fmt.Sprintf("ping %s repeat %d timeout %d", targetIP, repeat, int(timeout.Seconds()))
	ylog.Debugf("PingTask", "添加Cisco ping命令: %s", pingCommand)

	events = append(events, &channel.SendInteractiveEvent{
		ChannelInput:    pingCommand,
		ChannelResponse: prompt,
		HideInput:       false,
	})

	ylog.Debugf("PingTask", "Cisco交互式事件构建完成, 事件总数: %d", len(events))
	return events
}

// buildHuaweiEvent 构建华为平台的单个ping命令
// buildHuaweiEvent 构建华为平台的单个ping命令（交互式）
func (PingTask) buildHuaweiEvent(targetIP string, repeat int, timeout time.Duration) []*channel.SendInteractiveEvent {
	ylog.Debugf("PingTask", "构建华为交互式事件, 目标IP: %s", targetIP)

	var events []*channel.SendInteractiveEvent

	// 创建ping命令，华为VRP使用-t参数表示超时时间（秒）
	pingCommand := fmt.Sprintf("ping -c %d -t %d %s", repeat, int(timeout.Seconds()), targetIP)
	ylog.Debugf("PingTask", "添加华为ping命令: %s", pingCommand)

	events = append(events, &channel.SendInteractiveEvent{
		ChannelInput:    pingCommand,
		ChannelResponse: ">",
		HideInput:       false,
	})

	ylog.Debugf("PingTask", "华为交互式事件构建完成, 事件总数: %d", len(events))
	return events
}

// buildCiscoCommand 构建Cisco平台的单个ping命令（非交互式）
func (PingTask) buildCiscoCommand(targetIP string, repeat int, timeout time.Duration, enablePassword string) []string {
	ylog.Debugf("PingTask", "构建Cisco非交互式命令, 目标IP: %s", targetIP)

	var commands []string

	// 只有提供了enable密码才进入特权模式
	if enablePassword != "" {
		commands = append(commands, "enable", enablePassword)
		ylog.Debugf("PingTask", "添加enable命令和密码")
	}

	// 创建ping命令
	command := fmt.Sprintf("ping %s timeout %d", targetIP, int(timeout.Seconds()))
	ylog.Debugf("PingTask", "添加Cisco ping命令: %s", command)
	commands = append(commands, command)

	ylog.Debugf("PingTask", "Cisco非交互式命令构建完成, 命令总数: %d", len(commands))
	return commands
}

// buildHuaweiCommand 构建华为平台的单个ping命令（非交互式）
func (PingTask) buildHuaweiCommand(targetIP string, repeat int, timeout time.Duration) []string {
	ylog.Debugf("PingTask", "构建华为非交互式命令, 目标IP: %s", targetIP)

	var commands []string

	// 创建ping命令，华为VRP使用-t参数表示超时时间（秒）
	command := fmt.Sprintf("ping -c %d -t %d %s", repeat, int(timeout.Seconds()), targetIP)
	ylog.Debugf("PingTask", "添加华为ping命令: %s", command)
	commands = append(commands, command)

	ylog.Debugf("PingTask", "华为非交互式命令构建完成, 命令总数: %d", len(commands))
	return commands
}

// buildH3CCommand 构建H3C Comware平台的单个ping命令（非交互式）
func (PingTask) buildH3CCommand(targetIP string, repeat int, timeout time.Duration) []string {
	ylog.Debugf("PingTask", "构建H3C非交互式命令, 目标IP: %s", targetIP)

	var commands []string
	// H3C Comware使用-t参数表示超时时间（秒）
	command := fmt.Sprintf("ping -c %d -t %d %s", repeat, int(timeout.Seconds()), targetIP)
	ylog.Debugf("PingTask", "添加H3C ping命令: %s", command)
	commands = append(commands, command)

	ylog.Debugf("PingTask", "H3C非交互式命令构建完成, 命令总数: %d", len(commands))
	return commands
}

// buildH3CEvent 构建H3C Comware平台的单个ping命令（交互式）
func (PingTask) buildH3CEvent(targetIP string, repeat int, timeout time.Duration) []*channel.SendInteractiveEvent {
	ylog.Debugf("PingTask", "构建H3C交互式事件, 目标IP: %s", targetIP)

	var events []*channel.SendInteractiveEvent
	// H3C Comware使用-t参数表示超时时间（秒）
	pingCommand := fmt.Sprintf("ping -c %d -t %d %s", repeat, int(timeout.Seconds()), targetIP)
	ylog.Debugf("PingTask", "添加H3C ping命令: %s", pingCommand)

	events = append(events, &channel.SendInteractiveEvent{
		ChannelInput:    pingCommand,
		ChannelResponse: ">", // H3C Comware通常使用">"作为提示符
		HideInput:       false,
	})

	ylog.Debugf("PingTask", "H3C交互式事件构建完成, 事件总数: %d", len(events))
	return events
}

//ping 命令输出示例
// 1. ZZB0000_DTT_05_OTV01 : cisco_iosxe系统
/*
// 无丢包
```shell
ZZB0000_DTT_05_OTV01#ping 10.194.10.106
Type escape sequence to abort.
Sending 5, 100-byte ICMP Echos to 10.194.10.106, timeout is 2 seconds:
!!!!!
Success rate is 100 percent (5/5), round-trip min/avg/max = 1/1/1 ms
ZZB0000_DTT_05_OTV01#
```

// 不通
 ```shell
 ZZA0000_17_PRO_CS05-10.192.253.228#ping 123.123.123.123
 Type escape sequence to abort.
 Sending 5, 100-byte ICMP Echos to 123.123.123.123, timeout is 2 seconds:
 .....
 Success rate is 0 percent (0/5)
 ZZA0000_17_PRO_CS05#
 ```
*/

// 2. ZZA_DTT_17_SA21  : cisco_iosxr系统
/*
// 无丢包
```shell
ZZA_DTT_17_SA21#
> ping 10.194.10.106

ping 10.194.10.106
Type escape sequence to abort.
Sending 5, 100-byte ICMP Echos to 10.194.10.106, timeout is 2 seconds:
!!!!!
Success rate is 100 percent (5/5), round-trip min/avg/max = 1/2/4 ms
ZZA_DTT_17_SA21#
>
```
// 不通
```shell
RP/0/RSP0/CPU0:ZZA0000-SRB-WANPE-10.252.254.6#ping 123.123.123.123
Mon Dec 15 10:17:58.918 CST
Type escape sequence to abort.
Sending 5, 100-byte ICMP Echos to 123.123.123.123, timeout is 2 seconds:
UUUUU
Success rate is 0 percent (0/5)
RP/0/RSP0/CPU0:ZZA0000-SRB-WANPE#
```
*/

// 3. ZZB0000_DTT_05_SA17 : cisco_nxos系统
/*
```shell
ZZB0000_DTT_05_SA17# ping 10.194.17.20
PING 10.194.17.20 (10.194.17.20): 56 data bytes
64 bytes from 10.194.17.20: icmp_seq=0 ttl=61 time=1.687 ms
64 bytes from 10.194.17.20: icmp_seq=1 ttl=61 time=1.116 ms
64 bytes from 10.194.17.20: icmp_seq=2 ttl=61 time=1.405 ms
64 bytes from 10.194.17.20: icmp_seq=3 ttl=61 time=1.374 ms
64 bytes from 10.194.17.20: icmp_seq=4 ttl=61 time=1.416 ms

--- 10.194.17.20 ping statistics ---
5 packets transmitted, 5 packets received, 0.00% packet loss
round-trip min/avg/max = 1.116/1.399/1.687 ms
ZZB0000_DTT_05_SA17#
```
*/
// 4 .  ZZA0000_17_PRO_WR37-10.192.253.238 : huawei_vrp 系统
/*
// 不通
```shell
<ZZA0000_17_PRO_WR37-10.192.253.238>ping 123.123.123.123
Ping 123.123.123.123 (123.123.123.123): 56 data bytes, press CTRL+C to break
Request time out
Request time out
Request time out
Request time out
Request time out
```
*/
func (PingTask) ParseOutput(ctx TaskContext, raw interface{}) (Result, error) {
	ylog.Infof("PingTask", "开始解析输出, 平台: %s", ctx.Platform)

	// 安全的类型转换
	var output string
	switch v := raw.(type) {
	case string:
		output = v
		ylog.Debugf("PingTask", "输出类型: string, 长度: %d", len(output))
	case []byte:
		output = string(v)
		ylog.Debugf("PingTask", "输出类型: []byte, 长度: %d", len(output))
	default:
		ylog.Errorf("PingTask", "不支持的输出类型: %T", raw)
		return Result{
			Success: false,
			Error:   "unsupported output type",
			Data: map[string]interface{}{
				"status": StatusParseFailed,
			},
		}, fmt.Errorf("unsupported output type: %T", raw)
	}

	// 获取目标IP
	targetIP, ok := ctx.Params["target_ip"].(string)
	if !ok {
		ylog.Errorf("PingTask", "解析输出失败: target_ip 参数缺失或类型错误")
		return Result{
			Success: false,
			Error:   "target_ip parameter missing or invalid",
			Data: map[string]interface{}{
				"status": StatusParseFailed,
			},
		}, fmt.Errorf("target_ip parameter missing or invalid")
	}

	ylog.Debugf("PingTask", "解析目标IP: %s", targetIP)

	result := Result{
		Data: map[string]interface{}{
			"target_ip":  targetIP,
			"raw_output": output,
		},
	}

	// 根据平台解析输出
	switch ctx.Platform {
	case connection.PlatformCiscoIOSXE:
		result.Success = PingTask{}.parseCiscoOutput(output, &result)
	case connection.PlatformCiscoIOSXR:
		result.Success = PingTask{}.parseCiscoOutput(output, &result)
	case connection.PlatformCiscoNXOS:
		result.Success = PingTask{}.parseCiscoNxosOutput(output, &result)
	case connection.PlatformHuaweiVRP:
		result.Success = PingTask{}.parseHuaweiOutput(output, &result)
	case connection.PlatformH3CComware:
		result.Success = PingTask{}.parseH3COutput(output, &result)
	default:
		result.Success = PingTask{}.parseGenericOutput(output, &result)
	}

	ylog.Infof("PingTask", "解析完成, IP: %s, 结果: %v", targetIP, result.Success)
	ylog.Debugf("PingTask", "输出解析完成, 最终结果: %+v", result)
	return result, nil
}

// parseCiscoOutput 解析Cisco设备的ping输出
func (PingTask) parseCiscoOutput(output string, result *Result) bool {
	ylog.Debugf("PingTask", "开始解析Cisco输出")

	lines := strings.Split(output, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// 查找成功率行，如 "Success rate is 100 percent (5/5)"
		if strings.Contains(line, "Success rate is") {
			if strings.Contains(line, "100 percent") {
				result.Data["success_rate"] = 100
				result.Data["packet_loss"] = 0
				result.Data["status"] = StatusCheckFinished
				ylog.Debugf("PingTask", "Cisco parsing: 100%% success rate, 0%% packet loss, status=%s", StatusCheckFinished)
				return true
			} else if strings.Contains(line, "0 percent") {
				result.Data["success_rate"] = 0
				result.Data["packet_loss"] = 100
				result.Data["status"] = StatusCheckFinished
				ylog.Debugf("PingTask", "Cisco parsing: 0%% success rate, 100%% packet loss, status=%s", StatusCheckFinished)
				return true
			} else {
				// 提取百分比
				parts := strings.Fields(line)
				for i, part := range parts {
					if part == "percent" && i > 0 {
						// 计算成功率和丢包率
						if successRate, err := strconv.Atoi(parts[i-1]); err == nil {
							result.Data["success_rate"] = successRate
							result.Data["packet_loss"] = 100 - successRate
						} else {
							ylog.Errorf("PingTask", "Cisco parsing: 无法解析百分比值: %s", parts[i-1])
							result.Data["status"] = StatusParseFailed
							return false
						}
						break
					}
				}
				result.Data["status"] = StatusCheckFinished
				ylog.Debugf("PingTask", "Cisco parsing: partial success rate: %v, packet loss: %v, status=%s", result.Data["success_rate"], result.Data["packet_loss"], StatusCheckFinished)
				return true
			}
		}

		// 查找RTT信息，如 "round-trip min/avg/max = 1/2/4 ms"
		if strings.Contains(line, "round-trip") && strings.Contains(line, "min/avg/max") {
			result.Data["rtt_info"] = line
			ylog.Debugf("PingTask", "Cisco parsing: found RTT info: %s", line)
		}
	}

	ylog.Errorf("PingTask", "Cisco parsing: no success/failure pattern found")
	result.Data["status"] = StatusParseFailed
	return false
}

// parseCiscoNxosOutput 解析Cisco NXOS设备的ping输出
func (PingTask) parseCiscoNxosOutput(output string, result *Result) bool {
	ylog.Debugf("PingTask", "开始解析Cisco NXOS输出")

	lines := strings.Split(output, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// 查找包丢失率，如 "0.00% packet loss"
		if strings.Contains(line, "packet loss") {
			if strings.Contains(line, "0.00% packet loss") {
				result.Data["packet_loss"] = 0
				result.Data["success_rate"] = 100
				result.Data["status"] = StatusCheckFinished
				ylog.Debugf("PingTask", "Cisco NXOS parsing: 0%% packet loss, 100%% success rate, status=%s", StatusCheckFinished)
				return true
			} else if strings.Contains(line, "100.00% packet loss") {
				result.Data["packet_loss"] = 100
				result.Data["success_rate"] = 0
				result.Data["status"] = StatusCheckFinished
				ylog.Debugf("PingTask", "Cisco NXOS parsing: 100%% packet loss, 0%% success rate, status=%s", StatusCheckFinished)
				return true
			} else {
				// 提取丢包率
				parts := strings.Fields(line)
				for _, part := range parts {
					if strings.HasSuffix(part, "%") && strings.Contains(part, "packet") {
						// 提取数字部分并转换为整数
						if packetLossStr := strings.TrimSuffix(part, "%"); packetLossStr != "" {
							if packetLoss, err := strconv.Atoi(strings.TrimSuffix(packetLossStr, ".00")); err == nil {
								result.Data["packet_loss"] = packetLoss
								result.Data["success_rate"] = 100 - packetLoss
							} else {
								ylog.Errorf("PingTask", "Cisco NXOS parsing: 无法解析丢包率值: %s", packetLossStr)
								result.Data["status"] = StatusParseFailed
								return false
							}
						}
						break
					}
				}
				result.Data["status"] = StatusCheckFinished
				ylog.Debugf("PingTask", "Cisco NXOS parsing: partial packet loss: %v, success rate: %v, status=%s", result.Data["packet_loss"], result.Data["success_rate"], StatusCheckFinished)
				return true
			}
		}

		// 查找RTT信息，如 "round-trip min/avg/max = 1.116/1.399/1.687 ms"
		if strings.Contains(line, "round-trip") && strings.Contains(line, "min/avg/max") {
			result.Data["rtt_info"] = line
			ylog.Debugf("PingTask", "Cisco NXOS parsing: found RTT info: %s", line)
		}

		// 查找传输统计，如 "5 packets transmitted, 5 packets received"
		if strings.Contains(line, "packets transmitted") && strings.Contains(line, "packets received") {
			result.Data["packet_stats"] = line
			ylog.Debugf("PingTask", "Cisco NXOS parsing: found packet stats: %s", line)
		}
	}

	ylog.Errorf("PingTask", "Cisco NXOS parsing: no packet loss pattern found")
	result.Data["status"] = StatusParseFailed
	return false
}

// parseHuaweiOutput 解析华为设备的ping输出
func (PingTask) parseHuaweiOutput(output string, result *Result) bool {
	ylog.Debugf("PingTask", "开始解析华为输出")

	lines := strings.Split(output, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// 检查是否为Request time out（华为设备不通时的输出）
		if strings.Contains(strings.ToLower(line), "request time out") {
			result.Data["packet_loss"] = 100
			result.Data["success_rate"] = 0
			result.Data["status"] = StatusCheckFinished
			ylog.Debugf("PingTask", "Huawei parsing: Request time out detected, 100%% packet loss, status=%s", StatusCheckFinished)
			return true
		}

		// 查找packet loss行
		if strings.Contains(line, "packet loss") {
			// 首先检查100%（最具体的）
			if strings.Contains(line, "100% packet loss") {
				result.Data["packet_loss"] = 100
				result.Data["success_rate"] = 0
				result.Data["status"] = StatusCheckFinished
				ylog.Debugf("PingTask", "Huawei parsing: 100%% packet loss, 0%% success rate, status=%s", StatusCheckFinished)
				return true
			} else if strings.Contains(line, "0% packet loss") {
				result.Data["packet_loss"] = 0
				result.Data["success_rate"] = 100
				result.Data["status"] = StatusCheckFinished
				ylog.Debugf("PingTask", "Huawei parsing: 0%% packet loss, 100%% success rate, status=%s", StatusCheckFinished)
				return true
			} else {
				// 提取丢包率
				parts := strings.Fields(line)
				for _, part := range parts {
					if strings.HasSuffix(part, "%") {
						// 提取数字部分并转换为整数
						if packetLossStr := strings.TrimSuffix(part, "%"); packetLossStr != "" {
							if packetLoss, err := strconv.Atoi(packetLossStr); err == nil {
								result.Data["packet_loss"] = packetLoss
								result.Data["success_rate"] = 100 - packetLoss
								if packetLoss == 0 {
									result.Data["status"] = StatusCheckFinished
									return true
								} else if packetLoss == 100 {
									result.Data["status"] = StatusCheckFinished
									return true
								} else {
									result.Data["status"] = StatusCheckFinished
									return true
								}
							} else {
								ylog.Errorf("PingTask", "Huawei parsing: 无法解析丢包率值: %s", packetLossStr)
								result.Data["status"] = StatusParseFailed
								return false
							}
						}
						break
					}
				}
				result.Data["status"] = StatusCheckFinished
				ylog.Debugf("PingTask", "Huawei parsing: partial packet loss: %v, success rate: %v, status=%s", result.Data["packet_loss"], result.Data["success_rate"], StatusCheckFinished)
				return true
			}
		}

		// 查找RTT信息
		if strings.Contains(line, "min/avg/max") {
			result.Data["rtt_info"] = line
			ylog.Debugf("PingTask", "Huawei parsing: found RTT info: %s", line)
		}
	}

	ylog.Debugf("PingTask", "Huawei parsing: no packet loss info found")
	ylog.Errorf("PingTask", "Huawei parsing: no packet loss info found")
	result.Data["status"] = StatusParseFailed
	return false
}

// parseH3COutput 解析H3C Comware设备的ping输出
func (PingTask) parseH3COutput(output string, result *Result) bool {
	ylog.Debugf("PingTask", "开始解析H3C输出")

	lines := strings.Split(output, "\n")

	// 首先检查特殊情况
	for _, line := range lines {
		line = strings.TrimSpace(line)
		lowerLine := strings.ToLower(line)

		// 检查请求超时
		if strings.Contains(lowerLine, "request time out") {
			result.Data["status"] = StatusRequestTimeout
			ylog.Debugf("PingTask", "H3C parsing: request timeout detected, 100%% packet loss, status=%s", StatusCheckFinished)
			return true
		}

		// 检查主机不可达
		if strings.Contains(lowerLine, "destination host unreachable") ||
			strings.Contains(lowerLine, "no route to host") {
			result.Data["status"] = StatusNoRouteToHost
			ylog.Debugf("PingTask", "H3C parsing: host unreachable, 100%% packet loss, status=%s", StatusCheckFinished)
			return true
		}
	}

	// 查找包含packet loss的行
	for _, line := range lines {
		line = strings.TrimSpace(line)
		lowerLine := strings.ToLower(line)

		if strings.Contains(lowerLine, "packet loss") {
			// 使用正则表达式匹配百分比
			re := regexp.MustCompile(`(\d+\.?\d*)%\s*packet\s*loss`)
			if matches := re.FindStringSubmatch(line); len(matches) > 1 {
				// 解析带小数的百分比
				if packetLossFloat, err := strconv.ParseFloat(matches[1], 64); err == nil {
					// 确保值在0-100范围内
					packetLoss := int(packetLossFloat)
					if packetLoss < 0 {
						packetLoss = 0
					} else if packetLoss > 100 {
						packetLoss = 100
					}

					result.Data["packet_loss"] = packetLoss
					result.Data["success_rate"] = 100 - packetLoss
					result.Data["status"] = StatusCheckFinished
					ylog.Debugf("PingTask", "H3C parsing: %s%% packet loss, %d%% success rate, status=%s",
						matches[1], 100-packetLoss, StatusCheckFinished)
					return true
				} else {
					ylog.Errorf("PingTask", "H3C parsing: 无法解析百分比值: %s", matches[1])
					result.Data["status"] = StatusParseFailed
					return false
				}
			}
		}

		// 查找RTT信息
		if strings.Contains(line, "round-trip") && strings.Contains(line, "min/avg/max") {
			result.Data["rtt_info"] = line
			ylog.Debugf("PingTask", "H3C parsing: found RTT info: %s", line)
		}
	}

	ylog.Errorf("PingTask", "H3C parsing: no packet loss pattern found")
	result.Data["status"] = StatusParseFailed
	return false
}

// parseGenericOutput 通用ping输出解析
func (PingTask) parseGenericOutput(output string, result *Result) bool {
	ylog.Debugf("PingTask", "开始通用输出解析")
	output = strings.ToLower(output)

	// 通用成功模式
	successPatterns := []string{
		"0% packet loss",
		"0% loss",
		"100 percent",
		"success rate is 100",
	}

	for _, pattern := range successPatterns {
		if strings.Contains(output, pattern) {
			result.Data["status"] = StatusCheckFinished
			result.Data["success_rate"] = 100
			result.Data["packet_loss"] = 0
			ylog.Debugf("PingTask", "通用解析: 匹配成功模式: %s, status=%s", pattern, StatusCheckFinished)
			return true
		}
	}

	// 通用失败模式
	failPatterns := []string{
		"100% packet loss",
		"100% loss",
		"0 percent",
		"destination host unreachable",
		"request timeout",
		"no route to host",
	}

	for _, pattern := range failPatterns {
		if strings.Contains(output, pattern) {
			result.Data["status"] = StatusCheckFinished
			result.Data["success_rate"] = 0
			result.Data["packet_loss"] = 100
			ylog.Debugf("PingTask", "通用解析: 匹配失败模式: %s, status=%s", pattern, StatusCheckFinished)
			return true
		}
	}

	// 如果包含ping相关输出但无法确定结果
	if strings.Contains(output, "ping") || strings.Contains(output, "icmp") {
		ylog.Errorf("PingTask", "通用解析: 包含ping/icmp但无法确定结果，输出: %s", output)
		result.Data["status"] = StatusParseFailed
		ylog.Debugf("PingTask", "通用解析: 包含ping/icmp但无法确定结果, status=%s", StatusParseFailed)
		return false
	}

	ylog.Errorf("PingTask", "通用解析: 无法识别输出内容，输出: %s", output)
	result.Data["status"] = StatusParseFailed
	ylog.Debugf("PingTask", "通用解析: 无法识别输出内容, status=%s", StatusParseFailed)
	return false
}

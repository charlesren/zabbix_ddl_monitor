package task

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charlesren/ylog"
	"github.com/charlesren/zabbix_ddl_monitor/connection"
	"github.com/scrapli/scrapligo/channel"
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
func (PingTask) buildHuaweiEvent(targetIP string, repeat int, timeout time.Duration) []*channel.SendInteractiveEvent {
	ylog.Debugf("PingTask", "构建华为交互式事件, 目标IP: %s", targetIP)

	var events []*channel.SendInteractiveEvent

	// 创建ping命令
	pingCommand := fmt.Sprintf("ping -c %d -W %d %s", repeat, int(timeout.Seconds()), targetIP)
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

	// 创建ping命令
	command := fmt.Sprintf("ping -c %d -W %d %s", repeat, int(timeout.Seconds()), targetIP)
	ylog.Debugf("PingTask", "添加华为ping命令: %s", command)
	commands = append(commands, command)

	ylog.Debugf("PingTask", "华为非交互式命令构建完成, 命令总数: %d", len(commands))
	return commands
}

//ping 命令输出示例
// 1. ZZB0000_DTT_05_OTV01 : cisco_iosxe系统
/*
```shell
ZZB0000_DTT_05_OTV01#ping 10.194.10.106
Type escape sequence to abort.
Sending 5, 100-byte ICMP Echos to 10.194.10.106, timeout is 2 seconds:
!!!!!
Success rate is 100 percent (5/5), round-trip min/avg/max = 1/1/1 ms
ZZB0000_DTT_05_OTV01#
*/

// 2. ZZA_DTT_17_SA21  : cisco_iosxr系统
/*
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
		}, fmt.Errorf("unsupported output type: %T", raw)
	}

	// 获取目标IP
	targetIP, ok := ctx.Params["target_ip"].(string)
	if !ok {
		ylog.Errorf("PingTask", "解析输出失败: target_ip 参数缺失或类型错误")
		return Result{
			Success: false,
			Error:   "target_ip parameter missing or invalid",
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
				result.Data["status"] = "success"
				ylog.Debugf("PingTask", "Cisco parsing: 100%% success rate, 0%% packet loss")
				return true
			} else if strings.Contains(line, "0 percent") {
				result.Data["success_rate"] = 0
				result.Data["packet_loss"] = 100
				result.Data["status"] = "failed"
				ylog.Debugf("PingTask", "Cisco parsing: 0%% success rate, 100%% packet loss")
				return false
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
							result.Data["success_rate"] = "unknown"
							result.Data["packet_loss"] = "unknown"
						}
						break
					}
				}
				result.Data["status"] = "partial"
				ylog.Debugf("PingTask", "Cisco parsing: partial success rate: %v, packet loss: %v", result.Data["success_rate"], result.Data["packet_loss"])
				return true
			}
		}

		// 查找RTT信息，如 "round-trip min/avg/max = 1/2/4 ms"
		if strings.Contains(line, "round-trip") && strings.Contains(line, "min/avg/max") {
			result.Data["rtt_info"] = line
			ylog.Debugf("PingTask", "Cisco parsing: found RTT info: %s", line)
		}
	}

	ylog.Debugf("PingTask", "Cisco parsing: no success/failure pattern found")
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
				result.Data["status"] = "success"
				ylog.Debugf("PingTask", "Cisco NXOS parsing: 0%% packet loss, 100%% success rate")
				return true
			} else if strings.Contains(line, "100.00% packet loss") {
				result.Data["packet_loss"] = 100
				result.Data["success_rate"] = 0
				result.Data["status"] = "failed"
				ylog.Debugf("PingTask", "Cisco NXOS parsing: 100%% packet loss, 0%% success rate")
				return false
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
								result.Data["packet_loss"] = "unknown"
								result.Data["success_rate"] = "unknown"
							}
						}
						break
					}
				}
				result.Data["status"] = "partial"
				ylog.Debugf("PingTask", "Cisco NXOS parsing: partial packet loss: %v, success rate: %v", result.Data["packet_loss"], result.Data["success_rate"])
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

	ylog.Debugf("PingTask", "Cisco NXOS parsing: no packet loss pattern found")
	return false
}

// parseHuaweiOutput 解析华为设备的ping输出
func (PingTask) parseHuaweiOutput(output string, result *Result) bool {
	ylog.Debugf("PingTask", "开始解析华为输出")

	lines := strings.Split(output, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// 查找packet loss行
		if strings.Contains(line, "packet loss") {
			// 首先检查100%（最具体的）
			if strings.Contains(line, "100% packet loss") {
				result.Data["packet_loss"] = 100
				result.Data["success_rate"] = 0
				result.Data["status"] = "failed"
				ylog.Debugf("PingTask", "Huawei parsing: 100%% packet loss, 0%% success rate")
				return false
			} else if strings.Contains(line, "0% packet loss") {
				result.Data["packet_loss"] = 0
				result.Data["success_rate"] = 100
				result.Data["status"] = "success"
				ylog.Debugf("PingTask", "Huawei parsing: 0%% packet loss, 100%% success rate")
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
									result.Data["status"] = "success"
									return true
								} else if packetLoss == 100 {
									result.Data["status"] = "failed"
									return false
								} else {
									result.Data["status"] = "partial"
									return true
								}
							} else {
								result.Data["packet_loss"] = "unknown"
								result.Data["success_rate"] = "unknown"
							}
						}
						break
					}
				}
				result.Data["status"] = "partial"
				ylog.Debugf("PingTask", "Huawei parsing: partial packet loss: %v, success rate: %v", result.Data["packet_loss"], result.Data["success_rate"])
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
			result.Data["status"] = "success"
			result.Data["success_rate"] = 100
			result.Data["packet_loss"] = 0
			ylog.Debugf("PingTask", "通用解析: 匹配成功模式: %s", pattern)
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
			result.Data["status"] = "failed"
			result.Data["success_rate"] = 0
			result.Data["packet_loss"] = 100
			ylog.Debugf("PingTask", "通用解析: 匹配失败模式: %s", pattern)
			return false
		}
	}

	// 如果包含ping相关输出但无法确定结果
	if strings.Contains(output, "ping") || strings.Contains(output, "icmp") {
		result.Data["status"] = "unknown"
		result.Data["success_rate"] = "unknown"
		result.Data["packet_loss"] = "unknown"
		ylog.Debugf("PingTask", "通用解析: 包含ping/icmp但无法确定结果")
		return true
	}

	result.Data["status"] = "error"
	result.Data["success_rate"] = "unknown"
	result.Data["packet_loss"] = "unknown"
	ylog.Debugf("PingTask", "通用解析: 无法识别输出内容")
	return false
}

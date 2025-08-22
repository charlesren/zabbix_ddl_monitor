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
									{Name: "target_ip", Type: "string", Required: false},
									{Name: "target_ips", Type: "[]string", Required: false},
									{Name: "repeat", Type: "int", Required: false, Default: 5},
									{Name: "timeout", Type: "duration", Required: false, Default: 2 * time.Second},
									{Name: "enable_password", Type: "string", Required: false, Default: ""},
								},
							},
							{
								CommandType: connection.CommandTypeCommands,
								ImplFactory: func() Task { return &PingTask{} },
								Params: []ParamSpec{
									{Name: "target_ip", Type: "string", Required: false},
									{Name: "target_ips", Type: "[]string", Required: false},
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
									{Name: "target_ip", Type: "string", Required: false},
									{Name: "target_ips", Type: "[]string", Required: false},
									{Name: "repeat", Type: "int", Required: false, Default: 5},
									{Name: "timeout", Type: "duration", Required: false, Default: 2 * time.Second},
									{Name: "enable_password", Type: "string", Required: false, Default: ""},
								},
							},
							{
								CommandType: connection.CommandTypeCommands,
								ImplFactory: func() Task { return &PingTask{} },
								Params: []ParamSpec{
									{Name: "target_ip", Type: "string", Required: false},
									{Name: "target_ips", Type: "[]string", Required: false},
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
									{Name: "target_ip", Type: "string", Required: false},
									{Name: "target_ips", Type: "[]string", Required: false},
									{Name: "repeat", Type: "int", Required: false, Default: 5},
									{Name: "timeout", Type: "duration", Required: false, Default: 2 * time.Second},
									{Name: "enable_password", Type: "string", Required: false, Default: ""},
								},
							},
							{
								CommandType: connection.CommandTypeCommands,
								ImplFactory: func() Task { return &PingTask{} },
								Params: []ParamSpec{
									{Name: "target_ip", Type: "string", Required: false},
									{Name: "target_ips", Type: "[]string", Required: false},
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
									{Name: "target_ip", Type: "string", Required: false},
									{Name: "target_ips", Type: "[]string", Required: false},
									{Name: "repeat", Type: "int", Required: false, Default: 5},
									{Name: "timeout", Type: "duration", Required: false, Default: 2 * time.Second},
									{Name: "enable_password", Type: "string", Required: false, Default: ""},
								},
							},
							{
								CommandType: connection.CommandTypeCommands,
								ImplFactory: func() Task { return &PingTask{} },
								Params: []ParamSpec{
									{Name: "target_ip", Type: "string", Required: false},
									{Name: "target_ips", Type: "[]string", Required: false},
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

	// 验证必需参数 - target_ip 或 target_ips 至少有一个
	targetIP, hasTargetIP := params["target_ip"]
	targetIPs, hasTargetIPs := params["target_ips"]

	if !hasTargetIP && !hasTargetIPs {
		ylog.Errorf("PingTask", "参数验证失败: target_ip 或 target_ips 至少需要一个")
		return fmt.Errorf("either target_ip or target_ips parameter is required")
	}

	// 验证单个IP参数
	if hasTargetIP {
		if targetIPStr, ok := targetIP.(string); ok {
			if targetIPStr == "" {
				ylog.Errorf("PingTask", "参数验证失败: target_ip 不能为空")
				return fmt.Errorf("target_ip cannot be empty")
			}
			ylog.Debugf("PingTask", "验证单个IP参数: %s", targetIPStr)
		} else {
			ylog.Errorf("PingTask", "参数验证失败: target_ip 必须是字符串类型")
			return fmt.Errorf("target_ip must be a string")
		}
	}

	// 验证批量IP参数
	if hasTargetIPs {
		if targetIPSlice, ok := targetIPs.([]string); ok {
			if len(targetIPSlice) == 0 {
				ylog.Errorf("PingTask", "参数验证失败: target_ips 不能为空数组")
				return fmt.Errorf("target_ips cannot be empty")
			}
			ylog.Debugf("PingTask", "验证批量IP参数: %d 个IP地址", len(targetIPSlice))
			for i, ip := range targetIPSlice {
				if ip == "" {
					ylog.Errorf("PingTask", "参数验证失败: target_ips[%d] 不能为空", i)
					return fmt.Errorf("target_ips[%d] cannot be empty", i)
				}
			}
		} else {
			ylog.Errorf("PingTask", "参数验证失败: target_ips 必须是字符串切片类型")
			return fmt.Errorf("target_ips must be a []string")
		}
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

	// 获取目标IP列表
	var targetIPs []string

	// 检查是否有单个IP
	if targetIPRaw, ok := ctx.Params["target_ip"]; ok {
		if targetIP, ok := targetIPRaw.(string); ok {
			targetIPs = []string{targetIP}
			ylog.Debugf("PingTask", "使用单个IP: %s", targetIP)
		} else {
			ylog.Errorf("PingTask", "构建命令失败: target_ip 必须是字符串类型")
			return Command{}, fmt.Errorf("target_ip must be string")
		}
	} else if targetIPsRaw, ok := ctx.Params["target_ips"]; ok {
		// 检查是否有批量IP
		if targetIPSlice, ok := targetIPsRaw.([]string); ok {
			targetIPs = targetIPSlice
			ylog.Debugf("PingTask", "使用批量IP: %d 个目标地址", len(targetIPSlice))
		} else {
			ylog.Errorf("PingTask", "构建命令失败: target_ips 必须是字符串切片类型")
			return Command{}, fmt.Errorf("target_ips must be []string")
		}
	} else {
		ylog.Errorf("PingTask", "构建命令失败: target_ip 或 target_ips 参数缺失")
		return Command{}, fmt.Errorf("either target_ip or target_ips parameter is required")
	}

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
			commands = PingTask{}.buildCiscoCommands(targetIPs, repeat, timeout, enablePassword)
		case connection.PlatformCiscoIOSXR:
			commands = PingTask{}.buildCiscoCommands(targetIPs, repeat, timeout, enablePassword)
		case connection.PlatformCiscoNXOS:
			commands = PingTask{}.buildCiscoCommands(targetIPs, repeat, timeout, enablePassword)
		case connection.PlatformHuaweiVRP:
			commands = PingTask{}.buildHuaweiCommands(targetIPs, repeat, timeout)
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
			events = PingTask{}.buildCiscoEvents(targetIPs, repeat, timeout, enablePassword)
		case connection.PlatformCiscoIOSXR:
			events = PingTask{}.buildCiscoEvents(targetIPs, repeat, timeout, enablePassword)
		case connection.PlatformCiscoNXOS:
			events = PingTask{}.buildCiscoEvents(targetIPs, repeat, timeout, enablePassword)
		case connection.PlatformHuaweiVRP:
			events = PingTask{}.buildHuaweiEvents(targetIPs, repeat, timeout)
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

// buildCiscoEvents 构建Cisco平台的批量ping命令
func (PingTask) buildCiscoEvents(targetIPs []string, repeat int, timeout time.Duration, enablePassword string) []*channel.SendInteractiveEvent {
	ylog.Debugf("PingTask", "构建Cisco交互式事件, 目标IP数量: %d", len(targetIPs))

	var events []*channel.SendInteractiveEvent
	/*
		// 只有提供了enable密码才尝试进入特权模式
		if enablePassword != "" {
			events = append(events,
				&channel.SendInteractiveEvent{
					ChannelInput:    "enable",
					ChannelResponse: "Password:",
					HideInput:       false,
				},
				&channel.SendInteractiveEvent{
					ChannelInput:    enablePassword,
					ChannelResponse: "#",
					HideInput:       true,
				})
		}
	*/

	// 为每个IP创建ping命令
	for _, ip := range targetIPs {
		prompt := ">"
		if enablePassword != "" {
			prompt = "#"
		}

		pingCommand := fmt.Sprintf("ping %s repeat %d timeout %d", ip, repeat, int(timeout.Seconds()))
		ylog.Debugf("PingTask", "添加Cisco ping命令: %s", pingCommand)

		events = append(events, &channel.SendInteractiveEvent{
			ChannelInput:    pingCommand,
			ChannelResponse: prompt,
			HideInput:       false,
		})
	}

	ylog.Debugf("PingTask", "Cisco交互式事件构建完成, 事件总数: %d", len(events))
	return events
}

// buildHuaweiEvents 构建华为平台的批量ping命令
func (PingTask) buildHuaweiEvents(targetIPs []string, repeat int, timeout time.Duration) []*channel.SendInteractiveEvent {
	ylog.Debugf("PingTask", "构建华为交互式事件, 目标IP数量: %d", len(targetIPs))

	var events []*channel.SendInteractiveEvent

	// 为每个IP创建ping命令
	for _, ip := range targetIPs {
		pingCommand := fmt.Sprintf("ping -c %d -W %d %s", repeat, int(timeout.Seconds()), ip)
		ylog.Debugf("PingTask", "添加华为 ping命令: %s", pingCommand)

		events = append(events, &channel.SendInteractiveEvent{
			ChannelInput:    pingCommand,
			ChannelResponse: ">",
			HideInput:       false,
		})
	}

	ylog.Debugf("PingTask", "华为交互式事件构建完成, 事件总数: %d", len(events))
	return events
}

// buildCiscoCommands 构建Cisco平台的批量ping命令（非交互式）
func (PingTask) buildCiscoCommands(targetIPs []string, repeat int, timeout time.Duration, enablePassword string) []string {
	ylog.Debugf("PingTask", "构建Cisco非交互式命令, 目标IP数量: %d", len(targetIPs))

	var commands []string

	// 只有提供了enable密码才进入特权模式
	if enablePassword != "" {
		commands = append(commands, "enable", enablePassword)
		ylog.Debugf("PingTask", "添加enable命令和密码")
	}

	// 为每个IP创建ping命令
	for _, ip := range targetIPs {
		command := fmt.Sprintf("ping %s  timeout %d", ip, int(timeout.Seconds()))
		ylog.Debugf("PingTask", "添加Cisco ping命令: %s", command)
		commands = append(commands, command)
	}

	ylog.Debugf("PingTask", "Cisco非交互式命令构建完成, 命令总数: %d", len(commands))
	return commands
}

// buildHuaweiCommands 构建华为平台的批量ping命令（非交互式）
func (PingTask) buildHuaweiCommands(targetIPs []string, repeat int, timeout time.Duration) []string {
	ylog.Debugf("PingTask", "构建华为非交互式命令, 目标IP数量: %d", len(targetIPs))

	var commands []string

	// 为每个IP创建ping命令
	for _, ip := range targetIPs {
		command := fmt.Sprintf("ping -c %d -W %d %s", repeat, int(timeout.Seconds()), ip)
		ylog.Debugf("PingTask", "添加华为 ping命令: %s", command)
		commands = append(commands, command)
	}

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

	// 检查是否为批量处理
	var targetIPs []string
	if targetIPRaw, ok := ctx.Params["target_ip"]; ok {
		targetIPs = []string{targetIPRaw.(string)}
		ylog.Debugf("PingTask", "单IP模式, 目标IP: %s", targetIPs[0])
	} else if targetIPsRaw, ok := ctx.Params["target_ips"]; ok {
		targetIPs = targetIPsRaw.([]string)
		ylog.Debugf("PingTask", "批量IP模式, 目标IP数量: %d", len(targetIPs))
	}

	result := Result{
		Data: map[string]interface{}{
			"raw_output": output,
			"batch_mode": len(targetIPs) > 1,
		},
	}

	if len(targetIPs) == 1 {
		// 单IP处理
		ylog.Debugf("PingTask", "开始单IP输出解析")
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
		ylog.Infof("PingTask", "单IP解析完成, 结果: %v", result.Success)
	} else {
		// 批量IP处理
		ylog.Debugf("PingTask", "开始批量IP输出解析")
		batchResults := PingTask{}.parseBatchOutput(output, targetIPs, ctx.Platform)
		result.Data["batch_results"] = batchResults

		// 计算整体成功率
		successCount := 0
		for ip, ipResult := range batchResults {
			if ipResult.Success {
				successCount++
				ylog.Debugf("PingTask", "IP %s ping成功", ip)
			} else {
				ylog.Debugf("PingTask", "IP %s ping失败", ip)
			}
		}
		result.Success = successCount > 0
		result.Data["success_count"] = successCount
		result.Data["total_count"] = len(targetIPs)
		result.Data["success_rate"] = fmt.Sprintf("%.1f%%", float64(successCount)/float64(len(targetIPs))*100)

		ylog.Infof("PingTask", "批量IP解析完成, 成功率: %s (%d/%d)",
			result.Data["success_rate"], successCount, len(targetIPs))
	}

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

		// 查找包丢失率，如 "0% packet loss"
		if strings.Contains(line, "packet loss") {
			if strings.Contains(line, "0% packet loss") {
				result.Data["packet_loss"] = 0
				result.Data["success_rate"] = 100
				result.Data["status"] = "success"
				ylog.Debugf("PingTask", "Huawei parsing: 0%% packet loss, 100%% success rate")
				return true
			} else if strings.Contains(line, "100% packet loss") {
				result.Data["packet_loss"] = 100
				result.Data["success_rate"] = 0
				result.Data["status"] = "failed"
				ylog.Debugf("PingTask", "Huawei parsing: 100%% packet loss, 0%% success rate")
				return false
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

// parseBatchOutput 解析批量ping输出
func (PingTask) parseBatchOutput(output string, targetIPs []string, platform connection.Platform) map[string]Result {
	ylog.Debugf("PingTask", "开始批量输出解析, 目标IP数量: %d", len(targetIPs))
	results := make(map[string]Result)

	// 按行分割输出
	lines := strings.Split(output, "\n")

	// 为每个IP初始化结果
	for _, ip := range targetIPs {
		results[ip] = Result{
			Success: false,
			Data:    map[string]interface{}{"target_ip": ip, "status": "no_result"},
		}
	}

	// 解析输出，查找每个IP的结果
	currentIP := ""
	var currentLines []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// 检查是否是新的ping命令行
		for _, ip := range targetIPs {
			if strings.Contains(line, fmt.Sprintf("ping %s", ip)) ||
				strings.Contains(line, fmt.Sprintf("PING %s", ip)) ||
				(strings.Contains(line, "Sending") && strings.Contains(line, ip) && strings.Contains(line, "ICMP Echos")) {
				// 如果有之前的IP结果，先处理它
				if currentIP != "" {
					ylog.Debugf("PingTask", "处理IP %s 的结果, 输出行数: %d", currentIP, len(currentLines))
					PingTask{}.parseIPResult(currentIP, currentLines, platform, results)
				}
				// 开始新IP的结果收集
				currentIP = ip
				currentLines = []string{line}
				ylog.Debugf("PingTask", "开始收集IP %s 的输出", ip)
				continue
			}
		}

		// 收集当前IP的输出行
		if currentIP != "" {
			currentLines = append(currentLines, line)
		}
	}

	// 处理最后一个IP的结果
	if currentIP != "" {
		ylog.Debugf("PingTask", "处理最后一个IP %s 的结果, 输出行数: %d", currentIP, len(currentLines))
		PingTask{}.parseIPResult(currentIP, currentLines, platform, results)
	}

	ylog.Debugf("PingTask", "批量输出解析完成, 结果数量: %d", len(results))
	return results
}

// parseIPResult 解析单个IP的ping结果
func (PingTask) parseIPResult(ip string, lines []string, platform connection.Platform, results map[string]Result) {
	ylog.Debugf("PingTask", "解析IP %s 的结果, 平台: %s", ip, platform)
	output := strings.Join(lines, "\n")

	result := Result{
		Data: map[string]interface{}{
			"target_ip":  ip,
			"raw_output": output,
		},
	}

	switch platform {
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

	ylog.Debugf("PingTask", "IP %s 解析结果: 成功=%v", ip, result.Success)
	results[ip] = result
}

package task

import (
	"fmt"
	"strings"
	"time"

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
	if _, ok := params["target_ip"]; !ok {
		return fmt.Errorf("missing required parameter: target_ip")
	}
	return nil
}

func (PingTask) BuildCommand(ctx TaskContext) (Command, error) {
	targetIP := ctx.Params["target_ip"].(string)
	repeat := 5
	if r, ok := ctx.Params["repeat"]; ok {
		repeat = r.(int)
	}
	timeout := 2 * time.Second
	if t, ok := ctx.Params["timeout"]; ok {
		timeout = t.(time.Duration)
	}

	var events []*channel.SendInteractiveEvent
	switch ctx.Platform {
	case connection.PlatformCiscoIOSXE:
		events = []*channel.SendInteractiveEvent{
			{
				ChannelInput:    "enable",
				ChannelResponse: "Password:",
				HideInput:       false,
			},
			{
				ChannelInput:    "admin123", // Assume enable password is "admin123"
				ChannelResponse: "#",
				HideInput:       false,
			},
			{
				ChannelInput:    fmt.Sprintf("ping %s repeat %d timeout %d", targetIP, repeat, timeout.Milliseconds()),
				ChannelResponse: "#",
				HideInput:       false,
			},
		}
	case connection.PlatformHuaweiVRP:
		events = []*channel.SendInteractiveEvent{
			{
				ChannelInput:    "system-view",
				ChannelResponse: "Enter system view, return user view with",
				HideInput:       false,
			},
			{
				ChannelInput:    fmt.Sprintf("ping -c %d -W %d %s", repeat, timeout.Milliseconds(), targetIP),
				ChannelResponse: "]",
				HideInput:       false,
			},
			{
				ChannelInput:    "quit",
				ChannelResponse: "",
				HideInput:       false,
			},
		}
	default:
		return Command{}, fmt.Errorf("unsupported platform: %s", ctx.Platform)
	}

	return Command{
		Type:    connection.CommandTypeInteractiveEvent,
		Payload: events,
	}, nil
}

func (PingTask) ParseOutput(ctx TaskContext, raw interface{}) (Result, error) {
	out := raw.(string)
	return Result{
		Success: strings.Contains(out, "0% packet loss") || strings.Contains(out, "100% packet loss"),
		Data:    map[string]interface{}{"raw": out},
	}, nil
}

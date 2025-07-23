package task

import (
	"fmt"
	"strings"

	"github.com/scrapli/scrapligo/channel"
	"github.com/yourusername/zabbix_ddl_monitor/internal/router"
)

type ChangePasswordTask struct{}

func (t *ChangePasswordTask) GenerateCommands(platform string, params map[string]interface{}) ([]*channel.SendInteractiveEvent, error) {
	username, ok := params["username"].(string)
	if !ok {
		return nil, fmt.Errorf("missing username parameter")
	}
	newPass, ok := params["new_password"].(string)
	if !ok {
		return nil, fmt.Errorf("missing new_password parameter")
	}

	switch platform {
	case "huawei_vrp":
		return []*channel.SendInteractiveEvent{
			{
				ChannelInput:    "system-view",
				ChannelResponse: "Enter system view, return user view with",
				HideInput:       false,
			},
			{
				ChannelInput:    "aaa",
				ChannelResponse: "",
				HideInput:       false,
			},
			{
				ChannelInput:    fmt.Sprintf("local-user %s password irreversible-cipher %s", username, newPass),
				ChannelResponse: "the rights of users already online do not change",
				HideInput:       false,
			},
			{
				ChannelInput:    "quit",
				ChannelResponse: "",
				HideInput:       false,
			},
			{
				ChannelInput:    "quit",
				ChannelResponse: "",
				HideInput:       false,
			},
			{
				ChannelInput:    "save",
				ChannelResponse: "[Y/N]",
				HideInput:       false,
			},
			{
				ChannelInput:    "Y",
				ChannelResponse: "Save the configuration successfully",
				HideInput:       false,
			},
		}, nil
	case "cisco_iosxe":
		return []*channel.SendInteractiveEvent{
			{
				ChannelInput:    "enable",
				ChannelResponse: "Password:",
				HideInput:       false,
			},
			{
				ChannelInput:    newPass, // Enable password
				ChannelResponse: "#",
				HideInput:       false,
			},
			{
				ChannelInput:    "configure terminal",
				ChannelResponse: "(config)#",
				HideInput:       false,
			},
			{
				ChannelInput:    fmt.Sprintf("username %s secret %s", username, newPass),
				ChannelResponse: "(config)#",
				HideInput:       false,
			},
			{
				ChannelInput:    "end",
				ChannelResponse: "#",
				HideInput:       false,
			},
			{
				ChannelInput:    "write memory",
				ChannelResponse: "[OK]",
				HideInput:       false,
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported platform: %s", platform)
	}
}

func (t *ChangePasswordTask) Execute(platform string, conn *router.Connection, params map[string]interface{}) (Result, error) {
	// 1. Generate interactive events
	events, err := t.GenerateCommands(platform, params)
	if err != nil {
		return Result{Error: err.Error()}, nil
	}

	// 2. Execute commands
	driver, err := conn.Get()
	if err != nil {
		return Result{Error: fmt.Sprintf("connection failed: %v", err)}, nil
	}

	response, err := driver.SendInteractive(events)
	if err != nil {
		return Result{Error: fmt.Sprintf("command execution failed: %v", err)}, nil
	}

	// 3. Parse output
	success := strings.Contains(response.Result, "success") || strings.Contains(response.Result, "OK")
	return Result{
		Success: success,
		Data:    map[string]interface{}{"output": response.Result},
		Error:   "",
	}, nil
}

func (t *ChangePasswordTask) ParamsSpec() map[string]string {
	return map[string]string{
		"username":     "Target username",
		"new_password": "New password for the user",
	}
}

package task

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/scrapli/scrapligo/channel"
	"github.com/yourusername/zabbix_ddl_monitor/internal/router"
)

type PingTask struct{}

type PingParams struct {
	TargetIP string        `json:"target_ip"`
	Repeat   int           `json:"repeat"`
	Timeout  time.Duration `json:"timeout"`
}

func (t *PingTask) ParamsSpec() map[string]string {
	return map[string]string{
		"target_ip": "string (required) - IP address to ping",
		"repeat":    "int (optional, default=5) - Number of ping packets",
		"timeout":   "duration (optional, default=2s) - Per-packet timeout",
	}
}

func (t *PingTask) GenerateCommands(platform string, params map[string]interface{}) ([]*channel.SendInteractiveEvent, error) {
	var p PingParams
	if err := mapToStruct(params, &p); err != nil {
		return nil, fmt.Errorf("invalid ping params: %v", err)
	}

	switch platform {
	case "cisco_iosxe":
		return []*channel.SendInteractiveEvent{
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
				ChannelInput:    fmt.Sprintf("ping %s repeat %d timeout %d", p.TargetIP, p.Repeat, p.Timeout.Milliseconds()),
				ChannelResponse: "#",
				HideInput:       false,
			},
		}, nil
	case "huawei_vrp":
		return []*channel.SendInteractiveEvent{
			{
				ChannelInput:    "system-view",
				ChannelResponse: "Enter system view, return user view with",
				HideInput:       false,
			},
			{
				ChannelInput:    fmt.Sprintf("ping -c %d -W %d %s", p.Repeat, p.Timeout.Milliseconds(), p.TargetIP),
				ChannelResponse: "]",
				HideInput:       false,
			},
			{
				ChannelInput:    "quit",
				ChannelResponse: "",
				HideInput:       false,
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported platform: %s", platform)
	}
}

func (t *PingTask) Execute(platform string, conn *router.Connection, params map[string]interface{}) (Result, error) {
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
	return t.parseResult(platform, response.Result), nil
}

// parseResult parses the raw output into a structured Result.
func (t *PingTask) parseResult(platform, rawOutput string) Result {
	switch platform {
	case "cisco_iosxe":
		// Match success rate: "Success rate is 80 percent"
		successRe := regexp.MustCompile(`Success rate is (\d+) percent`)
		successMatches := successRe.FindStringSubmatch(rawOutput)
		if len(successMatches) < 2 {
			return Result{
				Success: false,
				Error:   "failed to parse ping success rate",
			}
		}

		// Match latency: "round-trip min/avg/max = 1/2/3 ms"
		latencyRe := regexp.MustCompile(`round-trip min/avg/max = (\d+)/(\d+)/(\d+)`)
		latencyMatches := latencyRe.FindStringSubmatch(rawOutput)

		data := make(map[string]interface{})
		if len(latencyMatches) >= 4 {
			minLatency, _ := strconv.Atoi(latencyMatches[1])
			avgLatency, _ := strconv.Atoi(latencyMatches[2])
			maxLatency, _ := strconv.Atoi(latencyMatches[3])
			data["latency_ms"] = map[string]interface{}{
				"min": minLatency,
				"avg": avgLatency,
				"max": maxLatency,
			}
		}

		successRate, _ := strconv.Atoi(successMatches[1])
		return Result{
			Success: successRate > 0,
			Data:    data,
		}

	case "huawei_vrp":
		// Format: "5 packet(s) transmitted, 5 received, 0% packet loss"
		re := regexp.MustCompile(`(\d+) packet\(s\) transmitted, (\d+) received`)
		matches := re.FindStringSubmatch(rawOutput)
		if len(matches) < 3 {
			return Result{
				Success: false,
				Error:   "failed to parse ping result",
			}
		}

		received, _ := strconv.Atoi(matches[2])
		return Result{
			Success: received > 0,
			Data:    map[string]interface{}{"packets_received": received},
		}

	default:
		return Result{
			Success: false,
			Error:   fmt.Sprintf("unsupported platform: %s", platform),
		}
	}
}

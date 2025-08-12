package task

import (
	"fmt"
	"strings"
	"time"

	"github.com/charlesren/zabbix_ddl_monitor/connection"
)

type CiscoIOSXEAdapter struct{}

func (a *CiscoIOSXEAdapter) NormalizeParams(params map[string]interface{}) map[string]interface{} {
	if _, ok := params["timeout"]; !ok {
		params["timeout"] = 2 * time.Second // 默认超时时间
	}
	if _, ok := params["repeat"]; !ok {
		params["repeat"] = 5 // 默认ping次数
	}
	return params
}

func (a *CiscoIOSXEAdapter) ConvertOutput(raw string) map[string]interface{} {
	result := map[string]interface{}{
		"raw_output": raw,
		"parsed_by":  "cisco_iosxe_adapter",
	}

	// Cisco IOS-XE特有的输出解析
	if strings.Contains(raw, "Success rate is") {
		if strings.Contains(raw, "100 percent") {
			result["success"] = true
			result["success_rate"] = "100%"
		} else if strings.Contains(raw, "0 percent") {
			result["success"] = false
			result["success_rate"] = "0%"
		} else {
			result["success"] = true // 部分成功
		}
	} else {
		result["success"] = !strings.Contains(strings.ToLower(raw), "failed")
	}

	return result
}

func (a *CiscoIOSXEAdapter) GetCommandTemplate(taskType TaskType, commandType connection.CommandType) (string, error) {
	switch taskType {
	case "ping":
		return "ping {{.target_ip}} repeat {{.repeat}} timeout {{.timeout_seconds}}", nil
	default:
		return "", fmt.Errorf("unsupported task type for Cisco IOS-XE: %s", taskType)
	}
}

func (a *CiscoIOSXEAdapter) ValidatePlatformParams(params map[string]interface{}) error {
	// Cisco IOS-XE特定参数验证
	if repeat, ok := params["repeat"]; ok {
		if r, ok := repeat.(int); ok && (r < 1 || r > 2000) {
			return fmt.Errorf("Cisco IOS-XE: repeat count must be between 1 and 2000")
		}
	}

	if timeout, ok := params["timeout"]; ok {
		if t, ok := timeout.(time.Duration); ok && (t < time.Second || t > 36*time.Second) {
			return fmt.Errorf("Cisco IOS-XE: timeout must be between 1 and 36 seconds")
		}
	}

	return nil
}

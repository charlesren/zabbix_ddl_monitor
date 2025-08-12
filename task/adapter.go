package task

import (
	"fmt"
	"strings"
	"time"

	"github.com/charlesren/zabbix_ddl_monitor/connection"
)

// PlatformAdapter 平台适配器接口
type PlatformAdapter interface {
	// 标准化参数，根据平台特性调整参数
	NormalizeParams(params map[string]interface{}) map[string]interface{}
	// 转换原始输出为结构化数据
	ConvertOutput(raw string) map[string]interface{}
	// 获取平台特定的命令模板
	GetCommandTemplate(taskType TaskType, commandType connection.CommandType) (string, error)
	// 验证平台特定的参数
	ValidatePlatformParams(params map[string]interface{}) error
}

// 全局适配器注册表
var adapters = map[connection.Platform]PlatformAdapter{
	connection.PlatformCiscoIOSXE: &CiscoIOSXEAdapter{},
	connection.PlatformCiscoIOSXR: &CiscoIOSXRAdapter{},
	connection.PlatformCiscoNXOS:  &CiscoNXOSAdapter{},
	connection.PlatformHuaweiVRP:  &HuaweiVRPAdapter{},
	connection.PlatformH3CComware: &H3CComwareAdapter{},
}

// GetAdapter 获取指定平台的适配器
func GetAdapter(platform connection.Platform) PlatformAdapter {
	if adapter, ok := adapters[platform]; ok {
		return adapter
	}
	return &GenericAdapter{} // 返回通用适配器作为fallback
}

// RegisterAdapter 注册新的平台适配器
func RegisterAdapter(platform connection.Platform, adapter PlatformAdapter) {
	adapters[platform] = adapter
}

// GenericAdapter 通用适配器实现
type GenericAdapter struct{}

func (a *GenericAdapter) NormalizeParams(params map[string]interface{}) map[string]interface{} {
	// 设置通用默认值
	if _, ok := params["repeat"]; !ok {
		params["repeat"] = 5
	}
	if _, ok := params["timeout"]; !ok {
		params["timeout"] = 2 * time.Second
	}
	return params
}

func (a *GenericAdapter) ConvertOutput(raw string) map[string]interface{} {
	return map[string]interface{}{
		"raw_output": raw,
		"success":    !strings.Contains(strings.ToLower(raw), "failed"),
		"parsed_by":  "generic_adapter",
	}
}

func (a *GenericAdapter) GetCommandTemplate(taskType TaskType, commandType connection.CommandType) (string, error) {
	switch taskType {
	case "ping":
		return "ping {{.target_ip}}", nil
	default:
		return "", fmt.Errorf("unsupported task type: %s", taskType)
	}
}

func (a *GenericAdapter) ValidatePlatformParams(params map[string]interface{}) error {
	return nil // 通用适配器不做额外验证
}

// HuaweiVRPAdapter 华为VRP平台适配器
type HuaweiVRPAdapter struct{}

func (a *HuaweiVRPAdapter) NormalizeParams(params map[string]interface{}) map[string]interface{} {
	// 华为设备默认参数
	if _, ok := params["repeat"]; !ok {
		params["repeat"] = 4 // 华为默认ping次数
	}
	if _, ok := params["timeout"]; !ok {
		params["timeout"] = 1 * time.Second // 华为默认超时较短
	}
	return params
}

func (a *HuaweiVRPAdapter) ConvertOutput(raw string) map[string]interface{} {
	result := map[string]interface{}{
		"raw_output": raw,
		"parsed_by":  "huawei_vrp_adapter",
	}

	// 华为特有的输出解析
	if strings.Contains(raw, "packet loss") {
		if strings.Contains(raw, "0% packet loss") {
			result["success"] = true
			result["packet_loss"] = "0%"
		} else if strings.Contains(raw, "100% packet loss") {
			result["success"] = false
			result["packet_loss"] = "100%"
		}
	}

	return result
}

func (a *HuaweiVRPAdapter) GetCommandTemplate(taskType TaskType, commandType connection.CommandType) (string, error) {
	switch taskType {
	case "ping":
		return "ping -c {{.repeat}} -W {{.timeout_seconds}} {{.target_ip}}", nil
	default:
		return "", fmt.Errorf("unsupported task type for Huawei VRP: %s", taskType)
	}
}

func (a *HuaweiVRPAdapter) ValidatePlatformParams(params map[string]interface{}) error {
	// 华为特定参数验证
	if repeat, ok := params["repeat"]; ok {
		if r, ok := repeat.(int); ok && (r < 1 || r > 20) {
			return fmt.Errorf("Huawei VRP: repeat count must be between 1 and 20")
		}
	}
	return nil
}

// CiscoIOSXRAdapter Cisco IOS-XR适配器
type CiscoIOSXRAdapter struct{}

func (a *CiscoIOSXRAdapter) NormalizeParams(params map[string]interface{}) map[string]interface{} {
	if _, ok := params["timeout"]; !ok {
		params["timeout"] = 3 * time.Second // IOS-XR默认超时稍长
	}
	return params
}

func (a *CiscoIOSXRAdapter) ConvertOutput(raw string) map[string]interface{} {
	result := map[string]interface{}{
		"raw_output": raw,
		"parsed_by":  "cisco_iosxr_adapter",
	}

	// IOS-XR特有的输出解析
	if strings.Contains(raw, "Success rate is") {
		if strings.Contains(raw, "100 percent") {
			result["success"] = true
			result["success_rate"] = "100%"
		} else {
			result["success"] = false
		}
	}

	return result
}

func (a *CiscoIOSXRAdapter) GetCommandTemplate(taskType TaskType, commandType connection.CommandType) (string, error) {
	switch taskType {
	case "ping":
		return "ping {{.target_ip}} repeat {{.repeat}} timeout {{.timeout_seconds}}", nil
	default:
		return "", fmt.Errorf("unsupported task type for Cisco IOS-XR: %s", taskType)
	}
}

func (a *CiscoIOSXRAdapter) ValidatePlatformParams(params map[string]interface{}) error {
	return nil
}

// CiscoNXOSAdapter Cisco NX-OS适配器
type CiscoNXOSAdapter struct{}

func (a *CiscoNXOSAdapter) NormalizeParams(params map[string]interface{}) map[string]interface{} {
	if _, ok := params["repeat"]; !ok {
		params["repeat"] = 5
	}
	return params
}

func (a *CiscoNXOSAdapter) ConvertOutput(raw string) map[string]interface{} {
	return map[string]interface{}{
		"raw_output": raw,
		"success":    strings.Contains(raw, "100.00% success"),
		"parsed_by":  "cisco_nxos_adapter",
	}
}

func (a *CiscoNXOSAdapter) GetCommandTemplate(taskType TaskType, commandType connection.CommandType) (string, error) {
	switch taskType {
	case "ping":
		return "ping {{.target_ip}} count {{.repeat}}", nil
	default:
		return "", fmt.Errorf("unsupported task type for Cisco NX-OS: %s", taskType)
	}
}

func (a *CiscoNXOSAdapter) ValidatePlatformParams(params map[string]interface{}) error {
	return nil
}

// H3CComwareAdapter H3C Comware适配器
type H3CComwareAdapter struct{}

func (a *H3CComwareAdapter) NormalizeParams(params map[string]interface{}) map[string]interface{} {
	if _, ok := params["repeat"]; !ok {
		params["repeat"] = 5
	}
	return params
}

func (a *H3CComwareAdapter) ConvertOutput(raw string) map[string]interface{} {
	result := map[string]interface{}{
		"raw_output": raw,
		"parsed_by":  "h3c_comware_adapter",
	}

	// H3C特有的输出解析
	if strings.Contains(raw, "packet loss") {
		result["success"] = strings.Contains(raw, "0% packet loss")
	} else {
		result["success"] = !strings.Contains(strings.ToLower(raw), "failed")
	}

	return result
}

func (a *H3CComwareAdapter) GetCommandTemplate(taskType TaskType, commandType connection.CommandType) (string, error) {
	switch taskType {
	case "ping":
		return "ping -c {{.repeat}} {{.target_ip}}", nil
	default:
		return "", fmt.Errorf("unsupported task type for H3C Comware: %s", taskType)
	}
}

func (a *H3CComwareAdapter) ValidatePlatformParams(params map[string]interface{}) error {
	return nil
}

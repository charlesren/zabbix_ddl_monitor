package task

import "strings"

type CiscoIOSXEAdapter struct{}

func (a *CiscoIOSXEAdapter) NormalizeParams(params map[string]interface{}) map[string]interface{} {
	if _, ok := params["timeout"]; !ok {
		params["timeout"] = 2000 // 默认毫秒
	}
	return params
}

func (a *CiscoIOSXEAdapter) ConvertOutput(raw string) map[string]interface{} {
	return map[string]interface{}{
		"raw":     raw,
		"success": !strings.Contains(raw, "Failed"),
	}
}

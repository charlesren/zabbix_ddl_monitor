type PlatformAdapter interface {
	NormalizeParams(params map[string]interface{}) map[string]interface{}
	ConvertOutput(raw string) map[string]interface{}
}

var adapters = map[string]PlatformAdapter{
	"cisco_iosxe": &CiscoAdapter{},
	"huawei_vrp":  &HuaweiAdapter{},
}

// 示例：华为平台适配器
type HuaweiAdapter struct{}

func (a *HuaweiAdapter) NormalizeParams(params map[string]interface{}) map[string]interface{} {
	if _, ok := params["repeat"]; !ok {
		params["repeat"] = 4 // 华为默认ping次数
	}
	return params
}

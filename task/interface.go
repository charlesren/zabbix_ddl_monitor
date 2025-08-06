package task

import (
	"encoding/json"

	"github.com/scrapli/scrapligo/channel"
)

type Result struct {
	Success bool                   `json:"success"`
	Data    map[string]interface{} `json:"data"`
	Error   string                 `json:"error,omitempty"`
}

type ParamSpec struct {
	Name     string                  `json:"name"`
	Type     string                  `json:"type"`
	Required bool                    `json:"required"`
	Default  interface{}             `json:"default"`
	Validate func(interface{}) error `json:"-"`
}

type ProtocolCapability struct {
	Protocol     string   // "ssh"或"scrapli"
	CommandTypes []string // ["commands", "interactive_event"]
}

type PlatformSupport struct {
	Platform string               // "cisco_iosxe"
	Params   map[string]ParamSpec // 平台特有参数规范
}

type TaskMeta struct {
	Name            string
	ProtocolSupport []ProtocolCapability
	Platforms       []PlatformSupport
}

type Task interface {
	// 元信息
	Meta() TaskMeta

	// 命令生成（动态适配协议和平台）
	Generate(protocolType string, commandType string, platform string, params map[string]interface{}) (interface{}, error)

	// 结果解析
	ParseOutput(protocolType string, commandType string, platform string, rawOutput interface{}) (Result, error)
}

type SimpleTask interface {
	Task
	GenerateCommands(params map[string]interface{}) ([]string, error)
}

type InteractiveTask interface {
	Task
	GenerateInteractiveEvents(params map[string]interface{}) ([]*channel.SendInteractiveEvent, error)
}

type BatchTask interface {
	Task
	GenerateBatchCommands(params []map[string]interface{}) ([]string, error)
	ParseBatchOutput(output string) []Result
}

func mapToStruct(m map[string]interface{}, out interface{}) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

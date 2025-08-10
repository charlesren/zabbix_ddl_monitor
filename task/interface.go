package task

import (
	"context"
	"encoding/json"

	"github.com/charlesren/zabbix_ddl_monitor/connection"
	"github.com/scrapli/scrapligo/channel"
)

type (
	TaskType    string
	Platform    = connection.Platform
	Protocol    = connection.Protocol
	CommandType = connection.CommandType
)

type Command struct {
	Type    CommandType
	Payload interface{} // []string 或 []*channel.SendInteractiveEvent
}

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

type TaskMeta struct {
	Type        TaskType          // 任务类型（如 "ping"）
	Description string            // 任务描述
	Platforms   []PlatformSupport // 支持的平台列表
}

type PlatformSupport struct {
	Platform  Platform          // 平台名称（如 "cisco_iosxe"）
	Protocols []ProtocolSupport // 支持的协议列表
}

type ProtocolSupport struct {
	Protocol     Protocol             // 协议类型（如 "ssh"）
	CommandTypes []CommandTypeSupport // 支持的命令类型列表
}

type CommandTypeSupport struct {
	CommandType CommandType // 命令类型（如 "commands"）
	ImplFactory func() Task // 任务实现的工厂方法
	Params      []ParamSpec // 参数规范
}

// TaskContext 封装任务执行的上下文信息
type TaskContext struct {
	TaskType TaskType // 任务类型（如 "PingTask"）
	Platform Platform // 平台类型（cisco_iosxe, huawei_vrp）
	Protocol Protocol // 协议类型（ssh, scrapli）
	// 指定命令类型（commands, interactive_event）
	// 有则按指定类型生成命令
	// 没有则按当前实现生成命令
	// 有多种实现按优先级高的生成命令（interactive_event > commands）
	CommandType CommandType
	Params      map[string]interface{} // 任务参数
	Ctx         context.Context
}

func (tc TaskContext) WithContext(ctx context.Context) TaskContext {
	tc.Ctx = ctx
	return tc
}

type Task interface {
	// 元信息
	Meta() TaskMeta

	ValidateParams(params map[string]interface{}) error
	BuildCommand(tct TaskContext) (Command, error)
	// 执行任务前检查,改为平台内置函数，不要求用户实现
	//ValidateParams() error // 参数校验
	// 返回结果，由用户自行解析
	ParseOutput(tct TaskContext, raw interface{}) (Result, error)
}

type SshCommandsTask interface {
	Task
	GenerateCommands(params map[string]interface{}) ([]string, error)
}

type ScrapliCommandsTask interface {
	Task
	GenerateCommands(params map[string]interface{}) ([]string, error)
}

type ScrapliInteractiveTask interface {
	Task
	GenerateInteractiveEvents(params map[string]interface{}) ([]*channel.SendInteractiveEvent, error)
}

func mapToStruct(m map[string]interface{}, out interface{}) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

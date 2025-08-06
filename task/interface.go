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

type Task interface {
	ParamsSpec() []ParamSpec
	SupportsProtocol(protocol string) bool
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

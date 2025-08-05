package task

import (
	"encoding/json"

	"github.com/charlesren/zabbix_ddl_monitor/connection"
	"github.com/scrapli/scrapligo/channel"
)

// Result represents the structured output of a task execution
type Result struct {
	Success bool                   `json:"success"`
	Data    map[string]interface{} `json:"data"` // Task-specific data
	Error   string                 `json:"error,omitempty"`
}

// Task defines the interface for platform-specific operations
type Task interface {
	// GenerateCommands generates platform-specific interactive events
	GenerateCommands(platform string, params map[string]interface{}) ([]*channel.SendInteractiveEvent, error)

	// Execute runs the task and returns a structured result
	Execute(platform string, conn *connection.Connection, params map[string]interface{}) (Result, error)

	// ParamsSpec describes required parameters
	ParamsSpec() map[string]string
}

// Helper function
func mapToStruct(m map[string]interface{}, out interface{}) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

// BatchTask 批量任务接口（需任务实现）
type BatchTask interface {
	Task
	// 新增批量方法
	GenerateBatchCommands(platform string, params []map[string]interface{}) ([]string, error)
	ParseBatchOutput(platform, output string) []Result
}

// ExecuteBatch 默认批量实现（可被具体任务覆盖）
func (t *BaseTask) ExecuteBatch(
	conn connection.ProtocolDriver,
	platform string,
	paramsList []map[string]interface{},
) []Result {
	var results []Result
	for _, params := range paramsList {
		commands, err := t.GenerateCommands(platform, params)
		if err != nil {
			results = append(results, Result{Error: err.Error()})
			continue
		}

		output, err := conn.SendCommands(commands)
		if err != nil {
			results = append(results, Result{Error: err.Error()})
			continue
		}

		results = append(results, t.ParseOutput(platform, output))
	}
	return results
}

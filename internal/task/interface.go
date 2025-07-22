package task

import (
	"encoding/json"

	"github.com/scrapli/scrapligo/channel"
	"github.com/yourusername/zabbix_ddl_monitor/internal/router"
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
	Execute(platform string, conn *router.Connection, params map[string]interface{}) (Result, error)

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

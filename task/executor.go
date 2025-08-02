package task

import (
	"fmt"
	"time"
)

// TaskExecutor executes platform-specific tasks on network devices
type TaskExecutor struct {
	task     Task
	platform string
	conn     *router.Connection
}

// NewTaskExecutor creates a new executor instance
func NewTaskExecutor(t Task, platform string, conn *router.Connection) *TaskExecutor {
	return &TaskExecutor{
		task:     t,
		platform: platform,
		conn:     conn,
	}
}

// Run executes the task with provided parameters
func (e *TaskExecutor) Run(params map[string]interface{}) (Result, error) {
	// 1. Generate platform-specific commands
	commands, err := e.task.Commands(e.platform, params)
	if err != nil {
		return Result{}, fmt.Errorf("command generation failed: %v", err)
	}

	// 2. Get device connection
	driver, err := e.conn.Get()
	if err != nil {
		return Result{}, fmt.Errorf("connection failed: %v", err)
	}

	// 3. Execute command sequence
	var lastOutput string
	for _, cmd := range commands {
		resp, err := driver.SendCommand(cmd)
		if err != nil {
			return Result{}, fmt.Errorf("command execution failed: %s (err: %v)", cmd, err)
		}
		lastOutput = resp.Result
	}

	// 4. Parse and return results
	return e.task.Parse(e.platform, lastOutput)
}

// WithTimeout creates a timed execution wrapper
func (e *TaskExecutor) WithTimeout(timeout time.Duration) *TimedExecutor {
	return &TimedExecutor{
		executor: e,
		timeout:  timeout,
	}
}

// TimedExecutor wraps TaskExecutor with timeout control
type TimedExecutor struct {
	executor *TaskExecutor
	timeout  time.Duration
}

// Run executes with timeout control
func (t *TimedExecutor) Run(params map[string]interface{}) (Result, error) {
	// Implementation would use context.WithTimeout
	// Omitted for brevity
	return t.executor.Run(params)
}

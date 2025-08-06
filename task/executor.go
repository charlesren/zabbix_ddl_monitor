package task

import (
	"errors"

	"github.com/scrapli/scrapligo/channel"
)

var (
	ErrInputMismatch = errors.New("input type does not match driver capability")
)

type TaskExecutor struct {
	task  Task
	input interface{}
}

func (e *TaskExecutor) Run(driver interface {
	SendCommands([]string) (string, error)
	SendInteractive([]*channel.SendInteractiveEvent) (string, error)
}) (Result, error) {
	switch v := e.input.(type) {
	case []string:
		out, err := driver.SendCommands(v)
		return Result{Success: err == nil, Data: map[string]interface{}{"output": out}}, err
	case []*channel.SendInteractiveEvent:
		out, err := driver.SendInteractive(v)
		return Result{Success: err == nil, Data: map[string]interface{}{"output": out}}, err
	default:
		return Result{}, ErrInputMismatch
	}
}

func (e *TaskExecutor) Close() error {
	if c, ok := e.task.(interface{ Close() error }); ok {
		return c.Close()
	}
	return nil
}

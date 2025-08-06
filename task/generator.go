package task

import "errors"

var (
	ErrProtocolNotSupported = errors.New("task does not support this protocol")
)

type TaskGenerator struct {
	factory TaskFactory
}

func NewTaskGenerator(f TaskFactory) *TaskGenerator {
	return &TaskGenerator{factory: f}
}

func (g *TaskGenerator) Build(taskType string, params map[string]interface{}, driver *EnhancedDriver) (*TaskExecutor, error) {
	task := g.factory.Create(taskType)
	if !task.SupportsProtocol(driver.ProtocolType()) {
		return nil, ErrProtocolNotSupported
	}

	var input interface{}
	var err error

	switch driver.ProtocolType() {
	case "scrapli":
		if t, ok := task.(InteractiveTask); ok {
			input, err = t.GenerateInteractiveEvents(params)
		}
	default:
		if t, ok := task.(SimpleTask); ok {
			input, err = t.GenerateCommands(params)
		}
	}

	if err != nil {
		return nil, err
	}

	return &TaskExecutor{task: task, input: input}, nil
}

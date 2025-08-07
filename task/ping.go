package task

import "log"

type PingTask struct{}

func (t *PingTask) Meta() TaskMeta {
	return TaskMeta{
		Name: "ping",
		ProtocolSupport: []ProtocolCapability{
			{Protocol: "ssh", CommandTypes: []string{"commands"}},
			{Protocol: "scrapli", CommandTypes: []string{"interactive_event"}},
		},
		Platforms: []PlatformSupport{
			{Platform: "cisco_iosxe", Params: map[string]ParamSpec{
				"ips": {Name: "ips", Type: "[]string", Required: true},
			}},
		},
	}
}

func (t *PingTask) Execute(ctx TaskContext) (Result, error) {
	switch ctx.CommandType {
	case "commands":
		commands, _ := t.GenerateCommands(ctx)
		// 执行命令并返回结果
	case "interactive_event":
		events, _ := t.GenerateInteractiveEvents(ctx)
		// 执行交互事件并返回结果
	}
	// 返回统一结果
}

func init() {
	registry := GetTaskRegistry()
	meta := TaskMeta{
		Name: "ping",
		Platforms: []PlatformSupport{
			{
				Platform: "cisco_iosxe",
				Protocols: []ProtocolSupport{
					{
						Protocol: "ssh",
						CommandTypes: []CommandTypeSupport{
							{
								CommandType: "commands",
								ImplFactory: func() Task { return &PingTask{} },
								Params: map[string]ParamSpec{
									"ips": {Name: "ips", Type: "[]string", Required: true},
								},
							},
						},
					},
				},
			},
		},
	}
	if err := registry.Register(meta); err != nil {
		log.Fatalf("Failed to register ping task: %v", err)
	}
}

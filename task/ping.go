type PingTask struct{}

func (t *PingTask) Meta() TaskMeta {
	return TaskMeta{
		Name: "ping",
		ProtocolSupport: []ProtocolCapability{
			{
				Protocol:   "ssh",
				InputTypes: []string{"commands"},
			},
			{
				Protocol:   "scrapli",
				InputTypes: []string{"interactive"},
			},
		},
		Platforms: []PlatformSupport{
			{
				Platform: "cisco_iosxe",
				Params: map[string]ParamSpec{
					"timeout": {Type: "duration", Default: 2 * time.Second},
					"repeat":  {Type: "int", Default: 5},
				},
			},
			{
				Platform: "huawei_vrp",
				Params: map[string]ParamSpec{
					"timeout": {Type: "duration", Default: 3 * time.Second},
				},
			},
		},
	}
}

func (t *PingTask) Generate(inputType, platform string, params map[string]interface{}) (interface{}, error) {
	// 参数校验
	if err := validateParams(params, t.Meta(), platform); err != nil {
		return nil, err
	}

	// 生成命令
	switch inputType {
	case "commands":
		return t.generateCommands(platform, params)
	case "interactive":
		return t.generateInteractive(platform, params)
	default:
		return nil, ErrUnsupportedInputType
	}
}

func (t *PingTask) generateCommands(platform string, params map[string]interface{}) ([]string, error) {
	switch platform {
	case "cisco_iosxe":
		return []string{fmt.Sprintf("ping %s repeat %d", params["target_ip"], params["repeat"])}, nil
	case "huawei_vrp":
		return []string{fmt.Sprintf("ping -c %d %s", params["repeat"], params["target_ip"])}, nil
	default:
		return nil, ErrUnsupportedPlatform
	}
}

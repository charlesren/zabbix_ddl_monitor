package task

type PingTask struct{}

func (t *PingTask) Prepare(platform string) string {
	switch platform {
	case "cisco_iosxe":
		return "ping ip_address repeat 5"
	case "huawei_vrp":
		return "ping -c 5 ip_address"
	default:
		return "ping ip_address"
	}
}

func (t *PingTask) Parse(output string) (interface{}, error) {
	// 解析ping结果
	return nil, nil
}

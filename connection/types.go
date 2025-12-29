package connection

type (
	Platform        string
	Protocol        string
	CommandType     string
	RebuildStrategy string
)

const (
	PlatformCiscoIOSXE Platform = "cisco_iosxe"
	PlatformCiscoIOSXR Platform = "cisco_iosxr"
	PlatformCiscoNXOS  Platform = "cisco_nxos"
	PlatformH3CComware Platform = "h3c_comware"
	PlatformHuaweiVRP  Platform = "huawei_vrp"

	ProtocolSSH                 Protocol    = "ssh"
	ProtocolScrapli             Protocol    = "scrapli"
	CommandTypeCommands         CommandType = "commands"
	CommandTypeInteractiveEvent CommandType = "interactive_event"

	// RebuildStrategyUsage 基于使用次数的重建策略
	// 当连接使用次数达到 RebuildMaxUsageCount 时触发重建
	RebuildStrategyUsage RebuildStrategy = "usage"

	// RebuildStrategyAge 基于连接年龄的重建策略
	// 当连接存活时间达到 RebuildMaxAge 时触发重建
	RebuildStrategyAge RebuildStrategy = "age"

	// RebuildStrategyError 基于错误率的重建策略
	// 当连接错误率达到 RebuildMaxErrorRate 时触发重建
	RebuildStrategyError RebuildStrategy = "error"

	// RebuildStrategyAll 满足所有条件的重建策略
	// 当使用次数、年龄、错误率三个条件同时满足时触发重建
	RebuildStrategyAll RebuildStrategy = "all"

	// RebuildStrategyAny 满足任意条件的重建策略（默认）
	// 当使用次数、年龄、错误率任意一个条件满足时触发重建
	RebuildStrategyAny RebuildStrategy = "any"
)

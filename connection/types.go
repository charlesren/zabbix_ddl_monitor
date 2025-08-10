package connection

type (
	Platform    string
	Protocol    string
	CommandType string
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
)

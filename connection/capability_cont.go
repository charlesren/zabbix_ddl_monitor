package connection

import "time"

// connection/capability_const.go
var (
	// SSH协议能力
	SSHCapability = ProtocolCapability{
		Protocol:        ProtocolSSH,
		PlatformSupport: []Platform{CiscoIOSXE, HuaweiVRP},
		CommandTypes: []CommandTypeSupport{
			{
				Type:        TypeCommands,
				Description: "Basic command execution",
				Example:     "show version",
			},
		},
		ConfigModes: []ConfigModeCapability{
			{
				Mode:          ConfigModeBasic,
				Privilege:     PrivilegeLevelUser,
				EnterCommands: []string{"configure terminal"},
				ExitCommands:  []string{"end"},
			},
		},
		MaxConcurrent: 5,
		Timeout:       30 * time.Second,
	}

	// Scrapli协议能力
	ScrapliCapability = ProtocolCapability{
		Protocol:        ProtocolScrapli,
		PlatformSupport: []Platform{CiscoIOSXE, CiscoNXOS, HuaweiVRP},
		CommandTypes: []CommandTypeSupport{
			{
				Type:        TypeCommands,
				Description: "Batch command execution",
				Example:     "show running-config",
			},
			{
				Type:        TypeInteractiveEvent,
				Description: "Interactive session",
				Example:     "dialog-based configuration",
			},
		},
		ConfigModes: []ConfigModeCapability{
			{
				Mode:          ConfigModeBasic,
				Privilege:     PrivilegeLevelUser,
				EnterCommands: []string{"config term"},
			},
			{
				Mode:          ConfigModePrivileged,
				Privilege:     PrivilegeLevelAdmin,
				EnterCommands: []string{"enable"},
			},
		},
		SupportsAutoComplete: true,
		MaxConcurrent:        10,
	}
)

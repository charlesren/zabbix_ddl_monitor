package connection

// connection/util.go
func (pc ProtocolCapability) SupportsCommandType(cmdType CommandType) bool {
	for _, ct := range pc.CommandTypesSupport {
		if ct == cmdType {
			return true
		}
	}
	return false
}

func (pc ProtocolCapability) GetConfigMode(mode ConfigMode) (ConfigModeCapability, bool) {
	for _, m := range pc.ConfigModes {
		if m.Mode == mode {
			return m, true
		}
	}
	return ConfigModeCapability{}, false
}

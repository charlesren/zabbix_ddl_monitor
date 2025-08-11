package connection

import "time"

// connection/capability.go
type ProtocolCapability struct {
	APIVersion    string // 能力定义版本
	MinAPIVersion string // 兼容的最低版本
	// 基础能力
	Protocol        Protocol   // 协议类型标识
	PlatformSupport []Platform // 支持的平台列表

	// 命令能力
	CommandTypesSupport []CommandType

	// 配置能力
	ConfigModes []ConfigModeCapability

	// 性能参数
	MaxConcurrent int           // 最大并发连接数
	Timeout       time.Duration // 默认超时时间

	// 高级功能
	SupportsAutoComplete bool // 是否支持命令自动补全
	SupportsColorOutput  bool // 是否支持彩色输出
}

// connection/capability.go
type ConfigLevel string

const (
	BasicConfig      ConfigLevel = "basic"
	PrivilegedConfig ConfigLevel = "privileged"
)

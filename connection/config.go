package connection

// 配置模式类型（设备配置层级）
type ConfigMode string

const (
	ConfigModePrivileged ConfigMode = "privileged" // 特权模式(如Cisco的"#")
	ConfigModeBasic      ConfigMode = "basic"      // 基础配置模式
	ConfigModeInterface  ConfigMode = "interface"  // 接口配置模式
	ConfigModeRouteMap   ConfigMode = "route-map"  // 路由策略模式
	ConfigModeVLAN       ConfigMode = "vlan"       // VLAN配置模式
	ConfigModeGlobal     ConfigMode = "global"     // 全局配置模式
)

// 权限级别（类似Unix权限概念）
type ConfigPrivilegeLevel int

const (
	PrivilegeLevelUser  ConfigPrivilegeLevel = iota // 0: 用户模式(如Cisco的">")
	PrivilegeLevelAdmin                             // 1: 特权模式(如Cisco的"#")
	PrivilegeLevelRoot                              // 2: 超级用户(如Juniper的root)
)

// 完整配置能力结构
type ConfigModeCapability struct {
	Mode              ConfigMode           // 模式类型
	Privilege         ConfigPrivilegeLevel // 所需权限级别
	EnterCommands     []string             // 进入模式的命令序列
	ExitCommands      []string             // 退出模式的命令序列
	ValidationPattern string               // 检测是否成功进入的正则表达式
	FeatureFlags      []ConfigFeature      // 支持的子功能
}

// 配置模式支持的子功能
type ConfigFeature struct {
	Name        string
	Description string
	Commands    []string // 相关命令前缀
}

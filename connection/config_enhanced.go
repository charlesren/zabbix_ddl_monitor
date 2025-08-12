package connection

import (
	"fmt"
	"time"
)

// 增强的配置结构
type EnhancedConnectionConfig struct {
	// 基础连接信息
	Host     string `json:"host" yaml:"host" validate:"required,ip_addr|hostname"`
	Port     int    `json:"port" yaml:"port" validate:"min=1,max=65535"`
	Username string `json:"username" yaml:"username" validate:"required"`
	Password string `json:"password" yaml:"password" validate:"required"`

	// 协议配置
	Protocol Protocol `json:"protocol" yaml:"protocol" validate:"required,oneof=ssh scrapli"`
	Platform Platform `json:"platform" yaml:"platform" validate:"required"`

	// 超时配置
	ConnectTimeout time.Duration `json:"connect_timeout" yaml:"connect_timeout"`
	ReadTimeout    time.Duration `json:"read_timeout" yaml:"read_timeout"`
	WriteTimeout   time.Duration `json:"write_timeout" yaml:"write_timeout"`
	IdleTimeout    time.Duration `json:"idle_timeout" yaml:"idle_timeout"`

	// 重试配置
	MaxRetries    int           `json:"max_retries" yaml:"max_retries"`
	RetryInterval time.Duration `json:"retry_interval" yaml:"retry_interval"`
	BackoffFactor float64       `json:"backoff_factor" yaml:"backoff_factor"`

	// 连接池配置
	MaxConnections  int           `json:"max_connections" yaml:"max_connections"`
	MinConnections  int           `json:"min_connections" yaml:"min_connections"`
	MaxIdleTime     time.Duration `json:"max_idle_time" yaml:"max_idle_time"`
	HealthCheckTime time.Duration `json:"health_check_time" yaml:"health_check_time"`

	// SSH特定配置
	SSHConfig *SSHConfig `json:"ssh_config,omitempty" yaml:"ssh_config,omitempty"`

	// Scrapli特定配置
	ScrapliConfig *ScrapliConfig `json:"scrapli_config,omitempty" yaml:"scrapli_config,omitempty"`

	// 安全配置
	SecurityConfig *SecurityConfig `json:"security_config,omitempty" yaml:"security_config,omitempty"`

	// 扩展配置
	Extensions map[string]interface{} `json:"extensions,omitempty" yaml:"extensions,omitempty"`

	// 标签和元数据
	Labels   map[string]string      `json:"labels,omitempty" yaml:"labels,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// SSH特定配置
type SSHConfig struct {
	// 认证方式
	AuthMethod         string `json:"auth_method" yaml:"auth_method"` // password, publickey, keyboard-interactive
	PrivateKeyPath     string `json:"private_key_path" yaml:"private_key_path"`
	PrivateKeyPassword string `json:"private_key_password" yaml:"private_key_password"`
	KnownHostsFile     string `json:"known_hosts_file" yaml:"known_hosts_file"`
	HostKeyCallback    string `json:"host_key_callback" yaml:"host_key_callback"` // strict, ignore, custom

	// SSH特定参数
	CompressionLevel int      `json:"compression_level" yaml:"compression_level"`
	Ciphers          []string `json:"ciphers" yaml:"ciphers"`
	MACs             []string `json:"macs" yaml:"macs"`
	KeyExchange      []string `json:"key_exchange" yaml:"key_exchange"`

	// 会话配置
	RequestPty   bool   `json:"request_pty" yaml:"request_pty"`
	TerminalType string `json:"terminal_type" yaml:"terminal_type"`
	WindowWidth  int    `json:"window_width" yaml:"window_width"`
	WindowHeight int    `json:"window_height" yaml:"window_height"`
}

// Scrapli特定配置
type ScrapliConfig struct {
	// 传输配置
	TransportType      string `json:"transport_type" yaml:"transport_type"` // system, ssh2, paramiko
	StrictHostChecking bool   `json:"strict_host_checking" yaml:"strict_host_checking"`

	// 平台特定配置
	CommsPromptPattern string        `json:"comms_prompt_pattern" yaml:"comms_prompt_pattern"`
	CommsReturnChar    string        `json:"comms_return_char" yaml:"comms_return_char"`
	CommsReadDelay     time.Duration `json:"comms_read_delay" yaml:"comms_read_delay"`
	TimeoutOpsDefault  time.Duration `json:"timeout_ops_default" yaml:"timeout_ops_default"`

	// 特权模式配置
	PrivEscalatePattern   string `json:"priv_escalate_pattern" yaml:"priv_escalate_pattern"`
	PrivEscalatePassword  string `json:"priv_escalate_password" yaml:"priv_escalate_password"`
	PrivDeescalatePattern string `json:"priv_deescalate_pattern" yaml:"priv_deescalate_pattern"`

	// 高级选项
	FailedWhenContains []string          `json:"failed_when_contains" yaml:"failed_when_contains"`
	TextfsCommands     map[string]string `json:"textfs_commands" yaml:"textfs_commands"`
	OnInit             []string          `json:"on_init" yaml:"on_init"`
	OnOpen             []string          `json:"on_open" yaml:"on_open"`
	OnClose            []string          `json:"on_close" yaml:"on_close"`
}

// 安全配置
type SecurityConfig struct {
	// TLS配置
	TLSEnabled         bool   `json:"tls_enabled" yaml:"tls_enabled"`
	TLSVersion         string `json:"tls_version" yaml:"tls_version"`
	CertFile           string `json:"cert_file" yaml:"cert_file"`
	KeyFile            string `json:"key_file" yaml:"key_file"`
	CAFile             string `json:"ca_file" yaml:"ca_file"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify" yaml:"insecure_skip_verify"`

	// 访问控制
	AllowedCiphers    []string `json:"allowed_ciphers" yaml:"allowed_ciphers"`
	DisallowedCiphers []string `json:"disallowed_ciphers" yaml:"disallowed_ciphers"`

	// 审计配置
	AuditEnabled      bool     `json:"audit_enabled" yaml:"audit_enabled"`
	AuditLogPath      string   `json:"audit_log_path" yaml:"audit_log_path"`
	SensitiveCommands []string `json:"sensitive_commands" yaml:"sensitive_commands"`
}

// 配置验证器
type ConfigValidator struct {
	strictMode bool
}

// 配置构建器
type ConfigBuilder struct {
	config *EnhancedConnectionConfig
}

// NewConfigBuilder 创建配置构建器
func NewConfigBuilder() *ConfigBuilder {
	return &ConfigBuilder{
		config: &EnhancedConnectionConfig{
			// 设置默认值
			Port:            22,
			ConnectTimeout:  30 * time.Second,
			ReadTimeout:     30 * time.Second,
			WriteTimeout:    10 * time.Second,
			IdleTimeout:     5 * time.Minute,
			MaxRetries:      3,
			RetryInterval:   1 * time.Second,
			BackoffFactor:   2.0,
			MaxConnections:  10,
			MinConnections:  1,
			MaxIdleTime:     10 * time.Minute,
			HealthCheckTime: 30 * time.Second,
			Extensions:      make(map[string]interface{}),
			Labels:          make(map[string]string),
			Metadata:        make(map[string]interface{}),
		},
	}
}

// WithBasicAuth 设置基础认证
func (b *ConfigBuilder) WithBasicAuth(host, username, password string) *ConfigBuilder {
	b.config.Host = host
	b.config.Username = username
	b.config.Password = password
	return b
}

// WithProtocol 设置协议和平台
func (b *ConfigBuilder) WithProtocol(protocol Protocol, platform Platform) *ConfigBuilder {
	b.config.Protocol = protocol
	b.config.Platform = platform
	return b
}

// WithTimeouts 设置超时配置
func (b *ConfigBuilder) WithTimeouts(connect, read, write, idle time.Duration) *ConfigBuilder {
	if connect > 0 {
		b.config.ConnectTimeout = connect
	}
	if read > 0 {
		b.config.ReadTimeout = read
	}
	if write > 0 {
		b.config.WriteTimeout = write
	}
	if idle > 0 {
		b.config.IdleTimeout = idle
	}
	return b
}

// WithRetryPolicy 设置重试策略
func (b *ConfigBuilder) WithRetryPolicy(maxRetries int, interval time.Duration, backoff float64) *ConfigBuilder {
	b.config.MaxRetries = maxRetries
	b.config.RetryInterval = interval
	b.config.BackoffFactor = backoff
	return b
}

// WithConnectionPool 设置连接池配置
func (b *ConfigBuilder) WithConnectionPool(max, min int, maxIdle, healthCheck time.Duration) *ConfigBuilder {
	b.config.MaxConnections = max
	b.config.MinConnections = min
	b.config.MaxIdleTime = maxIdle
	b.config.HealthCheckTime = healthCheck
	return b
}

// WithSSHConfig 设置SSH配置
func (b *ConfigBuilder) WithSSHConfig(config *SSHConfig) *ConfigBuilder {
	b.config.SSHConfig = config
	return b
}

// WithScrapliConfig 设置Scrapli配置
func (b *ConfigBuilder) WithScrapliConfig(config *ScrapliConfig) *ConfigBuilder {
	b.config.ScrapliConfig = config
	return b
}

// WithSecurity 设置安全配置
func (b *ConfigBuilder) WithSecurity(config *SecurityConfig) *ConfigBuilder {
	b.config.SecurityConfig = config
	return b
}

// WithLabels 设置标签
func (b *ConfigBuilder) WithLabels(labels map[string]string) *ConfigBuilder {
	for k, v := range labels {
		b.config.Labels[k] = v
	}
	return b
}

// WithMetadata 设置元数据
func (b *ConfigBuilder) WithMetadata(key string, value interface{}) *ConfigBuilder {
	b.config.Metadata[key] = value
	return b
}

// Build 构建配置
func (b *ConfigBuilder) Build() (*EnhancedConnectionConfig, error) {
	if err := b.config.Validate(); err != nil {
		return nil, err
	}
	return b.config, nil
}

// Validate 验证配置
func (c *EnhancedConnectionConfig) Validate() error {
	// 基础字段验证
	if c.Host == "" {
		return fmt.Errorf("host is required")
	}
	if c.Username == "" {
		return fmt.Errorf("username is required")
	}
	if c.Password == "" {
		return fmt.Errorf("password is required")
	}
	if c.Protocol == "" {
		return fmt.Errorf("protocol is required")
	}
	if c.Platform == "" {
		return fmt.Errorf("platform is required")
	}

	// 端口范围验证
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}

	// 超时时间验证
	if c.ConnectTimeout <= 0 {
		return fmt.Errorf("connect timeout must be positive")
	}

	// 连接池配置验证
	if c.MaxConnections <= 0 {
		return fmt.Errorf("max connections must be positive")
	}
	if c.MinConnections < 0 {
		return fmt.Errorf("min connections cannot be negative")
	}
	if c.MinConnections > c.MaxConnections {
		return fmt.Errorf("min connections cannot exceed max connections")
	}

	// 重试配置验证
	if c.MaxRetries < 0 {
		return fmt.Errorf("max retries cannot be negative")
	}
	if c.BackoffFactor < 1.0 {
		return fmt.Errorf("backoff factor must be >= 1.0")
	}

	// 协议特定验证
	switch c.Protocol {
	case ProtocolSSH:
		if c.SSHConfig != nil {
			if err := c.SSHConfig.Validate(); err != nil {
				return fmt.Errorf("SSH config validation failed: %w", err)
			}
		}
	case ProtocolScrapli:
		if c.ScrapliConfig != nil {
			if err := c.ScrapliConfig.Validate(); err != nil {
				return fmt.Errorf("Scrapli config validation failed: %w", err)
			}
		}
	}

	return nil
}

// Validate SSH配置验证
func (s *SSHConfig) Validate() error {
	if s.AuthMethod != "" {
		validMethods := []string{"password", "publickey", "keyboard-interactive"}
		valid := false
		for _, method := range validMethods {
			if s.AuthMethod == method {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("invalid auth method: %s", s.AuthMethod)
		}
	}

	if s.CompressionLevel < 0 || s.CompressionLevel > 9 {
		return fmt.Errorf("compression level must be between 0 and 9")
	}

	return nil
}

// Validate Scrapli配置验证
func (s *ScrapliConfig) Validate() error {
	if s.TransportType != "" {
		validTransports := []string{"system", "ssh2", "paramiko"}
		valid := false
		for _, transport := range validTransports {
			if s.TransportType == transport {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("invalid transport type: %s", s.TransportType)
		}
	}

	if s.CommsReadDelay < 0 {
		return fmt.Errorf("comms read delay cannot be negative")
	}

	if s.TimeoutOpsDefault <= 0 {
		return fmt.Errorf("timeout ops default must be positive")
	}

	return nil
}

// Clone 克隆配置
func (c *EnhancedConnectionConfig) Clone() *EnhancedConnectionConfig {
	clone := *c

	// 深拷贝嵌套结构
	if c.SSHConfig != nil {
		sshClone := *c.SSHConfig
		clone.SSHConfig = &sshClone
	}

	if c.ScrapliConfig != nil {
		scrapliClone := *c.ScrapliConfig
		clone.ScrapliConfig = &scrapliClone
	}

	if c.SecurityConfig != nil {
		secClone := *c.SecurityConfig
		clone.SecurityConfig = &secClone
	}

	// 深拷贝maps
	clone.Extensions = make(map[string]interface{})
	for k, v := range c.Extensions {
		clone.Extensions[k] = v
	}

	clone.Labels = make(map[string]string)
	for k, v := range c.Labels {
		clone.Labels[k] = v
	}

	clone.Metadata = make(map[string]interface{})
	for k, v := range c.Metadata {
		clone.Metadata[k] = v
	}

	return &clone
}

// ToLegacyConfig 转换为旧版配置格式（兼容性）
func (c *EnhancedConnectionConfig) ToLegacyConfig() ConnectionConfig {
	legacy := ConnectionConfig{
		IP:       c.Host,
		Username: c.Username,
		Password: c.Password,
		Port:     c.Port,
		Timeout:  c.ConnectTimeout,
		Metadata: make(map[string]interface{}),
	}

	// 复制协议和平台信息
	legacy.Metadata["protocol"] = string(c.Protocol)
	legacy.Metadata["platform"] = string(c.Platform)

	// 复制其他元数据
	for k, v := range c.Metadata {
		legacy.Metadata[k] = v
	}

	return legacy
}

// GetConnectionString 获取连接字符串（用于日志和调试）
func (c *EnhancedConnectionConfig) GetConnectionString() string {
	return fmt.Sprintf("%s://%s@%s:%d", c.Protocol, c.Username, c.Host, c.Port)
}

// IsSecure 判断是否为安全连接
func (c *EnhancedConnectionConfig) IsSecure() bool {
	if c.SecurityConfig != nil && c.SecurityConfig.TLSEnabled {
		return true
	}
	// SSH本身就是加密的
	return c.Protocol == ProtocolSSH
}

// GetEffectiveTimeout 获取有效超时时间
func (c *EnhancedConnectionConfig) GetEffectiveTimeout(operation string) time.Duration {
	switch operation {
	case "connect":
		return c.ConnectTimeout
	case "read":
		return c.ReadTimeout
	case "write":
		return c.WriteTimeout
	case "idle":
		return c.IdleTimeout
	default:
		return c.ConnectTimeout
	}
}

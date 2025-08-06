package connection

import (
	"fmt"
	"sync"
	"time"

	"github.com/scrapli/scrapligo/channel"
	"github.com/scrapli/scrapligo/driver/network"
	"github.com/scrapli/scrapligo/driver/options"
	"github.com/scrapli/scrapligo/platform"
)

type ScrapliDriver struct {
	host       string
	username   string
	password   string
	platform   string
	mu         sync.Mutex       // 保证线程安全
	driver     *network.Driver  // 主驱动
	channel    *channel.Channel // 独立缓存Channel
	maxRetries int              // 最大重试次数
	timeout    time.Duration    // 操作超时时间
}

func (d *ScrapliDriver) ProtocolType() string {
	return "scrapli"
}

// SendConfig 发送配置命令
func (d *ScrapliDriver) SendConfig(config string) (string, error) {
	r, err := d.driver.SendConfig(config)
	if err != nil {
		return "", err
	}
	return r.Result, nil
}

// SupportedPlatforms 返回支持的平台列表
func (d *ScrapliDriver) SupportedPlatforms() []string {
	return []string{
		"cisco_iosxe",
		"cisco_iosxr",
		"cisco_nxos",
		"juniper_junos",
		"nokia_sros",
		"arista_eos",
	}
}

// NewScrapliDriver 创建驱动实例
func NewScrapliDriver(platformOS, host, username, password string) *ScrapliDriver {
	return &ScrapliDriver{
		platform: platformOS,
		host:     host,
		username: username,
		password: password,
	}
}

// Connect 使用platform方式建立连接
func (d *ScrapliDriver) Connect() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	p, err := platform.NewPlatform(
		d.platform,
		d.host,
		options.WithAuthNoStrictKey(),
		options.WithAuthUsername(d.username),
		options.WithAuthPassword(d.password),
	)
	if err != nil {
		return fmt.Errorf("create platform failed: %w", err)
	}

	d.driver, err = p.GetNetworkDriver()
	if err != nil {
		return fmt.Errorf("get network driver failed: %w", err)
	}

	if err = d.driver.Open(); err != nil {
		return fmt.Errorf("open connection failed: %w", err)
	}

	d.channel = d.driver.Channel
	return nil
}

// SendInteractive 发送交互式命令
func (d *ScrapliDriver) SendInteractive(events []*channel.SendInteractiveEvent) (string, error) {
	if err := d.ensureConnected(); err != nil {
		return "", err
	}

	resp, err := d.channel.SendInteractive(events)
	if err != nil {
		return "", fmt.Errorf("send interactive failed: %w", err)
	}
	return string(resp), nil
}

// SendCommand 发送普通命令
func (d *ScrapliDriver) SendCommands(commands []string) (string, error) {
	if err := d.ensureConnected(); err != nil {
		return "", err
	}

	response, err := d.driver.SendCommands(commands)
	if err != nil {
		return "", fmt.Errorf("send command failed: %w", err)
	}
	return response.Result, nil
}

// GetPrompt 获取设备提示符
func (d *ScrapliDriver) GetPrompt() (string, error) {
	if err := d.ensureConnected(); err != nil {
		return "", err
	}
	return d.driver.GetPrompt()
}

// Close 关闭连接
func (d *ScrapliDriver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.driver == nil {
		return nil
	}
	return d.driver.Close()
}

// ensureConnected 确保连接已建立
func (d *ScrapliDriver) ensureConnected() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.driver == nil {
		if err := d.Connect(); err != nil {
			return fmt.Errorf("connection not established: %w", err)
		}
	}
	return nil
}
func (d *ScrapliDriver) Execute(input interface{}) (interface{}, error) {
	switch v := input.(type) {
	case []*channel.SendInteractiveEvent:
		return d.driver.SendInteractive(v)
	default:
		return nil, ErrUnsupportedInputType
	}
}

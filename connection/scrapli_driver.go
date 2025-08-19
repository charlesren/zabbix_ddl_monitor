package connection

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/scrapli/scrapligo/channel"
	"github.com/scrapli/scrapligo/driver/network"
	"github.com/scrapli/scrapligo/driver/options"
	"github.com/scrapli/scrapligo/platform"
	"github.com/scrapli/scrapligo/response"
)

var ErrUnsupportedCommandType = errors.New("unsupported command type")
var ErrUnsupportedInputType = errors.New("unsupported input type")

type ScrapliDriver struct {
	host       string
	username   string
	password   string
	platform   string
	mu         sync.Mutex         // 保证线程安全
	driver     *network.Driver    // 主驱动
	channel    *channel.Channel   // 独立缓存Channel
	maxRetries int                // 最大重试次数
	timeout    time.Duration      // 操作超时时间
	ctx        context.Context    // 新增上下文字段
	cancel     context.CancelFunc // 对应的取消函数
}

func (d *ScrapliDriver) ProtocolType() Protocol {
	return ProtocolScrapli
}

// connection/scrapli_driver.go
func (d *ScrapliDriver) Execute(req *ProtocolRequest) (*ProtocolResponse, error) {
	if d == nil || d.driver == nil || d.channel == nil {
		return nil, fmt.Errorf("driver or channel not initialized")
	}

	if req == nil {
		return nil, fmt.Errorf("request cannot be nil")
	}

	// 检查上下文是否已取消
	if err := d.ctx.Err(); err != nil {
		return nil, err
	}

	switch req.CommandType {
	case CommandTypeCommands:
		cmds, ok := req.Payload.([]string)
		if !ok {
			return nil, fmt.Errorf("invalid commands payload")
		}
		resp, err := d.driver.SendCommands(cmds)
		if err != nil {
			return nil, err
		}
		// 提取所有Response的结果并拼接为RawData
		var rawData strings.Builder
		for _, r := range resp.Responses {
			rawData.WriteString(r.Result)
		}
		return &ProtocolResponse{
			Success:    err == nil,
			RawData:    []byte(rawData.String()),
			Structured: resp,
		}, err

	case CommandTypeInteractiveEvent:
		events, ok := req.Payload.([]*channel.SendInteractiveEvent)
		if !ok {
			return nil, fmt.Errorf("invalid events payload")
		}
		resp, err := d.channel.SendInteractive(events)
		return &ProtocolResponse{
			Success:    err == nil,
			RawData:    resp,
			Structured: nil, // 交互式命令无结构化响应
		}, err

	default:
		return nil, ErrUnsupportedCommandType
	}
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
		platform:   platformOS,
		host:       host,
		username:   username,
		password:   password,
		timeout:    30 * time.Second, // 默认超时时间
		maxRetries: 3,                // 默认重试次数
	}
}

func (d *ScrapliDriver) WithContext(ctx context.Context) *ScrapliDriver {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.cancel != nil {
		d.cancel() // 取消旧的上下文
	}

	d.ctx, d.cancel = context.WithCancel(ctx)
	return d
}

// Connect 使用platform方式建立连接
func (d *ScrapliDriver) Connect() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctx == nil {
		d.ctx, d.cancel = context.WithTimeout(context.Background(), d.timeout)
	}
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
func (d *ScrapliDriver) SendInteractive(events []*channel.SendInteractiveEvent) ([]byte, error) {
	if err := d.ensureConnected(); err != nil {
		return nil, err
	}

	resp, err := d.channel.SendInteractive(events)
	if err != nil {
		return nil, fmt.Errorf("send interactive failed: %w", err)
	}
	return resp, nil
}

// SendCommand 发送普通命令
func (d *ScrapliDriver) SendCommands(commands []string) (*response.MultiResponse, error) {
	if err := d.ensureConnected(); err != nil {
		return nil, err
	}

	response, err := d.driver.SendCommands(commands)
	if err != nil {
		return nil, fmt.Errorf("send command failed: %w", err)
	}
	return response, nil
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

	if d.cancel != nil {
		d.cancel() // 取消上下文
	}
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

// connection/scrapli_driver.go
func (d *ScrapliDriver) GetCapability() ProtocolCapability {
	caps := ScrapliCapability
	caps.PlatformSupport = []Platform{Platform(d.platform)} // 动态设置当前平台
	return caps
}

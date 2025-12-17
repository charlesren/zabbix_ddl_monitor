package connection

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charlesren/ylog"
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
func (d *ScrapliDriver) Execute(ctx context.Context, req *ProtocolRequest) (*ProtocolResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// 全面检查关键字段
	if d == nil || d.driver == nil || d.channel == nil {
		ylog.Errorf("ScrapliDriver", "critical field not initialized: driver=%v, channel=%v",
			d.driver, d.channel)
		return nil, fmt.Errorf("driver not properly initialized")
	}

	// 使用传入的上下文，如果为nil则使用默认上下文
	effectiveCtx := ctx
	if effectiveCtx == nil {
		if d.ctx == nil {
			d.ctx, d.cancel = context.WithTimeout(context.Background(), d.timeout)
			ylog.Debugf("ScrapliDriver", "initialized default context: timeout=%v", d.timeout)
		}
		effectiveCtx = d.ctx
	}

	// 检查上下文状态（已受锁保护）
	if err := effectiveCtx.Err(); err != nil {
		ylog.Warnf("ScrapliDriver", "context cancelled: %v", err)
		return nil, err
	}

	if req == nil {
		return nil, fmt.Errorf("request cannot be nil")
	}

	switch req.CommandType {
	case CommandTypeCommands:
		cmds, ok := req.Payload.([]string)
		if !ok {
			return nil, fmt.Errorf("invalid commands payload")
		}

		// 使用goroutine包装SendCommands，以便可以超时中断
		resultChan := make(chan struct {
			resp *response.MultiResponse
			err  error
		}, 1)

		ylog.Debugf("ScrapliDriver", "executing %d commands with timeout control", len(cmds))

		go func() {
			resp, err := d.driver.SendCommands(cmds)
			resultChan <- struct {
				resp *response.MultiResponse
				err  error
			}{resp, err}
		}()

		select {
		case <-effectiveCtx.Done():
			// 上下文超时或取消
			ylog.Warnf("ScrapliDriver", "command execution timed out or cancelled: %v", effectiveCtx.Err())
			return nil, effectiveCtx.Err()
		case result := <-resultChan:
			if result.err != nil {
				ylog.Errorf("ScrapliDriver", "command execution failed: %v", result.err)
				return nil, result.err
			}
			// 提取所有Response的结果并拼接为RawData，每个命令结果之间添加分隔符
			var rawData strings.Builder
			for i, r := range result.resp.Responses {
				rawData.WriteString(r.Result)
				// 在命令结果之间添加分隔符（除了最后一个）
				if i < len(result.resp.Responses)-1 {
					rawData.WriteString("\n")
				}
			}
			ylog.Debugf("ScrapliDriver", "command execution successful, %d responses received", len(result.resp.Responses))
			return &ProtocolResponse{
				Success:    true,
				RawData:    []byte(rawData.String()),
				Structured: result.resp,
			}, nil
		}

	case CommandTypeInteractiveEvent:
		events, ok := req.Payload.([]*channel.SendInteractiveEvent)
		if !ok {
			return nil, fmt.Errorf("invalid events payload")
		}

		// 使用goroutine包装SendInteractive，以便可以超时中断
		resultChan := make(chan struct {
			resp []byte
			err  error
		}, 1)

		ylog.Debugf("ScrapliDriver", "executing interactive events with timeout control, events: %d", len(events))

		go func() {
			resp, err := d.channel.SendInteractive(events)
			resultChan <- struct {
				resp []byte
				err  error
			}{resp, err}
		}()

		select {
		case <-effectiveCtx.Done():
			// 上下文超时或取消
			ylog.Warnf("ScrapliDriver", "interactive execution timed out or cancelled: %v", effectiveCtx.Err())
			return nil, effectiveCtx.Err()
		case result := <-resultChan:
			if result.err != nil {
				ylog.Errorf("ScrapliDriver", "interactive execution failed: %v", result.err)
				return nil, result.err
			}
			ylog.Debugf("ScrapliDriver", "interactive execution successful, response length: %d", len(result.resp))
			return &ProtocolResponse{
				Success:    true,
				RawData:    result.resp,
				Structured: nil, // 交互式命令无结构化响应
			}, nil
		}

	default:
		return nil, ErrUnsupportedCommandType
	}
}

// SendConfig 发送配置命令
func (d *ScrapliDriver) SendConfig(config string) (string, error) {
	if err := d.ensureConnected(); err != nil {
		return "", err
	}
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
// 已弃用：请使用工厂模式（ScrapliFactory）替代直接构造函数
// 该函数将在未来版本中移除，推荐使用 connection.NewScrapliFactory().Create(config)
func NewScrapliDriver(platformOS, host, username, password string) *ScrapliDriver {
	panic("NewScrapliDriver is deprecated, use factory pattern instead")
}

func (d *ScrapliDriver) WithContext(ctx context.Context) *ScrapliDriver {
	d.mu.Lock()
	defer d.mu.Unlock()

	// 取消旧上下文（如果存在）
	if d.cancel != nil {
		d.cancel()
	}

	// 创建不可变的新上下文
	newCtx, cancel := context.WithCancel(ctx)
	d.ctx = newCtx
	d.cancel = cancel

	ylog.Debugf("ScrapliDriver", "context updated: %p (old cancel: %v)", newCtx, d.cancel != nil)
	return d
}

// Connect 使用platform方式建立连接
func (d *ScrapliDriver) Connect() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	// 确保基础字段初始化
	if d.ctx == nil {
		d.ctx, d.cancel = context.WithTimeout(context.Background(), d.timeout)
		ylog.Debugf("ScrapliDriver", "initialized default context: timeout=%v", d.timeout)
	}

	// 记录当前状态
	ylog.Debugf("ScrapliDriver", "connecting with: platform=%s, host=%s, ctx=%p",
		d.platform, d.host, d.ctx)
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
		return "driver connect failed", err
	}
	ylog.Debugf("scrapli", "GetPrompt...")
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
	return ScrapliCapability
}

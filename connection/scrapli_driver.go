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
	ctx        context.Context    // 上下文字段
	cancel     context.CancelFunc // 对应的取消函数
	closed     bool               // 标记是否已关闭
}

func (d *ScrapliDriver) ProtocolType() Protocol {
	return ProtocolScrapli
}

// connection/scrapli_driver.go
func (d *ScrapliDriver) Execute(ctx context.Context, req *ProtocolRequest) (*ProtocolResponse, error) {
	// 1. 确保连接已建立（需要锁保护连接状态）
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		ylog.Errorf("ScrapliDriver", "driver already closed: host=%s", d.host)
		return nil, fmt.Errorf("driver already closed")
	}

	if d.driver == nil {
		d.mu.Unlock()
		ylog.Errorf("ScrapliDriver", "driver not initialized")
		return nil, fmt.Errorf("driver not properly initialized")
	}

	// 检查是否需要建立连接
	if d.channel == nil {
		// 临时释放锁，调用Connect（Connect内部会重新获取锁）
		d.mu.Unlock()
		if err := d.Connect(); err != nil {
			ylog.Errorf("ScrapliDriver", "failed to establish connection: %v", err)
			return nil, fmt.Errorf("connection not established: %w", err)
		}
		// 重新获取锁检查连接状态
		d.mu.Lock()
	}

	// 验证连接状态
	if d.channel == nil {
		d.mu.Unlock()
		return nil, fmt.Errorf("channel not available after connection attempt")
	}

	// 获取driver和channel的本地引用，然后释放锁
	driver := d.driver
	ch := d.channel
	d.mu.Unlock()

	// 使用传入的上下文，如果为nil则使用默认上下文
	effectiveCtx := ctx
	if effectiveCtx == nil {
		d.mu.Lock()
		if d.ctx == nil {
			d.ctx, d.cancel = context.WithTimeout(context.Background(), d.timeout)
			ylog.Debugf("ScrapliDriver", "initialized default context in Execute: timeout=%v", d.timeout)
		}
		effectiveCtx = d.ctx
		d.mu.Unlock()
	}

	// 检查上下文状态
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

		// 创建带超时的子上下文，专门用于这个执行
		execCtx, execCancel := context.WithTimeout(effectiveCtx, d.timeout)
		defer execCancel()

		// 使用goroutine包装SendCommands，以便可以超时中断
		resultChan := make(chan struct {
			resp *response.MultiResponse
			err  error
		}, 1)

		ylog.Debugf("ScrapliDriver", "executing %d commands with timeout control", len(cmds))

		go func() {
			// 执行命令
			resp, err := driver.SendCommands(cmds)

			// 发送结果前检查上下文，避免goroutine泄漏
			select {
			case resultChan <- struct {
				resp *response.MultiResponse
				err  error
			}{resp, err}:
				// 成功发送结果
				ylog.Debugf("ScrapliDriver", "command execution completed and result sent")
			case <-execCtx.Done():
				// 上下文已取消，丢弃结果，避免goroutine泄漏
				ylog.Debugf("ScrapliDriver", "context cancelled before sending result, discarding to prevent goroutine leak")
			}
		}()

		select {
		case <-execCtx.Done():
			// 上下文超时或取消
			ylog.Warnf("ScrapliDriver", "command execution timed out or cancelled: %v", execCtx.Err())
			return nil, execCtx.Err()
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

		// 创建带超时的子上下文，专门用于这个执行
		execCtx, execCancel := context.WithTimeout(effectiveCtx, d.timeout)
		defer execCancel()

		// 使用goroutine包装SendInteractive，以便可以超时中断
		resultChan := make(chan struct {
			resp []byte
			err  error
		}, 1)

		ylog.Debugf("ScrapliDriver", "executing interactive events with timeout control, events: %d", len(events))

		go func() {
			// 执行交互式命令
			resp, err := ch.SendInteractive(events)

			// 发送结果前检查上下文，避免goroutine泄漏
			select {
			case resultChan <- struct {
				resp []byte
				err  error
			}{resp, err}:
				// 成功发送结果
				ylog.Debugf("ScrapliDriver", "interactive execution completed and result sent")
			case <-execCtx.Done():
				// 上下文已取消，丢弃结果，避免goroutine泄漏
				ylog.Debugf("ScrapliDriver", "context cancelled before sending result, discarding to prevent goroutine leak")
			}
		}()

		select {
		case <-execCtx.Done():
			// 上下文超时或取消
			ylog.Warnf("ScrapliDriver", "interactive execution timed out or cancelled: %v", execCtx.Err())
			return nil, execCtx.Err()
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
	// 确保连接已建立
	d.mu.Lock()
	if d.driver == nil {
		d.mu.Unlock()
		return "", fmt.Errorf("driver not initialized")
	}

	if d.channel == nil {
		d.mu.Unlock()
		if err := d.Connect(); err != nil {
			return "", fmt.Errorf("connection not established: %w", err)
		}
		d.mu.Lock()
	}

	if d.driver == nil || d.channel == nil {
		d.mu.Unlock()
		return "", fmt.Errorf("driver or channel not available after connection attempt")
	}

	driver := d.driver
	d.mu.Unlock()

	r, err := driver.SendConfig(config)
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

// WithContext 设置上下文
func (d *ScrapliDriver) WithContext(ctx context.Context) *ScrapliDriver {
	d.mu.Lock()
	defer d.mu.Unlock()

	// 如果已经关闭，直接返回
	if d.closed {
		ylog.Debugf("ScrapliDriver", "cannot set context on closed driver: host=%s", d.host)
		return d
	}

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

	// 如果已经关闭，直接返回错误
	if d.closed {
		ylog.Errorf("ScrapliDriver", "cannot connect closed driver: host=%s", d.host)
		return fmt.Errorf("driver already closed")
	}

	// 如果已经连接，直接返回
	if d.driver != nil && d.channel != nil {
		ylog.Debugf("ScrapliDriver", "already connected: host=%s", d.host)
		return nil
	}

	// 确保基础字段初始化
	if d.ctx == nil {
		// 如果Factory没有设置context，创建新的
		d.ctx, d.cancel = context.WithTimeout(context.Background(), d.timeout)
		ylog.Debugf("ScrapliDriver", "initialized default context: timeout=%v", d.timeout)
	} else if d.cancel == nil {
		// 如果Factory设置了context但没有cancel函数，创建cancel函数
		// 这种情况不应该发生，但为了安全起见处理一下
		var cancel context.CancelFunc
		d.ctx, cancel = context.WithCancel(d.ctx)
		d.cancel = cancel
		ylog.Debugf("ScrapliDriver", "added cancel function to existing context")
	}

	// 记录当前状态
	ylog.Debugf("ScrapliDriver", "connecting with: platform=%s, host=%s, ctx=%p",
		d.platform, d.host, d.ctx)

	// 如果driver已经存在（由Factory创建），直接打开它
	if d.driver != nil {
		if err := d.driver.Open(); err != nil {
			// 如果打开失败，清理资源
			d.driver = nil
			d.channel = nil
			return fmt.Errorf("open existing driver failed: %w", err)
		}
		d.channel = d.driver.Channel
		ylog.Infof("ScrapliDriver", "successfully opened existing driver: host=%s, platform=%s", d.host, d.platform)
		return nil
	}

	// 创建新的driver（兼容旧代码）
	p, err := platform.NewPlatform(
		d.platform,
		d.host,
		options.WithAuthNoStrictKey(),
		options.WithAuthUsername(d.username),
		options.WithAuthPassword(d.password),
		options.WithTimeoutOps(d.timeout),
	)
	if err != nil {
		return fmt.Errorf("create platform failed: %w", err)
	}

	d.driver, err = p.GetNetworkDriver()
	if err != nil {
		return fmt.Errorf("get network driver failed: %w", err)
	}

	if err = d.driver.Open(); err != nil {
		// 如果打开失败，清理资源
		d.driver = nil
		d.channel = nil
		return fmt.Errorf("open connection failed: %w", err)
	}

	d.channel = d.driver.Channel
	ylog.Infof("ScrapliDriver", "successfully connected new driver: host=%s, platform=%s", d.host, d.platform)
	return nil
}

// SendInteractive 发送交互式命令
func (d *ScrapliDriver) SendInteractive(events []*channel.SendInteractiveEvent) ([]byte, error) {
	// 确保连接已建立
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		ylog.Errorf("ScrapliDriver", "driver already closed: host=%s", d.host)
		return nil, fmt.Errorf("driver already closed")
	}

	if d.driver == nil {
		d.mu.Unlock()
		return nil, fmt.Errorf("driver not initialized")
	}

	if d.channel == nil {
		d.mu.Unlock()
		if err := d.Connect(); err != nil {
			return nil, fmt.Errorf("connection not established: %w", err)
		}
		d.mu.Lock()
	}

	if d.channel == nil {
		d.mu.Unlock()
		return nil, fmt.Errorf("channel not available after connection attempt")
	}

	ch := d.channel
	d.mu.Unlock()

	resp, err := ch.SendInteractive(events)
	if err != nil {
		return nil, fmt.Errorf("send interactive failed: %w", err)
	}
	return resp, nil
}

// SendCommand 发送普通命令
func (d *ScrapliDriver) SendCommands(commands []string) (*response.MultiResponse, error) {
	// 确保连接已建立
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		ylog.Errorf("ScrapliDriver", "driver already closed: host=%s", d.host)
		return nil, fmt.Errorf("driver already closed")
	}

	if d.driver == nil {
		d.mu.Unlock()
		return nil, fmt.Errorf("driver not initialized")
	}

	if d.channel == nil {
		d.mu.Unlock()
		if err := d.Connect(); err != nil {
			return nil, fmt.Errorf("connection not established: %w", err)
		}
		d.mu.Lock()
	}

	if d.driver == nil || d.channel == nil {
		d.mu.Unlock()
		return nil, fmt.Errorf("driver or channel not available after connection attempt")
	}

	driver := d.driver
	d.mu.Unlock()

	response, err := driver.SendCommands(commands)
	if err != nil {
		return nil, fmt.Errorf("send command failed: %w", err)
	}
	return response, nil
}

// GetPrompt 获取设备提示符
func (d *ScrapliDriver) GetPrompt() (string, error) {
	// 确保连接已建立
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		ylog.Errorf("ScrapliDriver", "driver already closed: host=%s", d.host)
		return "", fmt.Errorf("driver already closed")
	}

	if d.driver == nil {
		d.mu.Unlock()
		return "", fmt.Errorf("driver not initialized")
	}

	if d.channel == nil {
		d.mu.Unlock()
		if err := d.Connect(); err != nil {
			return "driver connect failed", fmt.Errorf("connection not established: %w", err)
		}
		d.mu.Lock()
	}

	if d.driver == nil {
		d.mu.Unlock()
		return "driver not available", fmt.Errorf("driver not available")
	}

	driver := d.driver
	d.mu.Unlock()

	ylog.Debugf("scrapli", "GetPrompt...")
	return driver.GetPrompt()
}

// GetPromptWithContext 获取设备提示符（带上下文控制）
func (d *ScrapliDriver) GetPromptWithContext(ctx context.Context) (string, error) {
	// 使用传入的上下文，如果为nil则使用默认上下文
	effectiveCtx := ctx
	if effectiveCtx == nil {
		effectiveCtx = context.Background()
	}

	// 创建带超时的子上下文，专门用于这个执行
	execCtx, execCancel := context.WithTimeout(effectiveCtx, d.timeout)
	defer execCancel()

	// 使用goroutine包装GetPrompt，以便可以超时中断
	resultChan := make(chan struct {
		prompt string
		err    error
	}, 1)

	ylog.Debugf("ScrapliDriver", "getting prompt with timeout control")

	go func() {
		// 执行获取提示符操作
		prompt, err := d.GetPrompt()

		// 发送结果前检查上下文，避免goroutine泄漏
		select {
		case resultChan <- struct {
			prompt string
			err    error
		}{prompt, err}:
			// 成功发送结果
			ylog.Debugf("ScrapliDriver", "get prompt completed and result sent")
		case <-execCtx.Done():
			// 上下文已取消，丢弃结果，避免goroutine泄漏
			ylog.Debugf("ScrapliDriver", "context cancelled before sending result, discarding to prevent goroutine leak")
		}
	}()

	select {
	case <-execCtx.Done():
		// 上下文超时或取消
		ylog.Warnf("ScrapliDriver", "get prompt timed out or cancelled: %v", execCtx.Err())
		return "", execCtx.Err()
	case result := <-resultChan:
		if result.err != nil {
			ylog.Errorf("ScrapliDriver", "get prompt failed: %v", result.err)
			return "", result.err
		}
		ylog.Debugf("ScrapliDriver", "get prompt successful")
		return result.prompt, nil
	}
}

// Close 关闭连接
func (d *ScrapliDriver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// 检查是否已经关闭
	if d.closed {
		ylog.Debugf("ScrapliDriver", "driver already closed: host=%s", d.host)
		return nil
	}

	// 标记为已关闭
	d.closed = true

	// 记录关闭前的状态
	ylog.Debugf("ScrapliDriver", "closing driver: host=%s, platform=%s, driver=%v, channel=%v",
		d.host, d.platform, d.driver != nil, d.channel != nil)

	// 先取消上下文，停止所有相关操作
	if d.cancel != nil {
		d.cancel() // 取消上下文
		// 给goroutine一点时间响应取消信号，但使用更智能的等待
		ylog.Debugf("ScrapliDriver", "cancelling context and waiting for goroutines to respond")
	}

	// 即使driver为nil，也要清理channel
	if d.driver == nil {
		ylog.Debugf("ScrapliDriver", "driver already nil, nothing to close")
		d.channel = nil
		ylog.Infof("ScrapliDriver", "driver closed (was already nil): host=%s", d.host)
		return nil
	}

	// 检查driver的实际状态，避免在无效状态时关闭
	// 注意：我们无法直接检查scrapli driver的内部状态，但可以尝试安全关闭
	var err error
	if d.driver != nil {
		// 尝试关闭driver，但处理可能的"预期内"错误
		err = d.driver.Close()
		if err != nil {
			// 检查是否为"预期内"的错误，这些错误可以安全忽略
			errStr := err.Error()
			isExpectedError := strings.Contains(errStr, "invalid argument") ||
				strings.Contains(errStr, "use of closed network connection") ||
				strings.Contains(errStr, "already closed") ||
				strings.Contains(errStr, "connection reset by peer") ||
				strings.Contains(errStr, "broken pipe")

			if isExpectedError {
				// 这些是预期内的错误，通常发生在driver已经关闭或连接已断开时
				ylog.Debugf("ScrapliDriver", "ignoring expected close error: %v", err)
				err = nil // 重置错误，因为这是预期情况
			} else {
				// 这是意外的错误，需要记录警告
				ylog.Warnf("ScrapliDriver", "driver.Close() returned unexpected error: %v", err)
			}
		}
	}

	// 重置连接相关字段，避免重复关闭
	d.driver = nil
	d.channel = nil

	// 注意：不重置ctx和cancel，因为Factory可能在其他地方使用它们
	// 只重置我们创建的临时context
	if d.ctx != nil && d.ctx.Err() == nil {
		// 这是一个活动的context，可能是Factory创建的，不重置
		ylog.Debugf("ScrapliDriver", "keeping factory-created context")
	} else {
		// 这是一个已取消或我们创建的context，可以清理
		d.ctx = nil
		d.cancel = nil
	}

	if err == nil {
		ylog.Infof("ScrapliDriver", "driver closed successfully: host=%s", d.host)
	} else {
		ylog.Infof("ScrapliDriver", "driver closed with error: host=%s, error=%v", d.host, err)
	}

	return err
}

// connection/scrapli_driver.go
func (d *ScrapliDriver) GetCapability() ProtocolCapability {
	return ScrapliCapability
}

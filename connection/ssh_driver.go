package connection

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/scrapli/scrapligo/channel"
	"golang.org/x/crypto/ssh"
)

type SSHDriver struct {
	session *ssh.Session
	timeout time.Duration // 命令执行超时
}

func NewSSHDriver(session *ssh.Session) *SSHDriver {
	return &SSHDriver{
		session: session,
		timeout: 30 * time.Second, // 默认超时
	}
}

// WithTimeout 设置命令执行超时
func (d *SSHDriver) WithTimeout(timeout time.Duration) *SSHDriver {
	d.timeout = timeout
	return d
}

func (d *SSHDriver) ProtocolType() Protocol {
	return ProtocolSSH
}

func (d *SSHDriver) SendCommands(commands []string) (string, error) {
	var output bytes.Buffer
	d.session.Stdout = &output
	for _, cmd := range commands {
		if err := d.session.Run(cmd); err != nil {
			return output.String(), fmt.Errorf("command execution failed: %w", err)
		}
	}
	return output.String(), nil
}

func (d *SSHDriver) SendInteractive(events []*channel.SendInteractiveEvent) (string, error) {
	return "", fmt.Errorf("SSH protocol does not support interactive commands")
}

func (d *SSHDriver) Close() error {
	return d.session.Close()
}

// connection/ssh_driver.go
func (d *SSHDriver) Execute(ctx context.Context, req *ProtocolRequest) (*ProtocolResponse, error) {
	if req.CommandType != CommandTypeCommands {
		return nil, ErrUnsupportedCommandType
	}

	cmds, ok := req.Payload.([]string)
	if !ok {
		return nil, fmt.Errorf("invalid commands payload")
	}

	// 确定使用的超时：优先使用传入的ctx超时，否则使用driver的timeout
	var cancel context.CancelFunc
	if ctx == nil {
		ctx, cancel = context.WithTimeout(context.Background(), d.timeout)
	} else {
		// 如果ctx没有超时，添加driver的timeout
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			ctx, cancel = context.WithTimeout(ctx, d.timeout)
		}
	}
	if cancel != nil {
		defer cancel()
	}

	// 使用goroutine实现超时控制，避免session.Run()永久阻塞
	resultChan := make(chan struct {
		output string
		err    error
	}, 1)

	go func() {
		output, err := d.SendCommands(cmds)

		// 发送结果前检查上下文，避免goroutine泄漏
		select {
		case resultChan <- struct {
			output string
			err    error
		}{output, err}:
			// 成功发送结果
		case <-ctx.Done():
			// 上下文已取消，丢弃结果，避免goroutine泄漏
		}
	}()

	// 等待结果或超时
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-resultChan:
		return &ProtocolResponse{
			Success:    result.err == nil,
			RawData:    []byte(result.output),
			Structured: nil, // SSH无结构化响应
		}, result.err
	}
}

// connection/ssh_driver.go
func (d *SSHDriver) GetCapability() ProtocolCapability {
	return ProtocolCapability{
		Protocol:        ProtocolSSH,
		PlatformSupport: []Platform{PlatformCiscoIOSXE, PlatformHuaweiVRP},
		CommandTypesSupport: []CommandType{
			CommandTypeCommands,
		},
		MaxConcurrent: 3,
		Timeout:       30 * time.Second,
	}
}

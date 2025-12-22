package connection

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/crypto/ssh"
)

type SSHFactory struct{}

func (f *SSHFactory) Create(config EnhancedConnectionConfig) (ProtocolDriver, error) {
	return f.CreateWithContext(context.Background(), config)
}

func (f *SSHFactory) CreateWithContext(ctx context.Context, config EnhancedConnectionConfig) (ProtocolDriver, error) {
	// 检查上下文
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// 设置默认值
	// 创建临时副本避免修改原始配置
	configCopy := config
	defer func() {
		configCopy.Password = ""
		configCopy.Metadata = nil
	}()

	if configCopy.Port == 0 {
		configCopy.Port = 22
	}

	// 单步建立SSH会话
	client, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", config.Host, config.Port),
		&ssh.ClientConfig{
			User:            config.Username,
			Auth:            []ssh.AuthMethod{ssh.Password(config.Password)},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         config.ConnectTimeout,
		})
	if err != nil {
		return nil, fmt.Errorf("SSH连接失败: %v", err)
	}

	session, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("创建会话失败: %v", err)
	}

	// 使用配置的ReadTimeout作为命令执行超时
	// 如果ReadTimeout为0，使用默认值
	timeout := config.ReadTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	return NewSSHDriver(session).WithTimeout(timeout), nil
}

func (f *SSHFactory) HealthCheck(driver ProtocolDriver, config EnhancedConnectionConfig) bool {
	// 使用配置的健康检查超时，默认5秒
	timeout := config.HealthCheckTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	_, err := driver.Execute(ctx, &ProtocolRequest{
		CommandType: CommandTypeCommands,
		Payload:     []string{"show clock"}, // 更通用的命令
	})
	return err == nil
}

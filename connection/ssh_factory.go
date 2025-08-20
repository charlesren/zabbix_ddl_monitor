package connection

import (
	"context"
	"fmt"

	"golang.org/x/crypto/ssh"
)

type SSHFactory struct{}

func (f *SSHFactory) Create(config EnhancedConnectionConfig) (ProtocolDriver, error) {
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

	return NewSSHDriver(session), nil
}

func (f *SSHFactory) HealthCheck(driver ProtocolDriver) bool {
	_, err := driver.Execute(context.Background(), &ProtocolRequest{
		CommandType: CommandTypeCommands,
		Payload:     []string{"echo healthcheck"},
	})
	return err == nil
}

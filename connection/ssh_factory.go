package connection

import (
	"fmt"
	"time"

	"golang.org/x/crypto/ssh"
)

type SSHFactory struct{}

func (f *SSHFactory) Create(config ConnectionConfig) (ProtocolDriver, error) {
	// 设置默认值
	if config.Port == 0 {
		config.Port = 22
	}
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}

	// 单步建立SSH会话
	client, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", config.IP, config.Port),
		&ssh.ClientConfig{
			User:            config.Username,
			Auth:            []ssh.AuthMethod{ssh.Password(config.Password)},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         config.Timeout,
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
	_, err := driver.Execute(&ProtocolRequest{
		CommandType: CommandTypeCommands,
		Payload:     []string{"echo healthcheck"},
	})
	return err == nil
}

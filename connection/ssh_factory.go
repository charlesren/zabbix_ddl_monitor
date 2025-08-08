package connection

import (
	"github.com/charlesren/zabbix_ddl_monitor/syncer"
	"golang.org/x/crypto/ssh"
)

type SSHFactory struct{}

func (f *SSHFactory) Create(router *syncer.Router) (ProtocolDriver, error) {
	config := &ssh.ClientConfig{
		User:            router.Username,
		Auth:            []ssh.AuthMethod{ssh.Password(router.Password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	client, err := ssh.Dial("tcp", router.IP+":22", config)
	if err != nil {
		return nil, err
	}
	session, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	return NewSSHDriver(session), nil
}

func (f *SSHFactory) HealthCheck(driver ProtocolDriver) bool {
	_, err := driver.Execute(&ProtocolRequest{
		CommandType: TypeCommands,
		Payload:     []string{"echo test"},
	})
	return err == nil
}

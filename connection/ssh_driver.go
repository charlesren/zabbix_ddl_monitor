package connection

import (
	"strings"

	"golang.org/x/crypto/ssh"
)

type SSHDriver struct {
	session *ssh.Session
}

func NewSSHDriver(s *ssh.Session) *SSHDriver {
	return &SSHDriver{session: s}
}

func (d *SSHDriver) ProtocolType() string {
	return "ssh"
}

func (d *SSHDriver) SendCommands(commands []string) (string, error) {
	return d.session.Output(strings.Join(commands, ";"))
}

func (d *SSHDriver) Close() error {
	return d.session.Close()
}

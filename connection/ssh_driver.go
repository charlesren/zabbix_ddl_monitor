package connection

import (
	"bytes"
	"fmt"

	"golang.org/x/crypto/ssh"
)

type SSHDriver struct {
	client  *ssh.Client
	session *ssh.Session
}

func NewSSHDriver(s *ssh.Session) *SSHDriver {
	return &SSHDriver{session: s}
}

func (d *SSHDriver) ProtocolType() string {
	return "ssh"
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

func (d *SSHDriver) Close() error {
	return d.session.Close()
}

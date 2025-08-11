package connection

import (
	"bytes"
	"fmt"
	"time"

	"github.com/scrapli/scrapligo/channel"
	"golang.org/x/crypto/ssh"
)

type SSHDriver struct {
	session *ssh.Session
}

func NewSSHDriver(session *ssh.Session) *SSHDriver {
	return &SSHDriver{session: session}
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
func (d *SSHDriver) Execute(req *ProtocolRequest) (*ProtocolResponse, error) {
	if req.CommandType != CommandTypeCommands {
		return nil, ErrUnsupportedCommandType
	}

	cmds, ok := req.Payload.([]string)
	if !ok {
		return nil, fmt.Errorf("invalid commands payload")
	}

	output, err := d.SendCommands(cmds)
	return &ProtocolResponse{
		Success:    err == nil,
		RawData:    []byte(output),
		Structured: nil, // SSH无结构化响应
	}, err
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
		Timeout:       15 * time.Second,
	}
}

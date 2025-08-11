//go:build integration
// +build integration

package connection

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScrapliDriver_RealDevice(t *testing.T) {
	t.Skip("需要配置真实设备进行测试")

	driver := NewScrapliDriver("cisco_iosxe", "192.168.1.1", "admin", "password")
	err := driver.Connect()
	require.NoError(t, err)
	defer driver.Close()

	t.Run("should execute show commands", func(t *testing.T) {
		resp, err := driver.Execute(&ProtocolRequest{
			CommandType: CommandTypeCommands,
			Payload:     []string{"show version"},
		})

		require.NoError(t, err)
		assert.Contains(t, string(resp.RawData), "Cisco IOS")
	})
}

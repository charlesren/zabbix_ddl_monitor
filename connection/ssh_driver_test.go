package connection

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSSHDriver_Execute_Mock(t *testing.T) {
	driver := &MockProtocolDriver{
		ExecuteFunc: func(req *ProtocolRequest) (*ProtocolResponse, error) {
			if req.CommandType != CommandTypeCommands {
				return nil, ErrUnsupportedCommandType
			}
			return &ProtocolResponse{
				Success: true,
				RawData: []byte("mock output"),
			}, nil
		},
	}

	t.Run("should execute commands via mock", func(t *testing.T) {
		resp, err := driver.Execute(&ProtocolRequest{
			CommandType: CommandTypeCommands,
			Payload:     []string{"echo test"},
		})
		assert.NoError(t, err)
		assert.True(t, resp.Success)
		assert.Contains(t, string(resp.RawData), "mock output")
	})

	t.Run("should return error for invalid command type", func(t *testing.T) {
		_, err := driver.Execute(&ProtocolRequest{
			CommandType: "invalid",
		})
		assert.ErrorIs(t, err, ErrUnsupportedCommandType)
	})
}

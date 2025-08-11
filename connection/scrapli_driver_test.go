package connection

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestScrapliDriver_ProtocolType(t *testing.T) {
	driver := &ScrapliDriver{}
	assert.Equal(t, ProtocolScrapli, driver.ProtocolType())
}

func TestScrapliDriver_GetCapability(t *testing.T) {
	driver := &ScrapliDriver{platform: "cisco_iosxe"}
	caps := driver.GetCapability()
	assert.Equal(t, ProtocolScrapli, caps.Protocol)
}

package connection

import (
	"fmt"

	"github.com/charlesren/ylog"
)

// connection/scrapli_factory.go
type ScrapliFactory struct{}

func (f *ScrapliFactory) Create(config ConnectionConfig) (ProtocolDriver, error) {
	ylog.Debugf("scrapli", "creating driver with config: %+v", config)

	platform, _ := config.Metadata["platform"].(string)
	driver := NewScrapliDriver(platform, config.IP, config.Username, config.Password)
	if err := driver.Connect(); err != nil {
		return nil, fmt.Errorf("create platform failed: %w", err) // 更清晰的错误信息
	}
	return driver, nil
}

func (f *ScrapliFactory) HealthCheck(driver ProtocolDriver) bool {
	scrapliDriver, ok := driver.(*ScrapliDriver)
	if !ok {
		return false
	}
	_, err := scrapliDriver.GetPrompt()
	return err == nil
}

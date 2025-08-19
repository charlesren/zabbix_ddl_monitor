package connection

import (
	"fmt"

	"github.com/charlesren/ylog"
	"github.com/scrapli/scrapligo/driver/options"
	"github.com/scrapli/scrapligo/platform"
)

// connection/scrapli_factory.go
type ScrapliFactory struct{}

func (f *ScrapliFactory) Create(config ConnectionConfig) (ProtocolDriver, error) {
	ylog.Debugf("scrapli", "creating driver with config: %+v", config)

	platformOS, _ := config.Metadata["platform"].(string)
	ylog.Debugf("scrapli", "platformOS: %s", platformOS)
	ylog.Debugf("scrapli", "ip: %s", config.IP)
	ylog.Debugf("scrapli", "username: %s", config.Username)
	ylog.Debugf("scrapli", "password: %s", config.Password)
	p, err := platform.NewPlatform(
		platformOS,
		config.IP,
		options.WithAuthNoStrictKey(),
		options.WithAuthUsername(config.Username),
		options.WithAuthPassword(config.Password),
	)
	if err != nil {
		return nil, fmt.Errorf("create platform failed: %w", err)
	}

	driver, err := p.GetNetworkDriver()
	if err != nil {
		return nil, fmt.Errorf("get network driver failed: %w", err)
	}

	if err := driver.Open(); err != nil {
		return nil, fmt.Errorf("open connection failed: %w", err)
	}

	return &ScrapliDriver{
		driver:  driver,
		channel: driver.Channel,
		host:    config.IP,
	}, nil

}

func (f *ScrapliFactory) HealthCheck(driver ProtocolDriver) bool {
	scrapliDriver, ok := driver.(*ScrapliDriver)
	if !ok {
		return false
	}
	_, err := scrapliDriver.GetPrompt()
	return err == nil
}

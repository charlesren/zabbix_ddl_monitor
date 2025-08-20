package connection

import (
	"fmt"
	"time"

	"github.com/charlesren/ylog"
	"github.com/scrapli/scrapligo/driver/options"
	"github.com/scrapli/scrapligo/platform"
)

// connection/scrapli_factory.go
type ScrapliFactory struct{}

func (f *ScrapliFactory) Create(config EnhancedConnectionConfig) (ProtocolDriver, error) {
	ylog.Debugf("scrapli", "creating driver with config: %+v", config)

	// 1. 检查platform字段存在性
	platformValue, exists := config.Metadata["platform"]
	if !exists {
		return nil, fmt.Errorf("metadata 'platform' field missing")
	}

	// 处理所有可能的输入类型
	var platformOS string
	switch v := platformValue.(type) {
	case string:
		platformOS = v
	case Platform:
		platformOS = string(v) // 显式类型转换
	case []byte:
		platformOS = string(v)
	case fmt.Stringer:
		platformOS = v.String()
	default:
		return nil, fmt.Errorf("unsupported platform type: %T", v)
	}
	//  空值检查
	if string(platformOS) == "" {
		return nil, fmt.Errorf("platform cannot be empty")
	}
	ylog.Debugf("scrapli", "platformOS: %s", platformOS)
	ylog.Debugf("scrapli", "ip: %s", config.Host)
	ylog.Debugf("scrapli", "username: %s", config.Username)
	ylog.Debugf("scrapli", "password: %s", config.Password)
	p, err := platform.NewPlatform(
		platformOS,
		config.Host,
		options.WithAuthNoStrictKey(),
		options.WithAuthUsername(config.Username),
		options.WithAuthPassword(config.Password),
		options.WithTimeoutOps(30*time.Second),
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
	ylog.Debugf("scrapli", "driver created with config: %+v", config)

	return &ScrapliDriver{
		driver:  driver,
		channel: driver.Channel,
		host:    config.Host,
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

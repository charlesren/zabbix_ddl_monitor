package connection

import (
	"context"
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
		options.WithTimeoutOps(60*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("create platform failed: %w", err)
	}

	driver, err := p.GetNetworkDriver()
	if err != nil {
		return nil, fmt.Errorf("get network driver failed: %w", err)
	}

	// 创建context，确保与driver生命周期一致
	ctx, cancel := context.WithCancel(context.Background())

	// 创建driver实例但不立即打开连接
	scrapliDriver := &ScrapliDriver{
		driver:     driver,
		channel:    nil, // channel将在Connect()时设置
		host:       config.Host,
		username:   config.Username,
		password:   config.Password,
		platform:   platformOS,
		maxRetries: 3,
		timeout:    config.ConnectTimeout,
		ctx:        ctx,
		cancel:     cancel,
	}

	ylog.Debugf("scrapli", "driver created with config: %+v", config)
	return scrapliDriver, nil

}

func (f *ScrapliFactory) HealthCheck(driver ProtocolDriver) bool {
	scrapliDriver, ok := driver.(*ScrapliDriver)
	if !ok {
		return false
	}
	_, err := scrapliDriver.GetPrompt()
	return err == nil
}

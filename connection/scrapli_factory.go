package connection

import "github.com/charlesren/zabbix_ddl_monitor/syncer"

// connection/scrapli_factory.go
type ScrapliFactory struct{}

func (f *ScrapliFactory) Create(router *syncer.Router) (ProtocolDriver, error) {
	driver := NewScrapliDriver(router.Platform, router.IP, router.Username, router.Password)
	if err := driver.Connect(); err != nil {
		return nil, err
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

package connection

// connection/scrapli_factory.go
type ScrapliFactory struct{}

func (f *ScrapliFactory) Create(config ConnectionConfig) (ProtocolDriver, error) {
	platform, _ := config.Metadata["platform"].(string)
	driver := NewScrapliDriver(platform, config.IP, config.Username, config.Password)
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

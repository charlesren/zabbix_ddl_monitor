package connection

type ProtocolFactory interface {
	Create(config EnhancedConnectionConfig) (ProtocolDriver, error)
	HealthCheck(driver ProtocolDriver) bool
}

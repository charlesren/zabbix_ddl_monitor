package connection

type ProtocolFactory interface {
	Create(config ConnectionConfig) (ProtocolDriver, error)
	HealthCheck(driver ProtocolDriver) bool
}

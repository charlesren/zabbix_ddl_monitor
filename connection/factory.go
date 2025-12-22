package connection

import "context"

type ProtocolFactory interface {
	Create(config EnhancedConnectionConfig) (ProtocolDriver, error)
	CreateWithContext(ctx context.Context, config EnhancedConnectionConfig) (ProtocolDriver, error)
	HealthCheck(driver ProtocolDriver) bool
}

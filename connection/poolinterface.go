package connection

type ConnectionPoolInterface interface {
	Get(protocol Protocol) (ProtocolDriver, error)
	Release(driver ProtocolDriver) error
	WarmUp(protocol Protocol, count int) error
	Close() error
}

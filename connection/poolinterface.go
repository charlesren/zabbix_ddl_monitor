package connection

type ConnectionPoolInterface interface {
	Get(protocol Protocol) (ProtocolDriver, error)
	Release(driver ProtocolDriver) error
	WarmUp(protocol Protocol, count int) error
	Close() error
	GetEventChan() <-chan PoolEvent
}

/*
  	type SshProtocolDriver interface {
  		ProtocolDriver
  		SendCommands(commands []string) (string, error)
  	}

	type ScrapliProtocolDriver interface {
 		ProtocolDriver
 		SendInteractive(events []*channel.SendInteractiveEvent, opts ...util.Option) (*response.Response, error)
 		SendCommand(command string, opts ...util.Option) (*response.Response, error)
		SendCommands(commands []string, opts ...util.Option) (*response.MultiResponse, error)
 		SendConfig(config string, opts ...util.Option) (*response.Response, error)
 		SendConfigs(configs []string, opts ...util.Option) (*response.MultiResponse, error)
 	}
*/

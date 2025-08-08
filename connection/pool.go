package connection

import (
	"fmt"
	"sync"
	"time"

	"github.com/charlesren/zabbix_ddl_monitor/syncer"
)

type (
	Platform    string
	Protocol    string
	CommandType string
)

const (
	CiscoIOSXE Platform = "cisco_iosxe"
	CiscoIOSXR Platform = "cisco_iosxr"
	CiscoNXOS  Platform = "cisco_nxos"
	H3CComware Platform = "h3c_comware"
	HuaweiVRP  Platform = "huawei_vrp"

	ProtocolSSH          Protocol    = "ssh"
	ProtocolScrapli      Protocol    = "scrapli"
	TypeCommands         CommandType = "commands"
	TypeInteractiveEvent CommandType = "interactive_event"
)

// connection/interfaces.go
type ProtocolDriver interface {
	ProtocolType() Protocol
	Close() error
	Execute(req *ProtocolRequest) (*ProtocolResponse, error)
	GetCapability() ProtocolCapability
}

type ProtocolRequest struct {
	CommandType CommandType // commands/interactive_event
	Payload     interface{} // []string 或 []*channel.SendInteractiveEvent
	Timeout     time.Duration
}

type ProtocolResponse struct {
	Success    bool
	RawData    []byte
	Structured interface{} // *response.Response 或 *response.MultiResponse
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
type ConnectionPool struct {
	router    *syncer.Router               // 源头Router
	factories map[Protocol]ProtocolFactory // 协议工厂映射
	pools     map[Protocol]*DriverPool     // 各协议的连接池
	mu        sync.RWMutex
}

type DriverPool struct {
	connections map[string]ProtocolDriver // key为IP或唯一标识
	factory     ProtocolFactory
	mu          sync.Mutex
}

// connection/pool.go
func NewConnectionPool(router *syncer.Router) *ConnectionPool {
	pool := &ConnectionPool{
		router:    router,
		factories: make(map[Protocol]ProtocolFactory),
		pools:     make(map[Protocol]*DriverPool),
	}
	// 注册默认工厂
	pool.RegisterFactory(ProtocolSSH, &SSHFactory{})
	pool.RegisterFactory(ProtocolScrapli, &ScrapliFactory{})
	return pool
}

func (p *ConnectionPool) RegisterFactory(proto Protocol, factory ProtocolFactory) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.factories[proto] = factory
	p.pools[proto] = &DriverPool{
		connections: make(map[string]ProtocolDriver),
		factory:     factory,
	}
}

// connection/pool.go
func (p *ConnectionPool) Get(proto Protocol) (ProtocolDriver, error) {
	p.mu.RLock()
	pool, exists := p.pools[proto]
	p.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("unsupported protocol: %s", proto)
	}

	pool.mu.Lock()
	defer pool.mu.Unlock()

	// 1. 尝试复用现有连接
	if conn, exists := pool.connections[p.router.IP]; exists {
		if pool.factory.HealthCheck(conn) {
			return conn, nil
		}
		_ = conn.Close() // 清理失效连接
	}

	// 2. 创建新连接
	conn, err := pool.factory.Create(p.router)
	if err != nil {
		return nil, err
	}
	pool.connections[p.router.IP] = conn
	return conn, nil
}

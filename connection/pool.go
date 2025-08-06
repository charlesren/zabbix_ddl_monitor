package connection

import (
	"sync"

	"github.com/scrapli/scrapligo/driver/network"
	"golang.org/x/crypto/ssh"
)

// ProtocolDriver 基础协议接口
type ProtocolDriver interface {
	SendCommands(commands []string) (string, error)
	ProtocolType() string
	Close() error
}

// EnhancedDriver 轻量驱动包装器（未来可扩展）
type EnhancedDriver struct {
	ProtocolDriver
}

// ConnectionPool 连接池实现
type ConnectionPool struct {
	scrapliPool *DriverPool[*network.Driver]
	sshPool     *DriverPool[*ssh.Session]
	mu          sync.Mutex
}

// NewConnectionPool 创建连接池（需传入实际初始化逻辑）
func NewConnectionPool(
	scrapliInitFunc func() (*network.Driver, error),
	sshInitFunc func() (*ssh.Session, error),
) *ConnectionPool {
	return &ConnectionPool{
		scrapliPool: NewDriverPool(scrapliInitFunc),
		sshPool:     NewDriverPool(sshInitFunc),
	}
}

// GetDriver 获取驱动（自动管理连接）
func (p *ConnectionPool) GetDriver(host, protocol string, opts ...DriverOption) (ProtocolDriver, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	switch protocol {
	case "scrapli":
		driver, err := p.scrapliPool.Get(host)
		if err != nil {
			return nil, err
		}
		return &ScrapliDriver{driver: driver, channel: driver.Channel}, nil

	case "ssh":
		session, err := p.sshPool.Get(host)
		if err != nil {
			return nil, err
		}
		return &SSHDriver{session: session}, nil

	default:
		return nil, errors.New("unsupported protocol")
	}
}



// Get 获取驱动（返回EnhancedDriver包装实例）
func (p *ConnectionPool) Get(ip, protocol string) *EnhancedDriver {
	p.mu.Lock()
	defer p.mu.Unlock()

	var baseDriver ProtocolDriver
	switch protocol {
	case "scrapli":
		driver, _ := p.scrapliPool.Get(ip)
		baseDriver = &ScrapliDriver{driver: driver, channel: driver.Channel}
	case "ssh":
		session, _ := p.sshPool.Get(ip)
		baseDriver = &SSHDriver{session: session}
	default:
		panic("unsupported protocol: " + protocol)
	}

	return &EnhancedDriver{ProtocolDriver: baseDriver}
}
// DriverOption 驱动配置函数
type DriverOption func(interface{})

// DriverPool 通用驱动池（泛型实现）
type DriverPool[T any] struct {
	pool   map[string]T
	create func() (T, error)
	mu     sync.Mutex
}

func NewDriverPool[T any](createFn func() (T, error)) *DriverPool[T] {
	return &DriverPool[T]{
		pool:   make(map[string]T),
		create: createFn,
	}
}

func (p *DriverPool[T]) Get(key string) (T, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if conn, ok := p.pool[key]; ok {
		return conn, nil
	}

	conn, err := p.create()
	if err != nil {
		return conn, err
	}
	p.pool[key] = conn
	return conn, nil
}


scrapliInit := func() (*network.Driver, error) {
		return network.NewDriver(
			"", // 实际使用时替换为host
			options.WithAuthUsername("admin"),
			options.WithAuthPassword("password"),
			options.WithTimeoutOps(30*time.Second),
		)
	}

	sshInit := func() (*ssh.Session, error) {
		// 实现SSH连接逻辑
		config := &ssh.ClientConfig{
			User: "admin",
			Auth: []ssh.AuthMethod{
				ssh.Password("password"),
			},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		}
		client, err := ssh.Dial("tcp", "host:22", config)
		if err != nil {
			return nil, err
		}
		return client.NewSession()
	}

package connection

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"

	"time"

	"github.com/charlesren/ylog"
)

// connection/interfaces.go
type ProtocolDriver interface {
	ProtocolType() Protocol
	Close() error
	Execute(ctx context.Context, req *ProtocolRequest) (*ProtocolResponse, error)
	GetCapability() ProtocolCapability
}

type ProtocolRequest struct {
	CommandType CommandType // commands/interactive_event
	Payload     interface{} // []string 或 []*channel.SendInteractiveEvent
	// Timeout     time.Duration
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

type pooledConnection struct {
	driver    ProtocolDriver
	createdAt time.Time
	lastUsed  time.Time
	valid     bool
	inUse     bool
	id        string
}

type DriverPool struct {
	connections map[string]*pooledConnection
	factory     ProtocolFactory
	mu          sync.Mutex
}

type ConnectionPool struct {
	config      ConnectionConfig
	factories   map[Protocol]ProtocolFactory
	pools       map[Protocol]*DriverPool
	mu          sync.RWMutex
	idleTimeout time.Duration
	ctx         context.Context
	cancel      context.CancelFunc
	debugMode   bool
	activeConns map[string]string // connID -> stack trace
}

// NewConnectionPool 创建带生命周期管理的新连接池
func NewConnectionPool(config ConnectionConfig) *ConnectionPool {
	ctx, cancel := context.WithCancel(context.Background())
	pool := &ConnectionPool{
		config:      config,
		factories:   make(map[Protocol]ProtocolFactory),
		pools:       make(map[Protocol]*DriverPool),
		idleTimeout: 5 * time.Minute,
		ctx:         ctx,
		cancel:      cancel,
		debugMode:   false, // 默认关闭调试
		activeConns: make(map[string]string),
	}

	// 注册默认协议工厂
	pool.RegisterFactory(ProtocolSSH, &SSHFactory{})
	pool.RegisterFactory(ProtocolScrapli, &ScrapliFactory{})

	// 启动后台维护协程
	pool.startBackgroundTasks()
	return pool
}

// EnableDebug 开启调试模式（连接泄漏检测）
func (p *ConnectionPool) EnableDebug() {
	p.debugMode = true
}

// RegisterFactory 注册协议工厂
func (p *ConnectionPool) RegisterFactory(proto Protocol, factory ProtocolFactory) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.factories[proto] = factory
	p.pools[proto] = &DriverPool{
		connections: make(map[string]*pooledConnection),
		factory:     factory,
	}
}

// Get 获取连接（自动维护连接状态）
func (p *ConnectionPool) Get(proto Protocol) (ProtocolDriver, error) {
	pool := p.getDriverPool(proto)
	if pool == nil {
		return nil, fmt.Errorf("unsupported protocol: %s", proto)
	}

	pool.mu.Lock()
	defer pool.mu.Unlock()

	// 1. 尝试复用有效空闲连接
	for _, conn := range pool.connections {
		if !conn.inUse && conn.valid {
			// 新增健康检查
			if healthy := pool.factory.HealthCheck(conn.driver); !healthy {
				conn.valid = false
				_ = conn.driver.Close()
				continue
			}
			return p.activateConnection(conn), nil
		}
	}

	// 2. 创建新连接
	conn, err := p.createConnection(pool, proto)
	if err != nil {
		return nil, err
	}
	pool.connections[conn.id] = conn
	return p.activateConnection(conn), nil
}

// Release 释放连接回池
func (p *ConnectionPool) Release(driver ProtocolDriver) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, pool := range p.pools {
		pool.mu.Lock()
		for id, conn := range pool.connections {
			if conn.driver == driver {
				conn.inUse = false
				conn.lastUsed = time.Now()

				if p.debugMode {
					delete(p.activeConns, id)
				}
				pool.mu.Unlock()
				return nil
			}
		}
		pool.mu.Unlock()
	}
	return fmt.Errorf("connection not found in pool")
}

// WarmUp 预热连接池
func (p *ConnectionPool) WarmUp(proto Protocol, count int) error {
	pool := p.getDriverPool(proto)
	if pool == nil {
		return fmt.Errorf("protocol %s not registered", proto)
	}

	for i := 0; i < count; i++ {
		conn, err := p.createConnection(pool, proto)
		if err != nil {
			return err
		}
		pool.connections[conn.id] = conn
	}
	return nil
}

// HealthCheck 返回各协议健康连接数
func (p *ConnectionPool) HealthCheck() map[Protocol]int {
	stats := make(map[Protocol]int)
	p.mu.RLock()
	defer p.mu.RUnlock()

	for proto, pool := range p.pools {
		pool.mu.Lock()
		healthy := 0
		for _, conn := range pool.connections {
			if !conn.valid || conn.inUse {
				continue // Skip invalid or in-use connections
			}

			// Perform a deep health check (e.g., test the connection)
			if driver, ok := conn.driver.(*ScrapliDriver); ok {
				_, err := driver.GetPrompt() // Simple test to verify the connection is alive
				if err != nil {
					ylog.Debugf("pool", "connection health check failed for %s: %v", proto, err)
					conn.valid = false // Mark as invalid
					continue
				}
			}

			healthy++
		}
		pool.mu.Unlock()
		stats[proto] = healthy
	}
	return stats
}

// Close 优雅关闭连接池
func (p *ConnectionPool) Close() error {
	p.cancel() // 停止后台协程

	p.mu.Lock()
	defer p.mu.Unlock()

	var errs []error
	for _, pool := range p.pools {
		pool.mu.Lock()
		for _, conn := range pool.connections {
			if err := conn.driver.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		pool.connections = make(map[string]*pooledConnection)
		pool.mu.Unlock()
	}

	if len(errs) > 0 {
		return fmt.Errorf("close errors: %v", errs)
	}
	return nil
}

// CheckLeaks 返回泄漏连接信息（调试模式）
func (p *ConnectionPool) CheckLeaks() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var leaks []string
	for id, stack := range p.activeConns {
		leaks = append(leaks, fmt.Sprintf("LEAK %s:\n%s", id, stack))
	}
	return leaks
}

// --- 私有方法 ---
func (p *ConnectionPool) startBackgroundTasks() {
	// 心跳保活
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-p.ctx.Done():
				return
			case <-ticker.C:
				p.checkConnectionsHealth()
			}
		}
	}()

	// 空闲连接清理
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-p.ctx.Done():
				return
			case <-ticker.C:
				p.cleanupIdleConnections()
			}
		}
	}()
}

func (p *ConnectionPool) activateConnection(conn *pooledConnection) ProtocolDriver {
	conn.inUse = true
	conn.lastUsed = time.Now()

	if p.debugMode {
		p.activeConns[conn.id] = string(debug.Stack())
	}

	return &monitoredDriver{
		ProtocolDriver: conn.driver,
		releaseFunc: func() {
			p.Release(conn.driver)
		},
	}
}

func (p *ConnectionPool) createConnection(pool *DriverPool, proto Protocol) (*pooledConnection, error) {
	//	driver, err := pool.factory.Create(p.config)
	//if err != nil {
	//return nil, err
	//}

	return &pooledConnection{
		//		driver:    driver,
		createdAt: time.Now(),
		lastUsed:  time.Now(),
		valid:     true,
		id:        fmt.Sprintf("%s|%s|%d", p.config.IP, proto, time.Now().UnixNano()),
	}, nil
}

func (p *ConnectionPool) checkConnectionsHealth() {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, pool := range p.pools {
		pool.mu.Lock()
		for _, conn := range pool.connections {
			if !conn.inUse && time.Since(conn.lastUsed) > p.idleTimeout/2 {
				/*
					if _, err := conn.driver.Execute(&ProtocolRequest{
						CommandType: CommandTypeCommands,
						Payload:     []string{"echo healthcheck"},
					}); err != nil {
						conn.valid = false
					}
				*/
			}
		}
		pool.mu.Unlock()
	}
}

func (p *ConnectionPool) cleanupIdleConnections() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, pool := range p.pools {
		pool.mu.Lock()
		for id, conn := range pool.connections {
			if !conn.inUse && time.Since(conn.lastUsed) > p.idleTimeout {
				conn.driver.Close()
				delete(pool.connections, id)

				if p.debugMode {
					delete(p.activeConns, id)
				}
			}
		}
		pool.mu.Unlock()
	}
}

func (p *ConnectionPool) getDriverPool(proto Protocol) *DriverPool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pools[proto]
}

// monitoredDriver 包装器实现
type monitoredDriver struct {
	ProtocolDriver
	releaseFunc func()
}

func (m *monitoredDriver) Close() error {
	defer m.releaseFunc()
	return m.ProtocolDriver.Close()
}

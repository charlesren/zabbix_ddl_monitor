package router

import (
	"sync"

	"github.com/scrapli/scrapligo/driver/network"
	"github.com/yourusername/zabbix_ddl_monitor/internal/config"
)

type ConnectionPool struct {
	mu          sync.Mutex
	connections map[string]*network.Driver
}

func NewConnectionPool() *ConnectionPool {
	return &ConnectionPool{
		connections: make(map[string]*network.Driver),
	}
}

func (p *ConnectionPool) GetConnection(router config.Router) (*network.Driver, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if conn, exists := p.connections[router.IP]; exists {
		return conn, nil
	}

	conn, err := network.NewDriver(
		router.IP,
		network.WithAuthUsername(router.Username),
		network.WithAuthPassword(router.Password),
		network.WithPlatform(router.Platform),
	)
	if err != nil {
		return nil, err
	}

	p.connections[router.IP] = conn
	return conn, nil
}

func (p *ConnectionPool) ReleaseConnection(conn *network.Driver) {
	// 连接保留在池中供复用
}

func (p *ConnectionPool) GetCachedConnection(ip string) (*network.Driver, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	conn, ok := p.connections[ip]
	return conn, ok
}

package connection

import (
	"context"
	"sync"
	"time"

	"github.com/charlesren/zabbix_ddl_monitor/connection"
	"github.com/charlesren/zabbix_ddl_monitor/syncer"
	"github.com/scrapli/scrapligo/driver/network"
)

type ConnectionPool interface {
	Get(routerIP string, protocol string) (ProtocolDriver, error)
	Release(driver ProtocolDriver)
}
type ProtocolDriver interface {
	SendCommands(commands []string) (string, error)
	Close() error
}

type ScrapliDriver struct {
	channel *scrapligo.Channel
}

/*
type SSHDriver struct {
	session *ssh.Session
}
*/
// NETCONF实现（示例）
type NETCONFDriver struct {
	session netconf.Session
}

type Connection struct {
	driver   *network.Driver
	config   Router
	mu       sync.Mutex
	lastUsed time.Time // 用于空闲检测
}

func NewConnection(router *syncer.Router) *Connection {
	var driver connection.ProtocolDriver
	switch router.Protocol {
	case "scrapli":
		driver = &ScrapliDriver{router: router}
	case "ssh":
		driver = &SSHDriver{router: router}
	case "netconf":
		driver = &NETCONFDriver{router: router}
	default:
		panic("unsupported protocol: " + router.Protocol)
	}
	return &Connection{driver: driver}
}

// 获取连接（带懒加载和心跳检测）
func (c *Connection) Get() (*network.Driver, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 心跳检测（示例：10秒无活动则重建连接）
	if c.driver != nil && time.Since(c.lastUsed) > 10*time.Second {
		_ = c.driver.Close()
		c.driver = nil
	}

	if c.driver == nil {
		driver, err := network.NewDriver(
			c.config.IP,
			network.WithAuthUsername(c.config.Username),
			network.WithAuthPassword(c.config.Password),
			network.WithPlatform(c.config.Platform),
			network.WithTimeoutOps(30*time.Second),
		)
		if err != nil {
			return nil, err
		}
		c.driver = driver
	}

	c.lastUsed = time.Now()
	return c.driver, nil
}

// 安全关闭连接
func (c *Connection) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.driver == nil {
		return nil
	}
	err := c.driver.Close()
	c.driver = nil
	return err
}

// 支持带上下文的连接获取
func (p *ConnectionPool) GetWithRetry(
	ctx context.Context,
	maxRetries int,
	interval time.Duration,
) (*Connection, error) {
}

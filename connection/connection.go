package connection

import (
	"sync"
	"time"

	"github.com/scrapli/scrapligo/driver/network"
)

type Connection struct {
	driver   *network.Driver
	config   Router
	mu       sync.Mutex
	lastUsed time.Time // 用于空闲检测
}

func NewConnection(config Router) *Connection {
	return &Connection{config: config}
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

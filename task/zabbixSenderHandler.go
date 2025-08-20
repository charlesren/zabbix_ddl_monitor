package task

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/charlesren/ylog"
	"github.com/charlesren/zapix/sender"
)

type ZabbixSenderConfig struct {
	ProxyIP           string        `yaml:"proxyip"`
	ProxyPort         string        `yaml:"proxyport"`
	ConnectionTimeout time.Duration `yaml:"connection_timeout"`
	ReadTimeout       time.Duration `yaml:"read_timeout"`
	WriteTimeout      time.Duration `yaml:"write_timeout"`
	PoolSize          int           `yaml:"pool_size"`
}

// 添加默认超时设置
func (c *ZabbixSenderConfig) SetDefaults() {
	if c.ConnectionTimeout == 0 {
		c.ConnectionTimeout = 5 * time.Second
	}
	if c.ReadTimeout == 0 {
		c.ReadTimeout = 15 * time.Second
	}
	if c.WriteTimeout == 0 {
		c.WriteTimeout = 5 * time.Second
	}
}

type ZabbixSenderHandler struct {
	sender     *sender.Sender
	config     ZabbixSenderConfig
	serverAddr string
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	stopChan   chan struct{}
}

func NewZabbixSenderHandler(config ZabbixSenderConfig) (*ZabbixSenderHandler, error) {
	config.SetDefaults()
	serverAddr := net.JoinHostPort(config.ProxyIP, config.ProxyPort)

	ylog.Infof("zabbix_sender", "creating zabbix sender handler for %s with pool size %d", serverAddr, config.PoolSize)
	ylog.Debugf("zabbix_sender", "connection timeout: %v, read timeout: %v, write timeout: %v",
		config.ConnectionTimeout, config.ReadTimeout, config.WriteTimeout)

	zabbixSender := sender.NewSender(
		serverAddr,
		config.ConnectionTimeout,
		config.ReadTimeout,
		config.WriteTimeout,
		config.PoolSize,
	)

	ctx, cancel := context.WithCancel(context.Background())
	handler := &ZabbixSenderHandler{
		sender:     zabbixSender,
		config:     config,
		serverAddr: serverAddr,
		ctx:        ctx,
		cancel:     cancel,
		stopChan:   make(chan struct{}),
	}

	// Test connection during initialization
	if err := handler.TestConnection(); err != nil {
		ylog.Warnf("zabbix_sender", "initial connection test failed for %s: %v", serverAddr, err)
	} else {
		ylog.Infof("zabbix_sender", "connection test successful for %s", serverAddr)
	}

	// Start connection health monitor
	handler.StartHealthMonitor()

	ylog.Infof("zabbix_sender", "zabbix sender handler created successfully for %s", serverAddr)
	return handler, nil
}

// TestConnection tests the connection to Zabbix server
func (h *ZabbixSenderHandler) TestConnection() error {
	testPacket := &sender.Packet{
		Request: "sender data",
		Data:    []*sender.Metric{},
	}

	start := time.Now()
	_, err := h.sender.Send(testPacket)
	duration := time.Since(start)

	if err != nil {
		ylog.Errorf("zabbix_sender", "connection test failed for %s after %v: %v", h.serverAddr, duration, err)
		return fmt.Errorf("connection test failed: %w", err)
	}

	ylog.Infof("zabbix_sender", "connection test successful for %s in %v", h.serverAddr, duration)
	return nil
}

// StartHealthMonitor starts a background goroutine to monitor connection health
func (h *ZabbixSenderHandler) StartHealthMonitor() {
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		ylog.Debugf("zabbix_sender", "connection health monitor started for %s", h.serverAddr)

		for {
			select {
			case <-ticker.C:
				if err := h.TestConnection(); err != nil {
					ylog.Warnf("zabbix_sender", "periodic connection health check failed for %s", h.serverAddr)
				} else {
					ylog.Debugf("zabbix_sender", "periodic connection health check passed for %s", h.serverAddr)
				}
			case <-h.stopChan:
				ylog.Debugf("zabbix_sender", "connection health monitor stopped for %s", h.serverAddr)
				return
			case <-h.ctx.Done():
				ylog.Debugf("zabbix_sender", "connection health monitor cancelled for %s", h.serverAddr)
				return
			}
		}
	}()
}

// Close cleans up resources and stops background goroutines
func (h *ZabbixSenderHandler) Close() error {
	ylog.Infof("zabbix_sender", "closing zabbix sender handler for %s", h.serverAddr)

	// Stop health monitor
	close(h.stopChan)

	// Cancel context
	h.cancel()

	// Wait for background goroutines
	h.wg.Wait()

	// Note: The underlying sender's connection pool connections will be closed
	// when they are garbage collected or when the pool is no longer used
	ylog.Infof("zabbix_sender", "zabbix sender handler closed for %s", h.serverAddr)
	return nil
}

// GetStats returns connection pool statistics
func (h *ZabbixSenderHandler) GetStats() map[string]interface{} {
	stats := h.sender.GetPoolStats()

	// Add additional information to the stats
	stats["server_address"] = h.serverAddr
	stats["timeouts"] = map[string]time.Duration{
		"connection": h.config.ConnectionTimeout,
		"read":       h.config.ReadTimeout,
		"write":      h.config.WriteTimeout,
	}

	return stats
}

// WarmupPool pre-warms the connection pool by establishing initial connections
func (h *ZabbixSenderHandler) WarmupPool() error {
	ylog.Infof("zabbix_sender", "pre-warming connection pool for %s with %d connections", h.serverAddr, h.config.PoolSize)

	connections := make([]net.Conn, 0, h.config.PoolSize)
	defer func() {
		// Clean up any connections that were created
		for _, conn := range connections {
			if conn != nil {
				conn.Close()
			}
		}
	}()

	for i := 0; i < h.config.PoolSize; i++ {
		conn, err := net.DialTimeout("tcp", h.serverAddr, h.config.ConnectionTimeout)
		if err != nil {
			ylog.Warnf("zabbix_sender", "failed to pre-warm connection %d for %s: %v", i+1, h.serverAddr, err)
			continue
		}
		connections = append(connections, conn)
		ylog.Debugf("zabbix_sender", "pre-warmed connection %d for %s", i+1, h.serverAddr)
	}

	successCount := len(connections)
	if successCount > 0 {
		ylog.Infof("zabbix_sender", "successfully pre-warmed %d/%d connections for %s", successCount, h.config.PoolSize, h.serverAddr)
	} else {
		ylog.Warnf("zabbix_sender", "failed to pre-warm any connections for %s", h.serverAddr)
		return fmt.Errorf("failed to pre-warm connections")
	}

	return nil
}

// GetActiveConnectionCount returns the number of active connections in the pool
func (h *ZabbixSenderHandler) GetActiveConnectionCount() int {
	return h.sender.GetActiveConnectionCount()
}

// GetPoolSize returns the configured pool size
func (h *ZabbixSenderHandler) GetPoolSize() int {
	return h.sender.GetPoolSize()
}

func (h *ZabbixSenderHandler) HandleResult(events []ResultEvent) error {
	if len(events) == 0 {
		ylog.Warnf("zabbix_sender", "no events to send to zabbix")
		return nil
	}

	ylog.Infof("zabbix_sender", "processing %d events for zabbix submission", len(events))

	metrics := make([]*sender.Metric, 0, len(events))
	validEvents := 0
	for _, event := range events {
		var value string
		if v, ok := event.Data["value"].(string); ok {
			value = v
			validEvents++
		} else {
			value = ""
			ylog.Warnf("zabbix_sender", "event for host %s has no valid value field", event.IP)
		}
		metrics = append(metrics, &sender.Metric{
			Host:  event.IP,
			Key:   "dedicatedLinePing",
			Value: value,
			Clock: event.Timestamp.Unix(),
		})
	}

	ylog.Debugf("zabbix_sender", "prepared %d metrics (%d valid events) for zabbix", len(metrics), validEvents)

	start := time.Now()
	_, _, err := h.sender.SendMetrics(metrics)
	duration := time.Since(start)

	if err != nil {
		ylog.Errorf("zabbix_sender", "failed to send %d metrics to zabbix after %v: %v", len(metrics), duration, err)
		return fmt.Errorf("zabbix send failed: %w (events=%d)", err, len(events))
	}

	ylog.Infof("zabbix_sender", "successfully sent %d metrics to zabbix in %v", len(metrics), duration)
	return nil
}

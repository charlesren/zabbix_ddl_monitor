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

	// 记录连接测试前的连接池状态
	stats := h.GetStats()
	testActive, _ := stats["active_connections"].(int)
	testIdle, _ := stats["idle_connections"].(int)
	testTotal, _ := stats["total_connections"].(int)
	ylog.Debugf("zabbix_sender", "connection test starting for %s, pool stats: active=%d, idle=%d, total=%d",
		h.serverAddr, testActive, testIdle, testTotal)

	start := time.Now()
	_, err := h.sender.Send(testPacket)
	duration := time.Since(start)

	if err != nil {
		ylog.Errorf("zabbix_sender", "connection test failed for %s after %v: %v", h.serverAddr, duration, err)

		// 记录失败时的详细连接池状态
		failedStats := h.GetStats()
		failedActive, _ := failedStats["active_connections"].(int)
		failedIdle, _ := failedStats["idle_connections"].(int)
		failedTotal, _ := failedStats["total_connections"].(int)
		failedUtilization, _ := failedStats["utilization_percent"].(string)
		ylog.Debugf("zabbix_sender", "connection test failed details - pool: active=%d, idle=%d, total=%d, utilization=%s",
			failedActive, failedIdle, failedTotal, failedUtilization)

		return fmt.Errorf("connection test failed: %w", err)
	}

	ylog.Infof("zabbix_sender", "connection test successful for %s in %v", h.serverAddr, duration)

	// 记录成功时的连接池状态
	successStats := h.GetStats()
	successActive, _ := successStats["active_connections"].(int)
	successIdle, _ := successStats["idle_connections"].(int)
	successTotal, _ := successStats["total_connections"].(int)
	successUtilization, _ := successStats["utilization_percent"].(string)
	ylog.Debugf("zabbix_sender", "connection test successful - pool stats: active=%d, idle=%d, total=%d, utilization=%s",
		successActive, successIdle, successTotal, successUtilization)

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

	// 记录关闭前的连接池状态
	stats := h.GetStats()
	closeActive, _ := stats["active_connections"].(int)
	closeIdle, _ := stats["idle_connections"].(int)
	closeTotal, _ := stats["total_connections"].(int)
	closeUtilization, _ := stats["utilization_percent"].(string)
	ylog.Debugf("zabbix_sender", "closing handler - final pool stats: active=%d, idle=%d, total=%d, utilization=%s",
		closeActive, closeIdle, closeTotal, closeUtilization)

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

	// 计算连接池使用率
	totalConnections, _ := stats["total_connections"].(int)
	activeConnections, _ := stats["active_connections"].(int)
	idleConnections, _ := stats["idle_connections"].(int)

	var utilization float64
	if totalConnections > 0 {
		utilization = float64(activeConnections) / float64(totalConnections) * 100
	}

	// Add additional information to the stats
	stats["server_address"] = h.serverAddr
	stats["timeouts"] = map[string]time.Duration{
		"connection": h.config.ConnectionTimeout,
		"read":       h.config.ReadTimeout,
		"write":      h.config.WriteTimeout,
	}
	stats["utilization_percent"] = fmt.Sprintf("%.1f%%", utilization)
	stats["config_pool_size"] = h.config.PoolSize
	stats["handler_status"] = "running" // 简单的状态标识

	ylog.Debugf("zabbix_sender", "connection pool stats: active=%d, idle=%d, total=%d, utilization=%s",
		activeConnections, idleConnections, totalConnections, stats["utilization_percent"])

	return stats
}

// WarmupPool pre-warms the connection pool by establishing initial connections
func (h *ZabbixSenderHandler) WarmupPool() error {
	ylog.Infof("zabbix_sender", "pre-warming connection pool for %s with %d connections", h.serverAddr, h.config.PoolSize)

	// 记录预暖前的连接池状态
	initialStats := h.GetStats()
	initialActive, _ := initialStats["active_connections"].(int)
	initialIdle, _ := initialStats["idle_connections"].(int)
	initialTotal, _ := initialStats["total_connections"].(int)
	ylog.Debugf("zabbix_sender", "pre-warm starting - initial pool stats: active=%d, idle=%d, total=%d",
		initialActive, initialIdle, initialTotal)

	connections := make([]net.Conn, 0, h.config.PoolSize)
	defer func() {
		// Clean up any connections that were created
		for _, conn := range connections {
			if conn != nil {
				conn.Close()
			}
		}

		// 记录预暖完成后的连接池状态
		finalStats := h.GetStats()
		finalActive, _ := finalStats["active_connections"].(int)
		finalIdle, _ := finalStats["idle_connections"].(int)
		finalTotal, _ := finalStats["total_connections"].(int)
		finalUtilization, _ := finalStats["utilization_percent"].(string)
		ylog.Debugf("zabbix_sender", "pre-warm completed - final pool stats: active=%d, idle=%d, total=%d, utilization=%s",
			finalActive, finalIdle, finalTotal, finalUtilization)
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
		if v, ok := event.Data["packet_loss"].(int); ok {
			value = fmt.Sprintf("%d", v)
			validEvents++
		} else if v, ok := event.Data["packet_loss"].(string); ok {
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

	// 详细记录metrics内容
	if len(metrics) > 0 {
		ylog.Debugf("zabbix_sender", "metrics details:")
		for i, metric := range metrics {
			ylog.Debugf("zabbix_sender", "  metric[%d]: host=%s, key=%s, value=%s, clock=%d",
				i, metric.Host, metric.Key, metric.Value, metric.Clock)
		}
	}

	start := time.Now()

	// 记录发送前的连接池状态
	stats := h.GetStats()
	activeConnections, _ := stats["active_connections"].(int)
	idleConnections, _ := stats["idle_connections"].(int)
	totalConnections, _ := stats["total_connections"].(int)
	ylog.Debugf("zabbix_sender", "connection pool stats before send: active=%d, idle=%d, total=%d",
		activeConnections, idleConnections, totalConnections)

	_, _, err := h.sender.SendMetrics(metrics)
	duration := time.Since(start)

	if err != nil {
		ylog.Errorf("zabbix_sender", "failed to send %d metrics to zabbix after %v: %v", len(metrics), duration, err)

		// 记录详细的错误信息和当前状态
		currentStats := h.GetStats()
		currentActive, _ := currentStats["active_connections"].(int)
		currentIdle, _ := currentStats["idle_connections"].(int)
		currentTotal, _ := currentStats["total_connections"].(int)
		ylog.Debugf("zabbix_sender", "error details - connection pool: active=%d, idle=%d, total=%d, server=%s",
			currentActive, currentIdle, currentTotal, h.serverAddr)

		return fmt.Errorf("zabbix send failed: %w (events=%d, server=%s)", err, len(events), h.serverAddr)
	}

	ylog.Infof("zabbix_sender", "successfully sent %d metrics to zabbix in %v", len(metrics), duration)

	// 记录发送后的连接池状态
	postStats := h.GetStats()
	postActive, _ := postStats["active_connections"].(int)
	postIdle, _ := postStats["idle_connections"].(int)
	postTotal, _ := postStats["total_connections"].(int)
	ylog.Debugf("zabbix_sender", "connection pool stats after send: active=%d, idle=%d, total=%d",
		postActive, postIdle, postTotal)
	return nil
}

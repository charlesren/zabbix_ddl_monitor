package task

import (
	"fmt"
	"net"
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
	sender *sender.Sender
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

	ylog.Infof("zabbix_sender", "zabbix sender handler created successfully for %s", serverAddr)
	return &ZabbixSenderHandler{
		sender: zabbixSender,
	}, nil
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

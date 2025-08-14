package task

import (
	"fmt"
	"net"
	"time"

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

	zabbixSender := sender.NewSender(
		serverAddr,
		config.ConnectionTimeout,
		config.ReadTimeout,
		config.WriteTimeout,
		config.PoolSize,
	)

	return &ZabbixSenderHandler{
		sender: zabbixSender,
	}, nil
}

func (h *ZabbixSenderHandler) HandleResult(events []ResultEvent) error {
	metrics := make([]*sender.Metric, 0, len(events))
	for _, event := range events {
		var value string
		if v, ok := event.Data["value"].(string); ok {
			value = v
		} else {
			value = ""
		}
		metrics = append(metrics, &sender.Metric{
			Host:  event.IP,
			Key:   "dedicatedLinePing",
			Value: value,
			Clock: event.Timestamp.Unix(),
		})
	}

	_, _, err := h.sender.SendMetrics(metrics)
	if err != nil {
		return fmt.Errorf("zabbix send failed: %w (events=%d)", err, len(events))
	}
	return nil
}

package task

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
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

	// 只在debug级别记录连接测试开始
	ylog.Debugf("zabbix_sender", "connection test starting for %s", h.serverAddr)

	start := time.Now()
	res, err := h.sender.Send(testPacket)
	duration := time.Since(start)

	// 只在debug级别记录详细响应信息
	ylog.Debugf("zabbix_sender", "TestConnection response: '%s', info: '%s', err: %v",
		res.Response, res.Info, err)

	if err != nil {
		ylog.Warnf("zabbix_sender", "connection test failed for %s after %v: %v", h.serverAddr, duration, err)
		return fmt.Errorf("connection test failed: %w", err)
	}

	// 只在debug级别记录成功信息
	ylog.Debugf("zabbix_sender", "connection test successful for %s in %v", h.serverAddr, duration)

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
				// 静默执行健康检查，只在debug级别记录
				if err := h.TestConnection(); err != nil {
					ylog.Debugf("zabbix_sender", "periodic health check failed for %s: %v", h.serverAddr, err)
				} else {
					ylog.Debugf("zabbix_sender", "periodic health check passed for %s", h.serverAddr)
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

	ylog.Debugf("zabbix_sender", "processing %d events for zabbix submission", len(events))

	metrics := make([]*sender.Metric, 0, len(events))
	processedEvents := 0

	for _, event := range events {
		// 检查status字段
		status, hasStatus := event.Data["status"].(string)
		if !hasStatus {
			ylog.Warnf("zabbix_sender", "事件缺少status字段: host=%s", event.IP)
			// 发送缺少status字段的错误到dedicatedLineErrorDetail
			metrics = append(metrics, &sender.Metric{
				Host:   event.IP,
				Key:    "dedicatedLineErrorDetail",
				Value:  StatusMissingStatusField,
				Clock:  event.Timestamp.Unix(),
				Active: false,
			})
			ylog.Debugf("zabbix_sender", "发送缺少status字段错误: host=%s", event.IP)
			processedEvents++
			continue
		}

		// 根据status决定发送哪个监控项
		if status == StatusCheckFinished {
			// 检查完成：发送packet_loss到dedicatedLinePing
			if v, ok := event.Data["packet_loss"].(int); ok {
				// 验证值在合理范围内 (0-100)
				if v >= 0 && v <= 100 {
					// 尝试直接使用整数，而不是转换为字符串
					// Zabbix可能期望数字类型而不是字符串
					value := fmt.Sprintf("%d", v)
					metrics = append(metrics, &sender.Metric{
						Host:   event.IP,
						Key:    "dedicatedLinePing",
						Value:  value,
						Clock:  event.Timestamp.Unix(),
						Active: false, // 明确设置为false，表示这是trapper item
					})
					// 同时记录原始整数值用于调试
					ylog.Debugf("zabbix_sender", "发送ping结果: host=%s, packet_loss=%d, status=%s", event.IP, v, status)
					// 详细记录每个有效metric
					ylog.Debugf("zabbix_sender", "有效数据: host=%s, packet_loss=%d, timestamp=%d, active=%v",
						event.IP, v, event.Timestamp.Unix(), false)
					processedEvents++
				} else {
					// 值超出范围，发送错误状态
					ylog.Warnf("zabbix_sender", "数据超出范围: host=%s, packet_loss=%d (应在0-100范围内), status=%s",
						event.IP, v, status)
					// 发送PacketLossOutOfRange错误到dedicatedLineErrorDetail
					metrics = append(metrics, &sender.Metric{
						Host:   event.IP,
						Key:    "dedicatedLineErrorDetail",
						Value:  StatusPacketLossOutOfRange,
						Clock:  event.Timestamp.Unix(),
						Active: false,
					})
					ylog.Debugf("zabbix_sender", "发送packet_loss超出范围错误: host=%s, packet_loss=%d", event.IP, v)
					processedEvents++
				}
			} else {
				// CheckFinished但无有效packet_loss，发送错误状态
				ylog.Warnf("zabbix_sender", "CheckFinished但无有效packet_loss: host=%s, packet_loss类型=%T, 值=%v, status=%s",
					event.IP, event.Data["packet_loss"], event.Data["packet_loss"], status)
				// 发送InvalidPacketLossData错误到dedicatedLineErrorDetail
				metrics = append(metrics, &sender.Metric{
					Host:   event.IP,
					Key:    "dedicatedLineErrorDetail",
					Value:  StatusInvalidPacketLossData,
					Clock:  event.Timestamp.Unix(),
					Active: false,
				})
				ylog.Debugf("zabbix_sender", "发送无效packet_loss数据错误: host=%s", event.IP)
				processedEvents++
			}
		} else {
			// 检查失败：发送status到dedicatedLineErrorDetail
			metrics = append(metrics, &sender.Metric{
				Host:   event.IP,
				Key:    "dedicatedLineErrorDetail",
				Value:  status,
				Clock:  event.Timestamp.Unix(),
				Active: false, // 明确设置为false，表示这是trapper item
			})
			ylog.Debugf("zabbix_sender", "发送错误状态: host=%s, status=%s", event.IP, status)
			processedEvents++
		}
	}

	// 统计不同类型的监控项
	pingMetrics := 0
	errorMetrics := 0
	for _, metric := range metrics {
		if metric.Key == "dedicatedLinePing" {
			pingMetrics++
		} else if metric.Key == "dedicatedLineErrorDetail" {
			errorMetrics++
		}
	}

	ylog.Infof("zabbix_sender", "数据统计: 总事件=%d, 已处理事件=%d", len(events), processedEvents)
	ylog.Infof("zabbix_sender", "监控项分类: ping指标=%d, 错误详情=%d", pingMetrics, errorMetrics)
	ylog.Debugf("zabbix_sender", "准备发送 %d 个metrics到Zabbix", len(metrics))

	// 检查是否有有效的metrics可以发送
	if len(metrics) == 0 {
		ylog.Errorf("zabbix_sender", "所有事件都被过滤，没有有效数据发送到Zabbix。总事件=%d", len(events))
		return nil
	}

	// 详细记录所有即将发送的metrics内容
	ylog.Debugf("zabbix_sender", "即将发送的metrics详细信息 (共%d个):", len(metrics))

	// 统计active和trapper metrics的数量
	activeCount := 0
	trapperCount := 0
	for _, metric := range metrics {
		if metric.Active {
			activeCount++
		} else {
			trapperCount++
		}
	}
	ylog.Infof("zabbix_sender", "Metrics分类: active=%d, trapper=%d", activeCount, trapperCount)

	// 按监控项类型记录详细信息
	for i, metric := range metrics {
		var metricType string
		if metric.Key == "dedicatedLinePing" {
			metricType = "ping指标"
		} else if metric.Key == "dedicatedLineErrorDetail" {
			metricType = "错误详情"
		} else {
			metricType = "未知类型"
		}

		ylog.Infof("zabbix_sender", "  [%d] %s: host=%s, key=%s, value=%s, type=%s",
			i+1, metricType, metric.Host, metric.Key, metric.Value, metricType)
	}

	for i, metric := range metrics {
		// 尝试解析值以确定实际类型
		var valueType string
		if _, err := strconv.Atoi(metric.Value); err == nil {
			valueType = "integer(as_string)"
		} else if _, err := strconv.ParseFloat(metric.Value, 64); err == nil {
			valueType = "float(as_string)"
		} else {
			valueType = "string"
		}

		ylog.Infof("zabbix_sender", "  metric[%d]: host=%s, key=%s, value=%s (实际类型: %s), clock=%d, active=%v",
			i, metric.Host, metric.Key, metric.Value, valueType, metric.Clock, metric.Active)

		// 额外验证value是否为有效整数
		if valueInt, err := strconv.Atoi(metric.Value); err == nil {
			if valueInt < 0 || valueInt > 100 {
				ylog.Warnf("zabbix_sender", "  WARNING: metric[%d]值超出范围: %d (应在0-100之间)", i, valueInt)
			} else {
				ylog.Debugf("zabbix_sender", "  metric[%d]是有效整数: %d", i, valueInt)
			}
		} else {
			// 尝试解析为浮点数
			if valueFloat, err := strconv.ParseFloat(metric.Value, 64); err == nil {
				ylog.Debugf("zabbix_sender", "  metric[%d]是浮点数: %f", i, valueFloat)
			} else {
				ylog.Warnf("zabbix_sender", "  WARNING: metric[%d]值不是有效数字: %s", i, metric.Value)
			}
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

	// 记录发送前的最后验证
	ylog.Debugf("zabbix_sender", "开始发送到Zabbix服务器: %s", h.serverAddr)
	for i, metric := range metrics {
		ylog.Debugf("zabbix_sender", "发送metric[%d/%d]: host=%s, key=%s, value=%s, clock=%d",
			i+1, len(metrics), metric.Host, metric.Key, metric.Value, metric.Clock)
	}

	// 调试：记录发送前的metrics数据结构
	ylog.Debugf("zabbix_sender", "发送前的metrics数据结构:")
	for i, metric := range metrics {
		ylog.Debugf("zabbix_sender", "  metric[%d]: %+v", i, metric)
	}

	// 调试：尝试手动构建Zabbix数据包以验证格式
	ylog.Debugf("zabbix_sender", "尝试构建Zabbix数据包...")

	resActive, resTrapper, err := h.sender.SendMetrics(metrics)
	duration := time.Since(start)

	// 打印详细的response信息用于调试
	ylog.Debugf("zabbix_sender", "SendMetrics返回的response详细信息:")
	ylog.Debugf("zabbix_sender", "  resActive.Response: '%s'", resActive.Response)
	ylog.Debugf("zabbix_sender", "  resActive.Info: '%s'", resActive.Info)
	ylog.Debugf("zabbix_sender", "  resTrapper.Response: '%s'", resTrapper.Response)
	ylog.Debugf("zabbix_sender", "  resTrapper.Info: '%s'", resTrapper.Info)
	ylog.Debugf("zabbix_sender", "  err: %v", err)

	if err != nil {
		// 记录详细的错误信息，包括响应内容
		activeResponse := fmt.Sprintf("active_response='%s', active_info='%s'", resActive.Response, resActive.Info)
		trapperResponse := fmt.Sprintf("trapper_response='%s', trapper_info='%s'", resTrapper.Response, resTrapper.Info)

		ylog.Errorf("zabbix_sender", "failed to send %d metrics to zabbix after %v: %v [%s %s]",
			len(metrics), duration, err, activeResponse, trapperResponse)

		// 记录详细的错误信息和当前状态
		currentStats := h.GetStats()
		currentActive, _ := currentStats["active_connections"].(int)
		currentIdle, _ := currentStats["idle_connections"].(int)
		currentTotal, _ := currentStats["total_connections"].(int)
		ylog.Debugf("zabbix_sender", "error details - connection pool: active=%d, idle=%d, total=%d, server=%s",
			currentActive, currentIdle, currentTotal, h.serverAddr)

		return fmt.Errorf("zabbix send failed: %w (events=%d, server=%s)", err, len(events), h.serverAddr)
	}

	// 检查是否有metrics处理失败（即使没有网络错误，服务器可能返回部分失败）
	// 注意：只有当有对应类型的metrics时才检查响应
	var failedDetails []string

	// 打印当前metrics分类信息
	ylog.Debugf("zabbix_sender", "检查响应状态 - activeCount=%d, trapperCount=%d", activeCount, trapperCount)
	ylog.Debugf("zabbix_sender", "响应值 - resActive.Response='%s', resTrapper.Response='%s'",
		resActive.Response, resTrapper.Response)

	// 只有当有active metrics时才检查active响应
	if activeCount > 0 && resActive.Response != "success" {
		failedDetails = append(failedDetails, fmt.Sprintf("active: %s - %s", resActive.Response, resActive.Info))
		ylog.Debugf("zabbix_sender", "检测到active响应失败: '%s' - '%s'", resActive.Response, resActive.Info)
	}

	// 只有当有trapper metrics时才检查trapper响应
	if trapperCount > 0 && resTrapper.Response != "success" {
		failedDetails = append(failedDetails, fmt.Sprintf("trapper: %s - %s", resTrapper.Response, resTrapper.Info))
		ylog.Debugf("zabbix_sender", "检测到trapper响应失败: '%s' - '%s'", resTrapper.Response, resTrapper.Info)
	}

	// 只有当有失败详情时才报告错误
	if len(failedDetails) > 0 {

		ylog.Warnf("zabbix_sender", "zabbix server reported partial failure: %v", failedDetails)

		// 记录metrics分类信息，帮助诊断问题
		ylog.Errorf("zabbix_sender", "发送的metrics分类: active=%d, trapper=%d", activeCount, trapperCount)

		// 记录更详细的Zabbix响应信息
		if activeCount > 0 {
			ylog.Errorf("zabbix_sender", "Active响应信息: response='%s', info='%s'", resActive.Response, resActive.Info)
		}
		if trapperCount > 0 {
			ylog.Errorf("zabbix_sender", "Trapper响应信息: response='%s', info='%s'", resTrapper.Response, resTrapper.Info)
		}

		// 检查是否是数据格式问题
		hasActiveInfo := activeCount > 0 && resActive.Info != ""
		hasTrapperInfo := trapperCount > 0 && resTrapper.Info != ""

		if !hasActiveInfo && !hasTrapperInfo {
			ylog.Errorf("zabbix_sender", "Zabbix返回空信息，可能是数据格式问题")
		} else {
			// 分析Zabbix错误信息
			if hasActiveInfo {
				ylog.Errorf("zabbix_sender", "Active错误分析: info='%s'", resActive.Info)
				// 检查是否是数据类型问题
				if strings.Contains(resActive.Info, "invalid") {
					ylog.Errorf("zabbix_sender", "可能是active数据类型无效错误")
				}
				if strings.Contains(resActive.Info, "type") {
					ylog.Errorf("zabbix_sender", "可能是active数据类型不匹配错误")
				}
				// 检查是否是"cannot find pair with name 'data'"错误
				if strings.Contains(resActive.Info, "cannot find pair") {
					ylog.Errorf("zabbix_sender", "检测到active 'cannot find pair'错误，可能是JSON格式问题")
				} else if strings.Contains(resActive.Info, "failed -") {
					ylog.Warnf("zabbix_sender", "检测到active业务错误，可能是key不存在或权限问题")
				}
			}

			if hasTrapperInfo {
				ylog.Errorf("zabbix_sender", "Trapper错误分析: info='%s'", resTrapper.Info)
				// 检查是否是数据类型问题
				if strings.Contains(resTrapper.Info, "invalid") {
					ylog.Errorf("zabbix_sender", "可能是trapper数据类型无效错误")
				}
				if strings.Contains(resTrapper.Info, "type") {
					ylog.Errorf("zabbix_sender", "可能是trapper数据类型不匹配错误")
				}
				// 检查是否是"cannot find pair with name 'data'"错误
				if strings.Contains(resTrapper.Info, "cannot find pair") {
					ylog.Errorf("zabbix_sender", "检测到trapper 'cannot find pair'错误，可能是JSON格式问题")
				} else if strings.Contains(resTrapper.Info, "failed -") {
					ylog.Warnf("zabbix_sender", "检测到trapper业务错误，可能是key不存在或权限问题")
				}
			}
		}

		return fmt.Errorf("zabbix server reported failures: %v", failedDetails)
	}

	// 检查连接池统计数据的合理性
	postStats := h.GetStats()
	postActive, _ := postStats["active_connections"].(int)
	postIdle, _ := postStats["idle_connections"].(int)
	postTotal, _ := postStats["total_connections"].(int)

	if postTotal == 0 && (postActive > 0 || postIdle > 0) {
		ylog.Warnf("zabbix_sender", "inconsistent connection pool stats after send: active=%d, idle=%d, total=%d - this may indicate underlying library issues",
			postActive, postIdle, postTotal)
	}

	ylog.Debugf("zabbix_sender", "successfully sent %d metrics to zabbix in %v", len(metrics), duration)

	// 记录成功的Zabbix响应信息
	ylog.Debugf("zabbix_sender", "Zabbix成功响应: active_response='%s', active_info='%s', trapper_response='%s', trapper_info='%s'",
		resActive.Response, resActive.Info, resTrapper.Response, resTrapper.Info)

	// 记录metrics分类信息
	ylog.Debugf("zabbix_sender", "成功发送metrics分类: active=%d, trapper=%d", activeCount, trapperCount)

	// 记录发送后的连接池状态
	ylog.Debugf("zabbix_sender", "connection pool stats after send: active=%d, idle=%d, total=%d",
		postActive, postIdle, postTotal)

	// 记录发送结果的详细信息
	if len(metrics) > 0 {
		ylog.Debugf("zabbix_sender", "sent metrics summary:")
		for _, metric := range metrics {
			ylog.Debugf("zabbix_sender", "  - host=%s, value=%s", metric.Host, metric.Value)
		}
	}

	return nil
}

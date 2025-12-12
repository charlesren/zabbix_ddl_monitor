package task

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/charlesren/ylog"
	"github.com/charlesren/zabbix_ddl_monitor/syncer"
)

// ResultEvent 结果事件
type ResultEvent struct {
	LineID    string                 `json:"line_id"`
	IP        string                 `json:"ip"`
	RouterIP  string                 `json:"router_ip"`
	TaskType  TaskType               `json:"task_type"`
	Timestamp time.Time              `json:"timestamp"`
	Success   bool                   `json:"success"`
	Data      map[string]interface{} `json:"data"`
	Error     string                 `json:"error,omitempty"`
	Duration  time.Duration          `json:"duration"`
}

// ResultHandler 结果处理器接口
type ResultHandler interface {
	HandleResult(event []ResultEvent) error
}

// Aggregator 结果聚合器
type Aggregator struct {
	handlers      []ResultHandler
	eventChan     chan ResultEvent
	workers       int
	buffer        []ResultEvent
	bufferSize    int
	flushTimer    *time.Timer
	flushInterval time.Duration
	mu            sync.Mutex
	wg            sync.WaitGroup
	stopChan      chan struct{}
	ctx           context.Context
	cancel        context.CancelFunc

	// 统计信息
	stats struct {
		sync.RWMutex
		totalEvents   int64
		successEvents int64
		failedEvents  int64
		lastFlush     time.Time
	}
}

// NewAggregator 创建新的结果聚合器
func NewAggregator(workers int, bufferSize int, flushInterval time.Duration) *Aggregator {
	ctx, cancel := context.WithCancel(context.Background())

	return &Aggregator{
		handlers:      make([]ResultHandler, 0),
		eventChan:     make(chan ResultEvent, workers*200), // 缓冲队列，200倍worker数量
		workers:       workers,
		buffer:        make([]ResultEvent, 0, bufferSize),
		bufferSize:    bufferSize,
		flushInterval: flushInterval,
		stopChan:      make(chan struct{}),
		ctx:           ctx,
		cancel:        cancel,
	}
}

// AddHandler 添加结果处理器
func (a *Aggregator) AddHandler(handler ResultHandler) {
	a.handlers = append(a.handlers, handler)
	ylog.Infof("aggregator", "added handler: %T (total handlers: %d)", handler, len(a.handlers))
}

// Start 启动聚合器
func (a *Aggregator) Start() {
	ylog.Debugf("aggregator", "starting aggregator with configuration: workers=%d, buffer_size=%d, flush_interval=%v, handlers=%d",
		a.workers, a.bufferSize, a.flushInterval, len(a.handlers))

	// 启动工作goroutine
	for i := 0; i < a.workers; i++ {
		a.wg.Add(1)
		go a.worker(i)
		ylog.Debugf("aggregator", "started worker %d", i)
	}

	// 启动缓冲区管理goroutine
	a.wg.Add(1)
	go a.bufferManager()
	ylog.Debugf("aggregator", "started buffer manager")

	ylog.Infof("aggregator", "started with %d workers, buffer size %d, flush interval %v, %d handlers",
		a.workers, a.bufferSize, a.flushInterval, len(a.handlers))

	// 记录初始统计信息
	stats := a.GetStats()
	ylog.Debugf("aggregator", "initial stats: queue_length=%d, buffer_length=%d",
		stats.QueueLength, stats.BufferLength)
}

// Stop 停止聚合器
func (a *Aggregator) Stop() {
	stats := a.GetStats()
	ylog.Infof("aggregator", "stopping aggregator - total events: %d (success: %d, failed: %d)",
		stats.TotalEvents, stats.SuccessEvents, stats.FailedEvents)

	ylog.Debugf("aggregator", "stop initiated: queue_length=%d, buffer_length=%d, last_flush=%v",
		stats.QueueLength, stats.BufferLength, stats.LastFlush)

	// 关闭停止通道
	close(a.stopChan)
	ylog.Debugf("aggregator", "stop channel closed")

	// 取消上下文
	a.cancel()
	ylog.Debugf("aggregator", "context cancelled")

	// 等待所有worker完成
	ylog.Debugf("aggregator", "waiting for workers to complete...")
	a.wg.Wait()
	ylog.Debugf("aggregator", "all workers completed")

	// 最后一次刷新缓冲区
	ylog.Debugf("aggregator", "performing final flush...")
	a.flush()
	ylog.Debugf("aggregator", "final flush completed")

	// 关闭事件通道
	close(a.eventChan)
	ylog.Debugf("aggregator", "event channel closed")

	// 记录最终统计信息
	finalStats := a.GetStats()
	ylog.Infof("aggregator", "aggregator stopped - final stats: total_events=%d, success_rate=%.1f%%",
		finalStats.TotalEvents, float64(finalStats.SuccessEvents)/float64(finalStats.TotalEvents)*100)

	ylog.Debugf("aggregator", "final details: queue_length=%d, buffer_length=%d",
		finalStats.QueueLength, finalStats.BufferLength)
}

// Submit 提交结果事件
func (a *Aggregator) Submit(event ResultEvent) error {
	select {
	case a.eventChan <- event:
		ylog.Debugf("aggregator", "event submitted: line=%s, ip=%s, task=%s, success=%t, duration=%v",
			event.LineID, event.IP, event.TaskType, event.Success, event.Duration)
		return nil
	case <-a.ctx.Done():
		ylog.Warnf("aggregator", "failed to submit event: context cancelled (line=%s, ip=%s)", event.LineID, event.IP)
		return a.ctx.Err()
	default:
		ylog.Warnf("aggregator", "failed to submit event: queue full (line=%s, ip=%s, queue_size=%d/%d)",
			event.LineID, event.IP, len(a.eventChan), cap(a.eventChan))
		return ErrQueueFull
	}
}

// SubmitTaskResult 提交任务结果（便捷方法）
func (a *Aggregator) SubmitTaskResult(line syncer.Line, taskType TaskType, result Result, duration time.Duration) error {
	event := ResultEvent{
		LineID:    line.ID,
		IP:        line.IP,
		RouterIP:  line.Router.IP,
		TaskType:  taskType,
		Timestamp: time.Now(),
		Success:   result.Success,
		Data:      result.Data,
		Error:     result.Error,
		Duration:  duration,
	}
	ylog.Debugf("aggregator", "submitting result for %s (line: %s, task: %s, success: %t, duration: %v)",
		line.IP, line.ID, taskType, result.Success, duration)

	// 记录详细的任务结果信息
	if result.Success {
		ylog.Debugf("aggregator", "task success details: line=%s, ip=%s, data=%+v",
			line.ID, line.IP, result.Data)
	} else {
		ylog.Debugf("aggregator", "task failure details: line=%s, ip=%s, error=%s, data=%+v",
			line.ID, line.IP, result.Error, result.Data)
	}

	return a.Submit(event)
}

// worker 工作goroutine
func (a *Aggregator) worker(id int) {
	defer a.wg.Done()

	ylog.Debugf("aggregator", "worker %d started", id)
	processedCount := 0

	for {
		select {
		case event, ok := <-a.eventChan:
			if !ok {
				ylog.Debugf("aggregator", "worker %d: event channel closed, processed %d events", id, processedCount)
				return
			}

			ylog.Debugf("aggregator", "worker %d received event: line=%s, ip=%s, task=%s, success=%t (queue_length=%d)",
				id, event.LineID, event.IP, event.TaskType, event.Success, len(a.eventChan))

			a.processEvent(id, event)
			processedCount++

			// 每处理10个事件记录一次统计
			if processedCount%10 == 0 {
				ylog.Debugf("aggregator", "worker %d: processed %d events so far", id, processedCount)
			}

		case <-a.stopChan:
			ylog.Debugf("aggregator", "worker %d: received stop signal, total processed: %d events", id, processedCount)
			return
		}
	}
}

// processEvent 处理单个事件
func (a *Aggregator) processEvent(workerID int, event ResultEvent) {
	start := time.Now()

	// 添加到缓冲区
	a.mu.Lock()
	a.buffer = append(a.buffer, event)
	currentBufferSize := len(a.buffer)
	shouldFlush := currentBufferSize >= a.bufferSize
	a.mu.Unlock()

	// 更新统计信息
	a.updateStats(event)

	// 如果缓冲区满了，立即刷新
	if shouldFlush {
		ylog.Debugf("aggregator", "worker %d: buffer full (%d/%d), triggering flush",
			workerID, currentBufferSize, a.bufferSize)
		a.flush()
	}

	processingTime := time.Since(start)
	ylog.Debugf("aggregator", "worker %d: processed event for %s (line: %s, task: %s, success: %t, duration: %v, proc_time: %v)",
		workerID, event.IP, event.LineID, event.TaskType, event.Success, event.Duration, processingTime)

	// 记录事件详细信息
	ylog.Debugf("aggregator", "worker %d event details: router_ip=%s, timestamp=%v, error=%s",
		workerID, event.RouterIP, event.Timestamp, event.Error)
}

// bufferManager 缓冲区管理goroutine
func (a *Aggregator) bufferManager() {
	defer a.wg.Done()

	a.flushTimer = time.NewTimer(a.flushInterval)
	defer a.flushTimer.Stop()

	ylog.Debugf("aggregator", "buffer manager started with flush interval: %v", a.flushInterval)

	for {
		select {
		case <-a.flushTimer.C:
			ylog.Debugf("aggregator", "buffer manager: timer triggered flush")
			a.flush()
			a.flushTimer.Reset(a.flushInterval)
			ylog.Debugf("aggregator", "buffer manager: timer reset to %v", a.flushInterval)

		case <-a.stopChan:
			ylog.Debugf("aggregator", "buffer manager: received stop signal")
			return
		}
	}
}

// flush 刷新缓冲区
func (a *Aggregator) flush() {
	start := time.Now()

	a.mu.Lock()
	currentBufferSize := len(a.buffer)
	if currentBufferSize == 0 {
		a.mu.Unlock()
		ylog.Debugf("aggregator", "flush: buffer empty, skipping")
		return
	}

	// 复制缓冲区内容
	events := make([]ResultEvent, currentBufferSize)
	copy(events, a.buffer)

	// 清空缓冲区
	a.buffer = a.buffer[:0]
	a.mu.Unlock()

	ylog.Debugf("aggregator", "flush: processing %d events from buffer", currentBufferSize)

	// 处理事件
	a.handleEvents(events)

	// 更新刷新时间
	a.stats.Lock()
	a.stats.lastFlush = time.Now()
	a.stats.Unlock()

	successCount := 0
	for _, event := range events {
		if event.Success {
			successCount++
		}
	}

	flushDuration := time.Since(start)
	ylog.Infof("aggregator", "flushed %d events (success: %d, failed: %d) in %v",
		currentBufferSize, successCount, currentBufferSize-successCount, flushDuration)

	// 记录详细的刷新统计
	ylog.Debugf("aggregator", "flush details: success_rate=%.1f%%, duration=%v, queue_length=%d",
		float64(successCount)/float64(currentBufferSize)*100, flushDuration, len(a.eventChan))
}

// handleEvents 处理事件批次
func (a *Aggregator) handleEvents(events []ResultEvent) {
	start := time.Now()

	if len(a.handlers) == 0 {
		successCount := countSuccessEvents(events)
		ylog.Warnf("aggregator", "no handlers registered, dropping %d events (success: %d, failed: %d)",
			len(events), successCount, len(events)-successCount)

		// 记录被丢弃事件的详细信息
		ylog.Debugf("aggregator", "dropped events details:")
		for i, event := range events {
			ylog.Debugf("aggregator", "  event[%d]: line=%s, ip=%s, task=%s, success=%t, error=%s",
				i, event.LineID, event.IP, event.TaskType, event.Success, event.Error)
		}
		return
	}

	ylog.Debugf("aggregator", "dispatching %d events to %d handlers", len(events), len(a.handlers))

	for i, handler := range a.handlers {
		handlerStart := time.Now()
		successCount := countSuccessEvents(events)
		failedCount := len(events) - successCount

		ylog.Debugf("aggregator", "handler %d/%d (%T): processing %d events (success: %d, failed: %d)",
			i+1, len(a.handlers), handler, len(events), successCount, failedCount)

		if err := handler.HandleResult(events); err != nil {
			ylog.Errorf("aggregator", "handler %T failed to process %d events: %v", handler, len(events), err)

			// 记录失败处理的详细信息
			ylog.Debugf("aggregator", "handler failure details: events_count=%d, error=%v, processing_time=%v",
				len(events), err, time.Since(handlerStart))
		} else {
			ylog.Debugf("aggregator", "handler %T successfully processed %d events in %v",
				handler, len(events), time.Since(handlerStart))
		}
	}

	totalDuration := time.Since(start)
	ylog.Debugf("aggregator", "batch processing completed: %d events, %d handlers, total_time=%v",
		len(events), len(a.handlers), totalDuration)
}

// updateStats 更新统计信息
func (a *Aggregator) updateStats(event ResultEvent) {
	a.stats.Lock()
	defer a.stats.Unlock()

	a.stats.totalEvents++
	if event.Success {
		a.stats.successEvents++
		ylog.Debugf("aggregator", "stats: success event for %s (total_success=%d, total_events=%d)",
			event.IP, a.stats.successEvents, a.stats.totalEvents)
	} else {
		a.stats.failedEvents++
		ylog.Debugf("aggregator", "stats: failed event for %s (total_failed=%d, total_events=%d, error=%s)",
			event.IP, a.stats.failedEvents, a.stats.totalEvents, event.Error)
	}

	// 定期记录统计摘要（每100个事件）
	if a.stats.totalEvents%100 == 0 {
		successRate := float64(a.stats.successEvents) / float64(a.stats.totalEvents) * 100
		ylog.Infof("aggregator", "stats summary: total=%d, success=%d, failed=%d, success_rate=%.1f%%",
			a.stats.totalEvents, a.stats.successEvents, a.stats.failedEvents, successRate)
	}
}

// GetStats 获取统计信息
func (a *Aggregator) GetStats() AggregatorStats {
	a.stats.RLock()
	defer a.stats.RUnlock()

	stats := AggregatorStats{
		TotalEvents:   a.stats.totalEvents,
		SuccessEvents: a.stats.successEvents,
		FailedEvents:  a.stats.failedEvents,
		LastFlush:     a.stats.lastFlush,
		QueueLength:   len(a.eventChan),
		BufferLength:  len(a.buffer),
	}

	// 记录统计信息获取
	ylog.Debugf("aggregator", "stats retrieved: total=%d, success=%d, failed=%d, queue=%d, buffer=%d",
		stats.TotalEvents, stats.SuccessEvents, stats.FailedEvents, stats.QueueLength, stats.BufferLength)

	return stats
}

// AggregatorStats 聚合器统计信息
type AggregatorStats struct {
	TotalEvents   int64     `json:"total_events"`
	SuccessEvents int64     `json:"success_events"`
	FailedEvents  int64     `json:"failed_events"`
	LastFlush     time.Time `json:"last_flush"`
	QueueLength   int       `json:"queue_length"`
	BufferLength  int       `json:"buffer_length"`
}

// countSuccessEvents 计算成功事件数量
func countSuccessEvents(events []ResultEvent) int {
	count := 0
	for _, event := range events {
		if event.Success {
			count++
		}
	}
	return count
}

// LogHandler 日志处理器
type LogHandler struct{}

func (h *LogHandler) HandleResult(events []ResultEvent) error {
	for _, event := range events {
		if event.Success {
			ylog.Infof("result", "✓ %s %s %s success (duration: %v, line: %s)",
				event.RouterIP, event.TaskType, event.IP, event.Duration, event.LineID)
		} else {
			ylog.Warnf("result", "✗ %s %s %s failed: %s (duration: %v, line: %s)",
				event.RouterIP, event.TaskType, event.IP, event.Error, event.Duration, event.LineID)
		}
	}
	return nil
}

// JSONFileHandler JSON文件处理器
type JSONFileHandler struct {
	filename string
	mu       sync.Mutex
}

func NewJSONFileHandler(filename string) *JSONFileHandler {
	return &JSONFileHandler{
		filename: filename,
	}
}

func (h *JSONFileHandler) HandleResult(events []ResultEvent) error {
	for _, event := range events {
		h.mu.Lock()
		defer h.mu.Unlock()

		data, err := json.MarshalIndent(event, "", "  ")
		if err != nil {
			return err
		}

		// 这里应该实现文件写入逻辑
		ylog.Debugf("json_handler", "would write to %s: %s", h.filename, string(data))
	}
	return nil
}

// MetricsHandler 指标处理器（用于监控系统集成）
type MetricsHandler struct {
	// 这里可以集成Prometheus、InfluxDB等监控系统
}

func (h *MetricsHandler) HandleResult(events []ResultEvent) error {
	for _, event := range events {
		// 示例：更新监控指标
		if event.Success {
			// increment success counter
		} else {
			// increment failure counter
		}

		ylog.Debugf("metrics_handler", "updated metrics for %s: success=%t",
			event.IP, event.Success)
	}
	return nil
}

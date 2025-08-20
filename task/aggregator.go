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
		eventChan:     make(chan ResultEvent, workers*2), // 缓冲队列
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
	// 启动工作goroutine
	for i := 0; i < a.workers; i++ {
		a.wg.Add(1)
		go a.worker(i)
	}

	// 启动缓冲区管理goroutine
	a.wg.Add(1)
	go a.bufferManager()

	ylog.Infof("aggregator", "started with %d workers, buffer size %d, flush interval %v, %d handlers",
		a.workers, a.bufferSize, a.flushInterval, len(a.handlers))
}

// Stop 停止聚合器
func (a *Aggregator) Stop() {
	stats := a.GetStats()
	ylog.Infof("aggregator", "stopping aggregator - total events: %d (success: %d, failed: %d)",
		stats.TotalEvents, stats.SuccessEvents, stats.FailedEvents)

	// 关闭停止通道
	close(a.stopChan)

	// 取消上下文
	a.cancel()

	// 等待所有worker完成
	a.wg.Wait()

	// 最后一次刷新缓冲区
	a.flush()

	// 关闭事件通道
	close(a.eventChan)

	ylog.Infof("aggregator", "aggregator stopped")
}

// Submit 提交结果事件
func (a *Aggregator) Submit(event ResultEvent) error {
	select {
	case a.eventChan <- event:
		return nil
	case <-a.ctx.Done():
		return a.ctx.Err()
	default:
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
	return a.Submit(event)
}

// worker 工作goroutine
func (a *Aggregator) worker(id int) {
	defer a.wg.Done()

	ylog.Debugf("aggregator", "worker %d started", id)

	for {
		select {
		case event, ok := <-a.eventChan:
			if !ok {
				ylog.Debugf("aggregator", "worker %d: event channel closed", id)
				return
			}

			a.processEvent(id, event)

		case <-a.stopChan:
			ylog.Debugf("aggregator", "worker %d: received stop signal", id)
			return
		}
	}
}

// processEvent 处理单个事件
func (a *Aggregator) processEvent(workerID int, event ResultEvent) {
	// 添加到缓冲区
	a.mu.Lock()
	a.buffer = append(a.buffer, event)
	shouldFlush := len(a.buffer) >= a.bufferSize
	a.mu.Unlock()

	// 更新统计信息
	a.updateStats(event)

	// 如果缓冲区满了，立即刷新
	if shouldFlush {
		a.flush()
	}

	ylog.Debugf("aggregator", "worker %d: processed event for %s (line: %s, task: %s, success: %t, duration: %v)",
		workerID, event.IP, event.LineID, event.TaskType, event.Success, event.Duration)
}

// bufferManager 缓冲区管理goroutine
func (a *Aggregator) bufferManager() {
	defer a.wg.Done()

	a.flushTimer = time.NewTimer(a.flushInterval)
	defer a.flushTimer.Stop()

	for {
		select {
		case <-a.flushTimer.C:
			a.flush()
			a.flushTimer.Reset(a.flushInterval)

		case <-a.stopChan:
			return
		}
	}
}

// flush 刷新缓冲区
func (a *Aggregator) flush() {
	a.mu.Lock()
	if len(a.buffer) == 0 {
		a.mu.Unlock()
		return
	}

	// 复制缓冲区内容
	events := make([]ResultEvent, len(a.buffer))
	copy(events, a.buffer)

	// 清空缓冲区
	a.buffer = a.buffer[:0]
	a.mu.Unlock()

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
	ylog.Infof("aggregator", "flushed %d events (success: %d, failed: %d)",
		len(events), successCount, len(events)-successCount)
}

// handleEvents 处理事件批次
func (a *Aggregator) handleEvents(events []ResultEvent) {
	if len(a.handlers) == 0 {
		ylog.Warnf("aggregator", "no handlers registered, dropping %d events (success: %d, failed: %d)",
			len(events), countSuccessEvents(events), len(events)-countSuccessEvents(events))
		return
	}

	for _, handler := range a.handlers {
		successCount := countSuccessEvents(events)
		ylog.Debugf("aggregator", "dispatching %d events to handler %T (success: %d, failed: %d)",
			len(events), handler, successCount, len(events)-successCount)
		if err := handler.HandleResult(events); err != nil {
			ylog.Errorf("aggregator", "handler %T failed to process %d events: %v", handler, len(events), err)
		}
	}
}

// updateStats 更新统计信息
func (a *Aggregator) updateStats(event ResultEvent) {
	a.stats.Lock()
	defer a.stats.Unlock()

	a.stats.totalEvents++
	if event.Success {
		a.stats.successEvents++
	} else {
		a.stats.failedEvents++
	}
}

// GetStats 获取统计信息
func (a *Aggregator) GetStats() AggregatorStats {
	a.stats.RLock()
	defer a.stats.RUnlock()

	return AggregatorStats{
		TotalEvents:   a.stats.totalEvents,
		SuccessEvents: a.stats.successEvents,
		FailedEvents:  a.stats.failedEvents,
		LastFlush:     a.stats.lastFlush,
		QueueLength:   len(a.eventChan),
		BufferLength:  len(a.buffer),
	}
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

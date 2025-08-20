package task

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/charlesren/zabbix_ddl_monitor/syncer"
)

// Mock ResultHandler for testing
type mockResultHandler struct {
	results      []ResultEvent
	mu           sync.Mutex
	handleFunc   func(ResultEvent) error
	callCount    int
	shouldError  bool
	errorMessage string
}

func (m *mockResultHandler) HandleResult(events []ResultEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.results = append(m.results, events...)
	m.callCount++

	if m.shouldError {
		return fmt.Errorf("%s", m.errorMessage)
	}

	if m.handleFunc != nil {
		// 对于单个事件的处理函数兼容性
		if len(events) == 1 {
			return m.handleFunc(events[0])
		}
		// 对于多个事件，只处理第一个（保持向后兼容）
		for _, event := range events {
			return m.handleFunc(event)
		}
	}

	return nil
}

func (m *mockResultHandler) getResults() []ResultEvent {
	m.mu.Lock()
	defer m.mu.Unlock()

	results := make([]ResultEvent, len(m.results))
	copy(results, m.results)
	return results
}

func (m *mockResultHandler) getCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

func (m *mockResultHandler) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.results = nil
	m.callCount = 0
}

func TestNewAggregator(t *testing.T) {
	tests := []struct {
		name          string
		workers       int
		bufferSize    int
		flushInterval time.Duration
	}{
		{
			name:          "basic_aggregator",
			workers:       2,
			bufferSize:    100,
			flushInterval: 5 * time.Second,
		},
		{
			name:          "single_worker",
			workers:       1,
			bufferSize:    50,
			flushInterval: 1 * time.Second,
		},
		{
			name:          "many_workers",
			workers:       10,
			bufferSize:    1000,
			flushInterval: 30 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			aggregator := NewAggregator(tt.workers, tt.bufferSize, tt.flushInterval)

			if aggregator == nil {
				t.Fatal("Expected non-nil aggregator")
			}

			if aggregator.workers != tt.workers {
				t.Errorf("Expected %d workers, got %d", tt.workers, aggregator.workers)
			}

			if aggregator.bufferSize != tt.bufferSize {
				t.Errorf("Expected buffer size %d, got %d", tt.bufferSize, aggregator.bufferSize)
			}

			if aggregator.flushInterval != tt.flushInterval {
				t.Errorf("Expected flush interval %v, got %v", tt.flushInterval, aggregator.flushInterval)
			}

			if aggregator.eventChan == nil {
				t.Error("Expected event channel to be initialized")
			}

			if aggregator.stopChan == nil {
				t.Error("Expected stop channel to be initialized")
			}

			if aggregator.handlers == nil {
				t.Error("Expected handlers slice to be initialized")
			}

			if len(aggregator.handlers) != 0 {
				t.Error("Expected empty handlers initially")
			}
		})
	}
}

func TestAggregator_AddHandler(t *testing.T) {
	aggregator := NewAggregator(1, 10, 1*time.Second)

	handler1 := &mockResultHandler{}
	handler2 := &mockResultHandler{}

	// Initially no handlers
	if len(aggregator.handlers) != 0 {
		t.Error("Expected no handlers initially")
	}

	// Add first handler
	aggregator.AddHandler(handler1)
	if len(aggregator.handlers) != 1 {
		t.Error("Expected 1 handler after adding first")
	}

	// Add second handler
	aggregator.AddHandler(handler2)
	if len(aggregator.handlers) != 2 {
		t.Error("Expected 2 handlers after adding second")
	}

	// Verify handlers are stored correctly
	if aggregator.handlers[0] != handler1 {
		t.Error("Expected first handler to match")
	}
	if aggregator.handlers[1] != handler2 {
		t.Error("Expected second handler to match")
	}
}

func TestAggregator_StartStop(t *testing.T) {
	aggregator := NewAggregator(2, 10, 100*time.Millisecond)

	// Test Start
	aggregator.Start()

	// Verify workers are running
	if aggregator.workers != 2 {
		t.Error("Workers should be initialized")
	}

	// Test Stop
	aggregator.Stop()

	// After stopping, context should be cancelled
	select {
	case <-aggregator.ctx.Done():
		// Expected
	default:
		t.Error("Expected context to be cancelled after Stop()")
	}
}

func TestAggregator_Submit(t *testing.T) {
	aggregator := NewAggregator(1, 10, 1*time.Second)
	handler := &mockResultHandler{}
	aggregator.AddHandler(handler)

	aggregator.Start()
	defer aggregator.Stop()

	// Create test event
	event := ResultEvent{
		LineID:    "line-001",
		IP:        "192.168.1.1",
		RouterIP:  "192.168.1.100",
		TaskType:  "ping",
		Timestamp: time.Now(),
		Success:   true,
		Data:      map[string]interface{}{"rtt": "2ms"},
		Duration:  100 * time.Millisecond,
	}

	// Submit event
	err := aggregator.Submit(event)
	if err != nil {
		t.Fatalf("Failed to submit event: %v", err)
	}

	// Wait for processing
	waitForCondition(t, func() bool {
		return handler.getCallCount() > 0
	}, 2*time.Second, "Event was not processed")

	// Verify handler received the event
	results := handler.getResults()
	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	receivedEvent := results[0]
	if receivedEvent.LineID != event.LineID {
		t.Errorf("Expected LineID %s, got %s", event.LineID, receivedEvent.LineID)
	}
	if receivedEvent.Success != event.Success {
		t.Errorf("Expected Success %t, got %t", event.Success, receivedEvent.Success)
	}
}

func TestAggregator_SubmitTaskResult(t *testing.T) {
	aggregator := NewAggregator(1, 10, 1*time.Second)
	handler := &mockResultHandler{}
	aggregator.AddHandler(handler)

	aggregator.Start()
	defer aggregator.Stop()

	// Create test line and result
	line := syncer.Line{
		ID: "line-001",
		IP: "192.168.1.1",
		Router: syncer.Router{
			IP:       "192.168.1.100",
			Platform: "cisco_iosxe",
		},
	}

	result := Result{
		Success: true,
		Data:    map[string]interface{}{"ping_success": true},
		Error:   "",
	}

	duration := 150 * time.Millisecond

	// Submit using convenience method
	err := aggregator.SubmitTaskResult(line, "ping", result, duration)
	if err != nil {
		t.Fatalf("Failed to submit task result: %v", err)
	}

	// Wait for processing
	waitForCondition(t, func() bool {
		return handler.getCallCount() > 0
	}, 2*time.Second, "Task result was not processed")

	// Verify handler received the correct event
	results := handler.getResults()
	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	event := results[0]
	if event.LineID != line.ID {
		t.Errorf("Expected LineID %s, got %s", line.ID, event.LineID)
	}
	if event.IP != line.IP {
		t.Errorf("Expected IP %s, got %s", line.IP, event.IP)
	}
	if event.RouterIP != line.Router.IP {
		t.Errorf("Expected RouterIP %s, got %s", line.Router.IP, event.RouterIP)
	}
	if event.TaskType != "ping" {
		t.Errorf("Expected TaskType ping, got %s", event.TaskType)
	}
	if event.Success != result.Success {
		t.Errorf("Expected Success %t, got %t", result.Success, event.Success)
	}
	if event.Duration != duration {
		t.Errorf("Expected Duration %v, got %v", duration, event.Duration)
	}
}

func TestAggregator_BufferFlush(t *testing.T) {
	// Use small buffer and short flush interval for testing
	aggregator := NewAggregator(1, 3, 50*time.Millisecond)
	handler := &mockResultHandler{}
	aggregator.AddHandler(handler)

	aggregator.Start()
	defer aggregator.Stop()

	// Submit events that fill the buffer
	for i := 0; i < 3; i++ {
		event := ResultEvent{
			LineID:   fmt.Sprintf("line-%d", i),
			IP:       fmt.Sprintf("192.168.1.%d", i+1),
			RouterIP: "192.168.1.100",
			TaskType: "ping",
			Success:  true,
		}

		err := aggregator.Submit(event)
		if err != nil {
			t.Fatalf("Failed to submit event %d: %v", i, err)
		}
	}

	// Wait for buffer flush (should happen immediately when buffer is full)
	waitForCondition(t, func() bool {
		return handler.getCallCount() >= 3
	}, 2*time.Second, "Buffer was not flushed")

	// Verify all events were processed
	results := handler.getResults()
	if len(results) != 3 {
		t.Errorf("Expected 3 results, got %d", len(results))
	}
}

func TestAggregator_TimeBasedFlush(t *testing.T) {
	// Use larger buffer but very short flush interval
	aggregator := NewAggregator(1, 100, 100*time.Millisecond)
	handler := &mockResultHandler{}
	aggregator.AddHandler(handler)

	aggregator.Start()
	defer aggregator.Stop()

	// Submit single event (won't fill buffer)
	event := ResultEvent{
		LineID:   "line-001",
		IP:       "192.168.1.1",
		RouterIP: "192.168.1.100",
		TaskType: "ping",
		Success:  true,
	}

	err := aggregator.Submit(event)
	if err != nil {
		t.Fatalf("Failed to submit event: %v", err)
	}

	// Wait for time-based flush
	waitForCondition(t, func() bool {
		return handler.getCallCount() > 0
	}, 500*time.Millisecond, "Time-based flush did not occur")

	results := handler.getResults()
	if len(results) != 1 {
		t.Errorf("Expected 1 result, got %d", len(results))
	}
}

func TestAggregator_MultipleHandlers(t *testing.T) {
	aggregator := NewAggregator(1, 10, 1*time.Second)

	handler1 := &mockResultHandler{}
	handler2 := &mockResultHandler{}
	handler3 := &mockResultHandler{}

	aggregator.AddHandler(handler1)
	aggregator.AddHandler(handler2)
	aggregator.AddHandler(handler3)

	aggregator.Start()
	defer aggregator.Stop()

	// Submit event
	event := ResultEvent{
		LineID:   "line-001",
		IP:       "192.168.1.1",
		RouterIP: "192.168.1.100",
		TaskType: "ping",
		Success:  true,
	}

	err := aggregator.Submit(event)
	if err != nil {
		t.Fatalf("Failed to submit event: %v", err)
	}

	// Wait for processing
	waitForCondition(t, func() bool {
		return handler1.getCallCount() > 0 &&
			handler2.getCallCount() > 0 &&
			handler3.getCallCount() > 0
	}, 2*time.Second, "Not all handlers processed the event")

	// Verify all handlers received the event
	for i, handler := range []*mockResultHandler{handler1, handler2, handler3} {
		results := handler.getResults()
		if len(results) != 1 {
			t.Errorf("Handler %d: expected 1 result, got %d", i+1, len(results))
		}
		if len(results) > 0 && results[0].LineID != event.LineID {
			t.Errorf("Handler %d: expected LineID %s, got %s", i+1, event.LineID, results[0].LineID)
		}
	}
}

func TestAggregator_HandlerError(t *testing.T) {
	aggregator := NewAggregator(1, 10, 1*time.Second)

	// Create handler that returns error
	errorHandler := &mockResultHandler{
		shouldError:  true,
		errorMessage: "handler error",
	}

	// Create normal handler
	normalHandler := &mockResultHandler{}

	aggregator.AddHandler(errorHandler)
	aggregator.AddHandler(normalHandler)

	aggregator.Start()
	defer aggregator.Stop()

	// Submit event
	event := ResultEvent{
		LineID:   "line-001",
		IP:       "192.168.1.1",
		RouterIP: "192.168.1.100",
		TaskType: "ping",
		Success:  true,
	}

	err := aggregator.Submit(event)
	if err != nil {
		t.Fatalf("Failed to submit event: %v", err)
	}

	// Wait for processing
	waitForCondition(t, func() bool {
		return errorHandler.getCallCount() > 0 && normalHandler.getCallCount() > 0
	}, 2*time.Second, "Handlers did not process the event")

	// Both handlers should have been called despite error
	if errorHandler.getCallCount() != 1 {
		t.Errorf("Error handler should have been called once, got %d", errorHandler.getCallCount())
	}
	if normalHandler.getCallCount() != 1 {
		t.Errorf("Normal handler should have been called once, got %d", normalHandler.getCallCount())
	}
}

func TestAggregator_NoHandlers(t *testing.T) {
	aggregator := NewAggregator(1, 10, 1*time.Second)
	// Don't add any handlers

	aggregator.Start()
	defer aggregator.Stop()

	// Submit event (should not cause error, but event will be dropped)
	event := ResultEvent{
		LineID:   "line-001",
		IP:       "192.168.1.1",
		RouterIP: "192.168.1.100",
		TaskType: "ping",
		Success:  true,
	}

	err := aggregator.Submit(event)
	if err != nil {
		t.Fatalf("Failed to submit event: %v", err)
	}

	// Just wait a bit to ensure no panic occurs
	time.Sleep(100 * time.Millisecond)
}

func TestAggregator_ConcurrentSubmit(t *testing.T) {
	aggregator := NewAggregator(3, 100, 1*time.Second)
	handler := &mockResultHandler{}
	aggregator.AddHandler(handler)

	aggregator.Start()
	defer aggregator.Stop()

	const numGoroutines = 10
	const eventsPerGoroutine = 20
	const totalEvents = numGoroutines * eventsPerGoroutine

	var wg sync.WaitGroup

	// Submit events concurrently
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			for j := 0; j < eventsPerGoroutine; j++ {
				event := ResultEvent{
					LineID:   fmt.Sprintf("line-%d-%d", goroutineID, j),
					IP:       fmt.Sprintf("192.168.%d.%d", goroutineID, j),
					RouterIP: "192.168.1.100",
					TaskType: "ping",
					Success:  true,
				}

				err := aggregator.Submit(event)
				if err != nil {
					t.Errorf("Failed to submit event from goroutine %d, event %d: %v", goroutineID, j, err)
				}
			}
		}(i)
	}

	wg.Wait()

	// Wait for all events to be processed
	waitForCondition(t, func() bool {
		return handler.getCallCount() >= totalEvents
	}, 5*time.Second, fmt.Sprintf("Not all %d events were processed", totalEvents))

	// Verify total count
	callCount := handler.getCallCount()
	if callCount != totalEvents {
		t.Errorf("Expected %d events to be processed, got %d", totalEvents, callCount)
	}
}

func TestAggregator_GetStats(t *testing.T) {
	aggregator := NewAggregator(1, 10, 1*time.Second)
	handler := &mockResultHandler{}
	aggregator.AddHandler(handler)

	// Initially empty stats
	stats := aggregator.GetStats()
	if stats.TotalEvents != 0 {
		t.Error("Expected 0 total events initially")
	}
	if stats.SuccessEvents != 0 {
		t.Error("Expected 0 success events initially")
	}
	if stats.FailedEvents != 0 {
		t.Error("Expected 0 failed events initially")
	}

	aggregator.Start()
	defer aggregator.Stop()

	// Submit successful event
	successEvent := ResultEvent{
		LineID:  "line-001",
		Success: true,
	}
	aggregator.Submit(successEvent)

	// Submit failed event
	failEvent := ResultEvent{
		LineID:  "line-002",
		Success: false,
	}
	aggregator.Submit(failEvent)

	// Wait for processing
	waitForCondition(t, func() bool {
		return handler.getCallCount() >= 2
	}, 2*time.Second, "Events were not processed")

	// Check updated stats
	stats = aggregator.GetStats()
	if stats.TotalEvents != 2 {
		t.Errorf("Expected 2 total events, got %d", stats.TotalEvents)
	}
	if stats.SuccessEvents != 1 {
		t.Errorf("Expected 1 success event, got %d", stats.SuccessEvents)
	}
	if stats.FailedEvents != 1 {
		t.Errorf("Expected 1 failed event, got %d", stats.FailedEvents)
	}
}

func TestAggregator_QueueFull(t *testing.T) {
	// Create aggregator with very small buffer
	aggregator := &Aggregator{
		eventChan: make(chan ResultEvent, 1), // Buffer size 1
		workers:   1,
		stopChan:  make(chan struct{}),
		ctx:       context.Background(),
		cancel:    func() {},
	}

	// Fill the queue
	event1 := ResultEvent{LineID: "line-001"}
	err1 := aggregator.Submit(event1)
	if err1 != nil {
		t.Errorf("First submit should succeed: %v", err1)
	}

	// This should fail due to full queue
	event2 := ResultEvent{LineID: "line-002"}
	err2 := aggregator.Submit(event2)
	if err2 != ErrQueueFull {
		t.Errorf("Expected ErrQueueFull, got %v", err2)
	}
}

// Test built-in handlers
func TestLogHandler(t *testing.T) {
	handler := &LogHandler{}

	successEvent := ResultEvent{
		RouterIP: "192.168.1.100",
		IP:       "192.168.1.1",
		Success:  true,
		Duration: 150 * time.Millisecond,
	}

	err := handler.HandleResult([]ResultEvent{successEvent})
	if err != nil {
		t.Errorf("LogHandler should not return error: %v", err)
	}

	failEvent := ResultEvent{
		RouterIP: "192.168.1.100",
		IP:       "192.168.1.1",
		Success:  false,
		Error:    "timeout",
		Duration: 5 * time.Second,
	}

	err = handler.HandleResult([]ResultEvent{failEvent})
	if err != nil {
		t.Errorf("LogHandler should not return error: %v", err)
	}
}

func TestJSONFileHandler(t *testing.T) {
	handler := NewJSONFileHandler("test.json")

	event := ResultEvent{
		LineID:   "line-001",
		IP:       "192.168.1.1",
		RouterIP: "192.168.1.100",
		Success:  true,
	}

	err := handler.HandleResult([]ResultEvent{event})
	if err != nil {
		t.Errorf("JSONFileHandler should not return error: %v", err)
	}
}

func TestMetricsHandler(t *testing.T) {
	handler := &MetricsHandler{}

	event := ResultEvent{
		IP:      "192.168.1.1",
		Success: true,
	}

	err := handler.HandleResult([]ResultEvent{event})
	if err != nil {
		t.Errorf("MetricsHandler should not return error: %v", err)
	}
}

// Benchmark tests
func BenchmarkAggregator_Submit(b *testing.B) {
	aggregator := NewAggregator(4, 1000, 1*time.Second)
	handler := &mockResultHandler{}
	aggregator.AddHandler(handler)

	aggregator.Start()
	defer aggregator.Stop()

	event := ResultEvent{
		LineID:   "line-001",
		IP:       "192.168.1.1",
		RouterIP: "192.168.1.100",
		TaskType: "ping",
		Success:  true,
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = aggregator.Submit(event)
		}
	})
}

func BenchmarkAggregator_ProcessEvent(b *testing.B) {
	aggregator := NewAggregator(1, 1000, 1*time.Second)
	handler := &mockResultHandler{}
	aggregator.AddHandler(handler)

	event := ResultEvent{
		LineID:   "line-001",
		IP:       "192.168.1.1",
		RouterIP: "192.168.1.100",
		TaskType: "ping",
		Success:  true,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		aggregator.processEvent(0, event)
	}
}

// Helper function for testing
func waitForCondition(t *testing.T, condition func() bool, timeout time.Duration, message string) {
	deadline := time.After(timeout)
	for {
		if condition() {
			return
		}
		select {
		case <-deadline:
			t.Fatal(message)
		case <-time.After(10 * time.Millisecond):
			// Continue waiting
		}
	}
}

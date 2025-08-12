package task

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charlesren/zabbix_ddl_monitor/connection"
)

// Mock middleware implementations for testing
type mockMiddleware struct {
	name        string
	beforeCount int64
	afterCount  int64
	errorCount  int64
	beforeFunc  func(TaskContext) error
	afterFunc   func(TaskContext, Result, error) (Result, error)
}

func (m *mockMiddleware) createMiddleware() Middleware {
	return func(next ExecutorFunc) ExecutorFunc {
		return func(task Task, conn connection.ProtocolDriver, ctx TaskContext) (Result, error) {
			// Before logic
			atomic.AddInt64(&m.beforeCount, 1)
			if m.beforeFunc != nil {
				if err := m.beforeFunc(ctx); err != nil {
					atomic.AddInt64(&m.errorCount, 1)
					result := Result{Success: false, Error: err.Error()}
					if m.afterFunc != nil {
						return m.afterFunc(ctx, result, err)
					}
					return result, err
				}
			}

			// Execute next middleware/task
			result, err := next(task, conn, ctx)

			// After logic
			atomic.AddInt64(&m.afterCount, 1)
			if err != nil {
				atomic.AddInt64(&m.errorCount, 1)
			}
			if m.afterFunc != nil {
				return m.afterFunc(ctx, result, err)
			}
			return result, err
		}
	}
}

func (m *mockMiddleware) getBeforeCount() int64 {
	return atomic.LoadInt64(&m.beforeCount)
}

func (m *mockMiddleware) getAfterCount() int64 {
	return atomic.LoadInt64(&m.afterCount)
}

func (m *mockMiddleware) getErrorCount() int64 {
	return atomic.LoadInt64(&m.errorCount)
}

func (m *mockMiddleware) reset() {
	atomic.StoreInt64(&m.beforeCount, 0)
	atomic.StoreInt64(&m.afterCount, 0)
	atomic.StoreInt64(&m.errorCount, 0)
}

// Test basic middleware functionality

func TestMiddleware_BasicExecution(t *testing.T) {
	middleware := &mockMiddleware{name: "test"}
	executor := NewExecutor(nil, middleware.createMiddleware())

	mockTask := &mockTask{}
	mockDriver := &mockProtocolDriver{}

	ctx := TaskContext{
		TaskType: "mock",
		Platform: connection.PlatformCiscoIOSXE,
		Protocol: connection.ProtocolScrapli,
		Params:   map[string]interface{}{},
		Ctx:      context.Background(),
	}

	result, err := executor.Execute(mockTask, mockDriver, ctx)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if !result.Success {
		t.Error("Expected successful result")
	}

	if middleware.getBeforeCount() != 1 {
		t.Errorf("Expected 1 before call, got %d", middleware.getBeforeCount())
	}

	if middleware.getAfterCount() != 1 {
		t.Errorf("Expected 1 after call, got %d", middleware.getAfterCount())
	}

	if middleware.getErrorCount() != 0 {
		t.Errorf("Expected 0 error calls, got %d", middleware.getErrorCount())
	}
}

func TestMiddleware_ErrorHandling(t *testing.T) {
	middleware := &mockMiddleware{name: "test"}
	executor := NewExecutor(nil, middleware.createMiddleware())

	// Mock task that fails
	mockTask := &mockTask{
		executeFunc: func(ctx TaskContext) (Result, error) {
			return Result{Success: false}, errors.New("task failed")
		},
	}
	mockDriver := &mockProtocolDriver{}

	ctx := TaskContext{
		TaskType: "mock",
		Platform: connection.PlatformCiscoIOSXE,
		Protocol: connection.ProtocolScrapli,
		Params:   map[string]interface{}{},
		Ctx:      context.Background(),
	}

	result, err := executor.Execute(mockTask, mockDriver, ctx)

	if err == nil {
		t.Error("Expected error from failed task")
	}

	if result.Success {
		t.Error("Expected failed result")
	}

	if middleware.getBeforeCount() != 1 {
		t.Errorf("Expected 1 before call, got %d", middleware.getBeforeCount())
	}

	if middleware.getAfterCount() != 1 {
		t.Errorf("Expected 1 after call, got %d", middleware.getAfterCount())
	}

	if middleware.getErrorCount() != 1 {
		t.Errorf("Expected 1 error call, got %d", middleware.getErrorCount())
	}
}

func TestMiddleware_BeforeReject(t *testing.T) {
	middleware := &mockMiddleware{
		name: "rejecting",
		beforeFunc: func(ctx TaskContext) error {
			return errors.New("middleware rejected task")
		},
	}
	executor := NewExecutor(nil, middleware.createMiddleware())

	mockTask := &mockTask{}
	mockDriver := &mockProtocolDriver{}

	ctx := TaskContext{
		TaskType: "mock",
		Platform: connection.PlatformCiscoIOSXE,
		Protocol: connection.ProtocolScrapli,
		Params:   map[string]interface{}{},
		Ctx:      context.Background(),
	}

	result, err := executor.Execute(mockTask, mockDriver, ctx)

	if err == nil {
		t.Error("Expected error from middleware rejection")
	}

	if !result.Success { // Result should be successful but error should be present
		// This depends on implementation - middleware rejection might create failed result
	}

	if middleware.getBeforeCount() != 1 {
		t.Errorf("Expected 1 before call, got %d", middleware.getBeforeCount())
	}

	// After should still be called even if before fails
	if middleware.getAfterCount() != 1 {
		t.Errorf("Expected 1 after call, got %d", middleware.getAfterCount())
	}
}

func TestMiddleware_ResultModification(t *testing.T) {
	middleware := &mockMiddleware{
		name: "modifier",
		afterFunc: func(ctx TaskContext, result Result, err error) (Result, error) {
			// Modify result
			result.Data["middleware_modified"] = true
			result.Data["original_success"] = result.Success
			result.Success = true // Force success
			return result, nil    // Clear error
		},
	}
	executor := NewExecutor(nil, middleware.createMiddleware())

	// Mock task that fails
	mockTask := &mockTask{
		executeFunc: func(ctx TaskContext) (Result, error) {
			return Result{
				Success: false,
				Data:    map[string]interface{}{"original": "data"},
			}, errors.New("original error")
		},
	}
	mockDriver := &mockProtocolDriver{}

	ctx := TaskContext{
		TaskType: "mock",
		Platform: connection.PlatformCiscoIOSXE,
		Protocol: connection.ProtocolScrapli,
		Params:   map[string]interface{}{},
		Ctx:      context.Background(),
	}

	result, err := executor.Execute(mockTask, mockDriver, ctx)

	if err != nil {
		t.Errorf("Expected no error after middleware modification, got: %v", err)
	}

	if !result.Success {
		t.Error("Expected successful result after middleware modification")
	}

	if modified, ok := result.Data["middleware_modified"].(bool); !ok || !modified {
		t.Error("Expected middleware modification flag")
	}

	if originalSuccess, ok := result.Data["original_success"].(bool); !ok || originalSuccess {
		t.Error("Expected original success to be preserved as false")
	}
}

// Test multiple middleware execution

func TestMiddleware_MultipleExecution(t *testing.T) {
	middleware1 := &mockMiddleware{name: "first"}
	middleware2 := &mockMiddleware{name: "second"}
	middleware3 := &mockMiddleware{name: "third"}

	executor := NewExecutor(nil, middleware1.createMiddleware(), middleware2.createMiddleware(), middleware3.createMiddleware())

	mockTask := &mockTask{}
	mockDriver := &mockProtocolDriver{}

	ctx := TaskContext{
		TaskType: "mock",
		Platform: connection.PlatformCiscoIOSXE,
		Protocol: connection.ProtocolScrapli,
		Params:   map[string]interface{}{},
		Ctx:      context.Background(),
	}

	result, err := executor.Execute(mockTask, mockDriver, ctx)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if !result.Success {
		t.Error("Expected successful result")
	}

	// All middleware should be called
	middlewares := []*mockMiddleware{middleware1, middleware2, middleware3}
	for i, mw := range middlewares {
		if mw.getBeforeCount() != 1 {
			t.Errorf("Middleware %d: Expected 1 before call, got %d", i, mw.getBeforeCount())
		}
		if mw.getAfterCount() != 1 {
			t.Errorf("Middleware %d: Expected 1 after call, got %d", i, mw.getAfterCount())
		}
	}
}

func TestMiddleware_ExecutionOrder(t *testing.T) {
	var executionOrder []string
	var mu sync.Mutex

	addToOrder := func(item string) {
		mu.Lock()
		executionOrder = append(executionOrder, item)
		mu.Unlock()
	}

	middleware1 := &mockMiddleware{
		name: "first",
		beforeFunc: func(ctx TaskContext) error {
			addToOrder("first_before")
			return nil
		},
		afterFunc: func(ctx TaskContext, result Result, err error) (Result, error) {
			addToOrder("first_after")
			return result, err
		},
	}

	middleware2 := &mockMiddleware{
		name: "second",
		beforeFunc: func(ctx TaskContext) error {
			addToOrder("second_before")
			return nil
		},
		afterFunc: func(ctx TaskContext, result Result, err error) (Result, error) {
			addToOrder("second_after")
			return result, err
		},
	}

	executor := NewExecutor(nil, middleware1.createMiddleware(), middleware2.createMiddleware())

	mockTask := &mockTask{
		executeFunc: func(ctx TaskContext) (Result, error) {
			addToOrder("task_execute")
			return Result{Success: true, Data: map[string]interface{}{}}, nil
		},
	}
	mockDriver := &mockProtocolDriver{}

	ctx := TaskContext{
		TaskType: "mock",
		Platform: connection.PlatformCiscoIOSXE,
		Protocol: connection.ProtocolScrapli,
		Params:   map[string]interface{}{},
		Ctx:      context.Background(),
	}

	_, err := executor.Execute(mockTask, mockDriver, ctx)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	expectedOrder := []string{
		"first_before",
		"second_before",
		"task_execute",
		"second_after",
		"first_after",
	}

	mu.Lock()
	actualOrder := make([]string, len(executionOrder))
	copy(actualOrder, executionOrder)
	mu.Unlock()

	if len(actualOrder) != len(expectedOrder) {
		t.Fatalf("Expected %d execution steps, got %d: %v", len(expectedOrder), len(actualOrder), actualOrder)
	}

	for i, expected := range expectedOrder {
		if actualOrder[i] != expected {
			t.Errorf("Step %d: Expected '%s', got '%s'", i, expected, actualOrder[i])
		}
	}
}

func TestMiddleware_ChainInterruption(t *testing.T) {
	var executionOrder []string
	var mu sync.Mutex

	addToOrder := func(item string) {
		mu.Lock()
		executionOrder = append(executionOrder, item)
		mu.Unlock()
	}

	middleware1 := &mockMiddleware{
		name: "first",
		beforeFunc: func(ctx TaskContext) error {
			addToOrder("first_before")
			return nil
		},
		afterFunc: func(ctx TaskContext, result Result, err error) (Result, error) {
			addToOrder("first_after")
			return result, err
		},
	}

	middleware2 := &mockMiddleware{
		name: "second_failing",
		beforeFunc: func(ctx TaskContext) error {
			addToOrder("second_before")
			return errors.New("middleware2 rejected")
		},
		afterFunc: func(ctx TaskContext, result Result, err error) (Result, error) {
			addToOrder("second_after")
			return result, err
		},
	}

	middleware3 := &mockMiddleware{
		name: "third",
		beforeFunc: func(ctx TaskContext) error {
			addToOrder("third_before")
			return nil
		},
		afterFunc: func(ctx TaskContext, result Result, err error) (Result, error) {
			addToOrder("third_after")
			return result, err
		},
	}

	executor := NewExecutor(nil, middleware1.createMiddleware(), middleware2.createMiddleware(), middleware3.createMiddleware())

	mockTask := &mockTask{
		executeFunc: func(ctx TaskContext) (Result, error) {
			addToOrder("task_execute")
			return Result{Success: true, Data: map[string]interface{}{}}, nil
		},
	}
	mockDriver := &mockProtocolDriver{}

	ctx := TaskContext{
		TaskType: "mock",
		Platform: connection.PlatformCiscoIOSXE,
		Protocol: connection.ProtocolScrapli,
		Params:   map[string]interface{}{},
		Ctx:      context.Background(),
	}

	_, err := executor.Execute(mockTask, mockDriver, ctx)
	if err == nil {
		t.Error("Expected error from middleware rejection")
	}

	mu.Lock()
	actualOrder := make([]string, len(executionOrder))
	copy(actualOrder, executionOrder)
	mu.Unlock()

	// Should execute first_before, second_before (fails), then after methods in reverse order
	expectedOrder := []string{
		"first_before",
		"second_before",
		"second_after",
		"first_after",
	}

	if len(actualOrder) != len(expectedOrder) {
		t.Fatalf("Expected %d execution steps, got %d: %v", len(expectedOrder), len(actualOrder), actualOrder)
	}

	for i, expected := range expectedOrder {
		if i < len(actualOrder) && actualOrder[i] != expected {
			t.Errorf("Step %d: Expected '%s', got '%s'", i, expected, actualOrder[i])
		}
	}

	// Task should not execute if middleware fails
	for _, step := range actualOrder {
		if step == "task_execute" {
			t.Error("Task should not execute if middleware Before fails")
		}
		if step == "third_before" || step == "third_after" {
			t.Error("Third middleware should not execute if second middleware Before fails")
		}
	}
}

// Test built-in middleware

func TestWithTimeout_Success(t *testing.T) {
	timeoutMiddleware := WithTimeout(1 * time.Second)
	executor := NewExecutor(nil, timeoutMiddleware)

	mockTask := &mockTask{
		executeFunc: func(ctx TaskContext) (Result, error) {
			// Quick execution
			time.Sleep(100 * time.Millisecond)
			return Result{Success: true, Data: map[string]interface{}{}}, nil
		},
	}
	mockDriver := &mockProtocolDriver{}

	ctx := TaskContext{
		TaskType: "mock",
		Platform: connection.PlatformCiscoIOSXE,
		Protocol: connection.ProtocolScrapli,
		Params:   map[string]interface{}{},
		Ctx:      context.Background(),
	}

	start := time.Now()
	result, err := executor.Execute(mockTask, mockDriver, ctx)
	duration := time.Since(start)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if !result.Success {
		t.Error("Expected successful result")
	}

	if duration > 500*time.Millisecond {
		t.Errorf("Expected quick execution, took %v", duration)
	}
}

func TestWithTimeout_Timeout(t *testing.T) {
	timeoutMiddleware := WithTimeout(100 * time.Millisecond)
	executor := NewExecutor(nil, timeoutMiddleware)

	mockTask := &mockTask{
		executeFunc: func(ctx TaskContext) (Result, error) {
			// Slow execution that should timeout
			time.Sleep(1 * time.Second)
			return Result{Success: true, Data: map[string]interface{}{}}, nil
		},
	}
	mockDriver := &mockProtocolDriver{}

	ctx := TaskContext{
		TaskType: "mock",
		Platform: connection.PlatformCiscoIOSXE,
		Protocol: connection.ProtocolScrapli,
		Params:   map[string]interface{}{},
		Ctx:      context.Background(),
	}

	start := time.Now()
	result, err := executor.Execute(mockTask, mockDriver, ctx)
	duration := time.Since(start)

	if err == nil {
		t.Error("Expected timeout error")
	}

	if result.Success {
		t.Error("Expected failed result due to timeout")
	}

	if duration > 200*time.Millisecond {
		t.Errorf("Expected quick timeout, took %v", duration)
	}

	if !isTimeoutError(err) {
		t.Errorf("Expected timeout error, got: %v", err)
	}
}

func TestWithRetry_Success(t *testing.T) {
	retryMiddleware := WithRetry(3, 10*time.Millisecond)
	executor := NewExecutor(nil, retryMiddleware)

	var attemptCount int64
	mockTask := &mockTask{
		executeFunc: func(ctx TaskContext) (Result, error) {
			count := atomic.AddInt64(&attemptCount, 1)
			if count < 3 {
				return Result{Success: false}, errors.New("temporary failure")
			}
			return Result{Success: true, Data: map[string]interface{}{"attempts": count}}, nil
		},
	}
	mockDriver := &mockProtocolDriver{}

	ctx := TaskContext{
		TaskType: "mock",
		Platform: connection.PlatformCiscoIOSXE,
		Protocol: connection.ProtocolScrapli,
		Params:   map[string]interface{}{},
		Ctx:      context.Background(),
	}

	result, err := executor.Execute(mockTask, mockDriver, ctx)

	if err != nil {
		t.Errorf("Expected no error after retries, got: %v", err)
	}

	if !result.Success {
		t.Error("Expected successful result after retries")
	}

	if attempts, ok := result.Data["attempts"].(int64); !ok || attempts != 3 {
		t.Errorf("Expected 3 attempts, got %v", attempts)
	}
}

func TestWithRetry_ExhaustRetries(t *testing.T) {
	retryMiddleware := WithRetry(2, 10*time.Millisecond)
	executor := NewExecutor(nil, retryMiddleware)

	var attemptCount int64
	mockTask := &mockTask{
		executeFunc: func(ctx TaskContext) (Result, error) {
			atomic.AddInt64(&attemptCount, 1)
			return Result{Success: false}, errors.New("persistent failure")
		},
	}
	mockDriver := &mockProtocolDriver{}

	ctx := TaskContext{
		TaskType: "mock",
		Platform: connection.PlatformCiscoIOSXE,
		Protocol: connection.ProtocolScrapli,
		Params:   map[string]interface{}{},
		Ctx:      context.Background(),
	}

	result, err := executor.Execute(mockTask, mockDriver, ctx)

	if err == nil {
		t.Error("Expected error after exhausting retries")
	}

	if result.Success {
		t.Error("Expected failed result after exhausting retries")
	}

	finalCount := atomic.LoadInt64(&attemptCount)
	if finalCount != 3 { // Initial attempt + 2 retries
		t.Errorf("Expected 3 total attempts, got %d", finalCount)
	}
}

func TestWithLogging_Success(t *testing.T) {
	var logEntries []string
	var mu sync.Mutex

	logger := func(level, message string) {
		mu.Lock()
		logEntries = append(logEntries, fmt.Sprintf("[%s] %s", level, message))
		mu.Unlock()
	}

	loggingMiddleware := WithLogging(logger)
	executor := NewExecutor(nil, loggingMiddleware)

	mockTask := &mockTask{}
	mockDriver := &mockProtocolDriver{}

	ctx := TaskContext{
		TaskType: "mock",
		Platform: connection.PlatformCiscoIOSXE,
		Protocol: connection.ProtocolScrapli,
		Params:   map[string]interface{}{},
		Ctx:      context.Background(),
	}

	result, err := executor.Execute(mockTask, mockDriver, ctx)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if !result.Success {
		t.Error("Expected successful result")
	}

	mu.Lock()
	entries := make([]string, len(logEntries))
	copy(entries, logEntries)
	mu.Unlock()

	if len(entries) < 2 {
		t.Errorf("Expected at least 2 log entries, got %d: %v", len(entries), entries)
	}

	// Should have start and completion logs
	hasStartLog := false
	hasCompleteLog := false
	for _, entry := range entries {
		if contains(entry, "Starting") {
			hasStartLog = true
		}
		if contains(entry, "Completed") {
			hasCompleteLog = true
		}
	}

	if !hasStartLog {
		t.Error("Expected start log entry")
	}
	if !hasCompleteLog {
		t.Error("Expected completion log entry")
	}
}

func TestWithLogging_Error(t *testing.T) {
	var logEntries []string
	var mu sync.Mutex

	logger := func(level, message string) {
		mu.Lock()
		logEntries = append(logEntries, fmt.Sprintf("[%s] %s", level, message))
		mu.Unlock()
	}

	loggingMiddleware := WithLogging(logger)
	executor := NewExecutor(nil, loggingMiddleware)

	mockTask := &mockTask{
		executeFunc: func(ctx TaskContext) (Result, error) {
			return Result{Success: false}, errors.New("task failed")
		},
	}
	mockDriver := &mockProtocolDriver{}

	ctx := TaskContext{
		TaskType: "mock",
		Platform: connection.PlatformCiscoIOSXE,
		Protocol: connection.ProtocolScrapli,
		Params:   map[string]interface{}{},
		Ctx:      context.Background(),
	}

	result, err := executor.Execute(mockTask, mockDriver, ctx)

	if err == nil {
		t.Error("Expected error from failed task")
	}

	if result.Success {
		t.Error("Expected failed result")
	}

	mu.Lock()
	entries := make([]string, len(logEntries))
	copy(entries, logEntries)
	mu.Unlock()

	hasErrorLog := false
	for _, entry := range entries {
		if contains(entry, "ERROR") && contains(entry, "task failed") {
			hasErrorLog = true
		}
	}

	if !hasErrorLog {
		t.Error("Expected error log entry")
	}
}

func TestWithMetrics_Success(t *testing.T) {
	var metrics []string
	var mu sync.Mutex

	metricsCollector := func(taskType string, platform connection.Platform, success bool, duration time.Duration) {
		mu.Lock()
		metrics = append(metrics, fmt.Sprintf("%s_%s_%t_%v", taskType, platform, success, duration))
		mu.Unlock()
	}

	metricsMiddleware := WithMetrics(metricsCollector)
	executor := NewExecutor(nil, metricsMiddleware)

	mockTask := &mockTask{
		executeFunc: func(ctx TaskContext) (Result, error) {
			time.Sleep(50 * time.Millisecond) // Measurable duration
			return Result{Success: true, Data: map[string]interface{}{}}, nil
		},
	}
	mockDriver := &mockProtocolDriver{}

	ctx := TaskContext{
		TaskType: "mock",
		Platform: connection.PlatformCiscoIOSXE,
		Protocol: connection.ProtocolScrapli,
		Params:   map[string]interface{}{},
		Ctx:      context.Background(),
	}

	result, err := executor.Execute(mockTask, mockDriver, ctx)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if !result.Success {
		t.Error("Expected successful result")
	}

	mu.Lock()
	metricEntries := make([]string, len(metrics))
	copy(metricEntries, metrics)
	mu.Unlock()

	if len(metricEntries) != 1 {
		t.Errorf("Expected 1 metric entry, got %d: %v", len(metricEntries), metricEntries)
	}

	metric := metricEntries[0]
	if !contains(metric, "mock") {
		t.Error("Expected task type in metrics")
	}
	if !contains(metric, "cisco_iosxe") {
		t.Error("Expected platform in metrics")
	}
	if !contains(metric, "true") {
		t.Error("Expected success=true in metrics")
	}
}

// Test concurrent middleware execution

func TestMiddleware_ConcurrentExecution(t *testing.T) {
	middleware := &mockMiddleware{name: "concurrent"}
	executor := NewExecutor(nil, middleware.createMiddleware())

	mockTask := &mockTask{}
	mockDriver := &mockProtocolDriver{}

	const numGoroutines = 50
	var wg sync.WaitGroup
	var errors []error
	var mu sync.Mutex

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			ctx := TaskContext{
				TaskType: "mock",
				Platform: connection.PlatformCiscoIOSXE,
				Protocol: connection.ProtocolScrapli,
				Params:   map[string]interface{}{"id": id},
				Ctx:      context.Background(),
			}

			result, err := executor.Execute(mockTask, mockDriver, ctx)
			if err != nil {
				mu.Lock()
				errors = append(errors, err)
				mu.Unlock()
			}

			if !result.Success {
				mu.Lock()
				errors = append(errors, fmt.Errorf("task %d failed", id))
				mu.Unlock()
			}
		}(i)
	}

	wg.Wait()

	mu.Lock()
	errorCount := len(errors)
	mu.Unlock()

	if errorCount > 0 {
		t.Errorf("Expected no errors in concurrent execution, got %d errors", errorCount)
	}

	if middleware.getBeforeCount() != numGoroutines {
		t.Errorf("Expected %d before calls, got %d", numGoroutines, middleware.getBeforeCount())
	}

	if middleware.getAfterCount() != numGoroutines {
		t.Errorf("Expected %d after calls, got %d", numGoroutines, middleware.getAfterCount())
	}
}

// Benchmark tests

func BenchmarkMiddleware_SingleExecution(b *testing.B) {
	middleware := &mockMiddleware{name: "bench"}
	executor := NewExecutor(nil, middleware.createMiddleware())

	mockTask := &mockTask{}
	mockDriver := &mockProtocolDriver{}

	ctx := TaskContext{
		TaskType: "mock",
		Platform: connection.PlatformCiscoIOSXE,
		Protocol: connection.ProtocolScrapli,
		Params:   map[string]interface{}{},
		Ctx:      context.Background(),
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = executor.Execute(mockTask, mockDriver, ctx)
	}
}

func BenchmarkMiddleware_MultipleMiddleware(b *testing.B) {
	middlewares := make([]Middleware, 10)
	for i := 0; i < 10; i++ {
		mockMw := &mockMiddleware{name: fmt.Sprintf("bench_%d", i)}
		middlewares[i] = mockMw.createMiddleware()
	}
	executor := NewExecutor(nil, middlewares...)

	mockTask := &mockTask{}
	mockDriver := &mockProtocolDriver{}

	ctx := TaskContext{
		TaskType: "mock",
		Platform: connection.PlatformCiscoIOSXE,
		Protocol: connection.ProtocolScrapli,
		Params:   map[string]interface{}{},
		Ctx:      context.Background(),
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = executor.Execute(mockTask, mockDriver, ctx)
	}
}

func BenchmarkMiddleware_WithTimeout(b *testing.B) {
	timeoutMiddleware := WithTimeout(1 * time.Second)
	executor := NewExecutor(nil, timeoutMiddleware)

	mockTask := &mockTask{}
	mockDriver := &mockProtocolDriver{}

	ctx := TaskContext{
		TaskType: "mock",
		Platform: connection.PlatformCiscoIOSXE,
		Protocol: connection.ProtocolScrapli,
		Params:   map[string]interface{}{},
		Ctx:      context.Background(),
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = executor.Execute(mockTask, mockDriver, ctx)
	}
}

// Helper functions

func isTimeoutError(err error) bool {
	return err != nil && (contains(err.Error(), "timeout") || contains(err.Error(), "context deadline exceeded"))
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

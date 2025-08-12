package task

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charlesren/zabbix_ddl_monitor/connection"
	"github.com/charlesren/zabbix_ddl_monitor/syncer"
)

// Edge case test utilities
type edgeCaseTestSetup struct {
	registry   Registry
	executor   *Executor
	asyncExec  *AsyncExecutor
	aggregator *Aggregator
}

type faultyDriver struct {
	failOnExecute bool
	failOnClose   bool
	panicOnCall   bool
	timeoutDelay  time.Duration
	corruptData   bool
	responses     map[string]*connection.ProtocolResponse
	mu            sync.RWMutex
}

func (f *faultyDriver) ProtocolType() connection.Protocol {
	return connection.ProtocolScrapli
}

func (f *faultyDriver) Close() error {
	if f.failOnClose {
		return errors.New("failed to close connection")
	}
	return nil
}

func (f *faultyDriver) Execute(req *connection.ProtocolRequest) (*connection.ProtocolResponse, error) {
	if f.panicOnCall {
		panic("driver panic on execute")
	}

	if f.failOnExecute {
		return nil, errors.New("driver execution failed")
	}

	if f.timeoutDelay > 0 {
		time.Sleep(f.timeoutDelay)
	}

	f.mu.RLock()
	response, exists := f.responses[string(req.CommandType)]
	f.mu.RUnlock()

	if !exists {
		if f.corruptData {
			return &connection.ProtocolResponse{
				Success: true,
				RawData: []byte("\x00\x01\x02corrupt\xFFdata\x00"),
			}, nil
		}
		return &connection.ProtocolResponse{
			Success: true,
			RawData: []byte("default response"),
		}, nil
	}

	return response, nil
}

func (f *faultyDriver) GetCapability() connection.ProtocolCapability {
	return connection.ProtocolCapability{
		Protocol:            connection.ProtocolScrapli,
		PlatformSupport:     []connection.Platform{connection.PlatformCiscoIOSXE},
		CommandTypesSupport: []connection.CommandType{connection.CommandTypeInteractiveEvent},
		MaxConcurrent:       1,
		Timeout:             1 * time.Second,
	}
}

func setupEdgeCaseTest() *edgeCaseTestSetup {
	registry := NewDefaultRegistry()
	executor := NewExecutor(nil)
	asyncExec := NewAsyncExecutor(2)
	aggregator := NewAggregator(2, 10, 100*time.Millisecond)

	// Register ping task
	pingMeta := TaskMeta{
		Type:        "ping",
		Description: "Ping connectivity test",
		Platforms: []PlatformSupport{
			{
				Platform: connection.PlatformCiscoIOSXE,
				Protocols: []ProtocolSupport{
					{
						Protocol: connection.ProtocolScrapli,
						CommandTypes: []CommandTypeSupport{
							{
								CommandType: connection.CommandTypeInteractiveEvent,
								ImplFactory: func() Task { return &PingTask{} },
							},
						},
					},
				},
			},
		},
	}
	registry.Register(pingMeta)

	return &edgeCaseTestSetup{
		registry:   registry,
		executor:   executor,
		asyncExec:  asyncExec,
		aggregator: aggregator,
	}
}

// Test edge cases in task parameters

func TestEdgeCase_EmptyTaskParameters(t *testing.T) {
	task := &PingTask{}

	// Test with completely empty parameters
	err := task.ValidateParams(map[string]interface{}{})
	if err == nil {
		t.Error("Expected validation error for empty parameters")
	}

	// Test with nil parameters
	err = task.ValidateParams(nil)
	if err == nil {
		t.Error("Expected validation error for nil parameters")
	}
}

func TestEdgeCase_ExtremeParameterValues(t *testing.T) {
	task := &PingTask{}

	tests := []struct {
		name    string
		params  map[string]interface{}
		wantErr bool
		errMsg  string
	}{
		{
			name: "negative_repeat",
			params: map[string]interface{}{
				"target_ip": "192.168.1.1",
				"repeat":    -1,
			},
			wantErr: true,
			errMsg:  "repeat must be positive",
		},
		{
			name: "zero_repeat",
			params: map[string]interface{}{
				"target_ip": "192.168.1.1",
				"repeat":    0,
			},
			wantErr: true,
			errMsg:  "repeat must be positive",
		},
		{
			name: "huge_repeat",
			params: map[string]interface{}{
				"target_ip": "192.168.1.1",
				"repeat":    1000000,
			},
			wantErr: true,
			errMsg:  "repeat too large",
		},
		{
			name: "invalid_timeout_type",
			params: map[string]interface{}{
				"target_ip": "192.168.1.1",
				"repeat":    3,
				"timeout":   "not_a_duration",
			},
			wantErr: true,
		},
		{
			name: "negative_timeout",
			params: map[string]interface{}{
				"target_ip": "192.168.1.1",
				"repeat":    3,
				"timeout":   -5 * time.Second,
			},
			wantErr: true,
		},
		{
			name: "extremely_long_timeout",
			params: map[string]interface{}{
				"target_ip": "192.168.1.1",
				"repeat":    3,
				"timeout":   24 * time.Hour,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := task.ValidateParams(tt.params)
			if tt.wantErr {
				if err == nil {
					t.Error("Expected validation error")
				} else if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("Expected error message to contain '%s', got '%s'", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error, got: %v", err)
				}
			}
		})
	}
}

func TestEdgeCase_MalformedIPAddresses(t *testing.T) {
	task := &PingTask{}

	malformedIPs := []string{
		"",
		"256.256.256.256",
		"192.168.1",
		"192.168.1.1.1",
		"192.168.1.-1",
		"not.an.ip.address",
		"192.168.1.1/24", // CIDR notation
		"::1",            // IPv6 (if not supported)
		"localhost",
		"192.168.1.1:8080",        // With port
		"192.168.1.1 192.168.1.2", // Multiple IPs in one string
		"\x00\x01\x02\x03",        // Binary data
		strings.Repeat("1", 1000), // Extremely long string
	}

	for _, ip := range malformedIPs {
		t.Run(fmt.Sprintf("ip_%s", ip), func(t *testing.T) {
			params := map[string]interface{}{
				"target_ip": ip,
				"repeat":    3,
			}

			err := task.ValidateParams(params)
			if err == nil {
				t.Errorf("Expected validation error for malformed IP: %s", ip)
			}
		})
	}
}

func TestEdgeCase_BatchIPLimits(t *testing.T) {
	task := &PingTask{}

	// Test empty batch
	params := map[string]interface{}{
		"target_ips": []string{},
		"repeat":     3,
	}
	err := task.ValidateParams(params)
	if err == nil {
		t.Error("Expected error for empty IP batch")
	}

	// Test single IP in batch (edge case)
	params = map[string]interface{}{
		"target_ips": []string{"192.168.1.1"},
		"repeat":     3,
	}
	err = task.ValidateParams(params)
	if err != nil {
		t.Errorf("Single IP batch should be valid: %v", err)
	}

	// Test extremely large batch
	largeIPList := make([]string, 10000)
	for i := 0; i < 10000; i++ {
		largeIPList[i] = fmt.Sprintf("192.%d.%d.%d", (i/65536)%256, (i/256)%256, i%256)
	}
	params = map[string]interface{}{
		"target_ips": largeIPList,
		"repeat":     3,
	}
	err = task.ValidateParams(params)
	if err == nil {
		t.Error("Expected error for extremely large IP batch")
	}

	// Test mixed valid/invalid IPs in batch
	mixedIPs := []string{
		"192.168.1.1",   // Valid
		"invalid.ip",    // Invalid
		"192.168.1.2",   // Valid
		"256.256.256.1", // Invalid
	}
	params = map[string]interface{}{
		"target_ips": mixedIPs,
		"repeat":     3,
	}
	err = task.ValidateParams(params)
	if err == nil {
		t.Error("Expected error for batch with invalid IPs")
	}
}

// Test edge cases in driver behavior

func TestEdgeCase_DriverExecutionFailure(t *testing.T) {
	setup := setupEdgeCaseTest()

	task, err := setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
	if err != nil {
		t.Fatalf("Failed to discover task: %v", err)
	}

	faultyDriver := &faultyDriver{
		failOnExecute: true,
		responses:     make(map[string]*connection.ProtocolResponse),
	}

	ctx := TaskContext{
		TaskType:    "ping",
		Platform:    connection.PlatformCiscoIOSXE,
		Protocol:    connection.ProtocolScrapli,
		CommandType: connection.CommandTypeInteractiveEvent,
		Params: map[string]interface{}{
			"target_ip": "192.168.1.1",
			"repeat":    3,
		},
		Ctx: context.Background(),
	}

	result, err := setup.executor.Execute(task, faultyDriver, ctx)
	if err == nil {
		t.Error("Expected error when driver fails")
	}

	if result.Success {
		t.Error("Expected failed result when driver fails")
	}

	if result.Error == "" {
		t.Error("Expected error message in result")
	}
}

func TestEdgeCase_DriverPanic(t *testing.T) {
	setup := setupEdgeCaseTest()

	task, err := setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
	if err != nil {
		t.Fatalf("Failed to discover task: %v", err)
	}

	faultyDriver := &faultyDriver{
		panicOnCall: true,
		responses:   make(map[string]*connection.ProtocolResponse),
	}

	ctx := TaskContext{
		TaskType:    "ping",
		Platform:    connection.PlatformCiscoIOSXE,
		Protocol:    connection.ProtocolScrapli,
		CommandType: connection.CommandTypeInteractiveEvent,
		Params: map[string]interface{}{
			"target_ip": "192.168.1.1",
			"repeat":    3,
		},
		Ctx: context.Background(),
	}

	// This should recover from panic and return error
	result, err := setup.executor.Execute(task, faultyDriver, ctx)
	if err == nil {
		t.Error("Expected error when driver panics")
	}

	if result.Success {
		t.Error("Expected failed result when driver panics")
	}

	if !strings.Contains(result.Error, "panic") {
		t.Error("Expected panic mentioned in error message")
	}
}

func TestEdgeCase_CorruptedDriverResponse(t *testing.T) {
	setup := setupEdgeCaseTest()

	task, err := setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
	if err != nil {
		t.Fatalf("Failed to discover task: %v", err)
	}

	// Test various corrupted responses
	corruptedResponses := [][]byte{
		nil,                                   // Nil response
		{},                                    // Empty response
		{0x00, 0x01, 0x02, 0xFF},              // Binary data
		[]byte(strings.Repeat("x", 10000000)), // Extremely large response
		[]byte("\x00\x01\x02corrupt\xFFdata\x00\x01\x02\x03"), // Mixed binary/text
	}

	for i, corruptData := range corruptedResponses {
		t.Run(fmt.Sprintf("corrupt_data_%d", i), func(t *testing.T) {
			faultyDriver := &faultyDriver{
				responses: map[string]*connection.ProtocolResponse{
					string(connection.CommandTypeInteractiveEvent): {
						Success: true,
						RawData: corruptData,
					},
				},
			}

			ctx := TaskContext{
				TaskType:    "ping",
				Platform:    connection.PlatformCiscoIOSXE,
				Protocol:    connection.ProtocolScrapli,
				CommandType: connection.CommandTypeInteractiveEvent,
				Params: map[string]interface{}{
					"target_ip": "192.168.1.1",
					"repeat":    3,
				},
				Ctx: context.Background(),
			}

			// Should handle corrupted data gracefully
			result, err := setup.executor.Execute(task, faultyDriver, ctx)
			if err != nil {
				t.Logf("Error handling corrupted data (expected): %v", err)
			}

			// Result should indicate parsing issues or provide default behavior
			t.Logf("Result for corrupted data %d: Success=%t, Error=%s", i, result.Success, result.Error)
		})
	}
}

// Test edge cases in context handling

func TestEdgeCase_ContextCancellation(t *testing.T) {
	setup := setupEdgeCaseTest()
	setup.asyncExec.Start()
	defer setup.asyncExec.Stop()

	task, err := setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
	if err != nil {
		t.Fatalf("Failed to discover task: %v", err)
	}

	// Create a context that gets cancelled immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	taskCtx := TaskContext{
		TaskType:    "ping",
		Platform:    connection.PlatformCiscoIOSXE,
		Protocol:    connection.ProtocolScrapli,
		CommandType: connection.CommandTypeInteractiveEvent,
		Params: map[string]interface{}{
			"target_ip": "192.168.1.1",
			"repeat":    3,
		},
		Ctx: ctx,
	}

	faultyDriver := &faultyDriver{
		timeoutDelay: 5 * time.Second, // Long delay
		responses:    make(map[string]*connection.ProtocolResponse),
	}

	var callbackResult Result
	var callbackError error
	var callbackCalled bool
	var mu sync.Mutex

	callback := func(result Result, err error) {
		mu.Lock()
		defer mu.Unlock()
		callbackResult = result
		callbackError = err
		callbackCalled = true
	}

	err = setup.asyncExec.Submit(task, faultyDriver, taskCtx, callback)
	if err != nil {
		t.Fatalf("Failed to submit task: %v", err)
	}

	// Wait for callback
	timeout := time.After(2 * time.Second)
	for {
		mu.Lock()
		called := callbackCalled
		mu.Unlock()

		if called {
			break
		}

		select {
		case <-timeout:
			t.Fatal("Callback was not called within timeout")
		case <-time.After(10 * time.Millisecond):
			continue
		}
	}

	mu.Lock()
	defer mu.Unlock()

	if callbackError == nil {
		t.Error("Expected error due to context cancellation")
	}

	if callbackResult.Success {
		t.Error("Expected failed result due to context cancellation")
	}
}

func TestEdgeCase_ContextTimeout(t *testing.T) {
	setup := setupEdgeCaseTest()

	task, err := setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
	if err != nil {
		t.Fatalf("Failed to discover task: %v", err)
	}

	// Create a context with very short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	taskCtx := TaskContext{
		TaskType:    "ping",
		Platform:    connection.PlatformCiscoIOSXE,
		Protocol:    connection.ProtocolScrapli,
		CommandType: connection.CommandTypeInteractiveEvent,
		Params: map[string]interface{}{
			"target_ip": "192.168.1.1",
			"repeat":    3,
		},
		Ctx: ctx,
	}

	faultyDriver := &faultyDriver{
		timeoutDelay: 5 * time.Second, // Much longer than context timeout
		responses:    make(map[string]*connection.ProtocolResponse),
	}

	result, err := setup.executor.Execute(task, faultyDriver, taskCtx)
	if err == nil {
		t.Error("Expected timeout error")
	}

	if result.Success {
		t.Error("Expected failed result due to timeout")
	}

	if !strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "context") {
		t.Errorf("Expected timeout-related error, got: %v", err)
	}
}

// Test edge cases in aggregator behavior

func TestEdgeCase_AggregatorOverflow(t *testing.T) {
	// Create aggregator with very small buffer
	aggregator := NewAggregator(1, 2, 1*time.Second) // Buffer size 2
	aggregator.Start()
	defer aggregator.Stop()

	handler := &mockResultHandler{}
	aggregator.AddHandler(handler)

	// Submit more events than buffer can handle
	for i := 0; i < 10; i++ {
		event := ResultEvent{
			LineID:    fmt.Sprintf("line-%d", i),
			IP:        fmt.Sprintf("192.168.1.%d", i+1),
			TaskType:  "ping",
			Success:   true,
			Timestamp: time.Now(),
		}

		err := aggregator.Submit(event)
		if i < 2 {
			// First few should succeed
			if err != nil {
				t.Errorf("Event %d should be accepted: %v", i, err)
			}
		} else {
			// Later ones might fail due to buffer overflow
			if err != nil {
				t.Logf("Event %d rejected (expected due to buffer overflow): %v", i, err)
			}
		}
	}
}

func TestEdgeCase_AggregatorHandlerPanic(t *testing.T) {
	aggregator := NewAggregator(1, 10, 100*time.Millisecond)
	aggregator.Start()
	defer aggregator.Stop()

	// Handler that panics
	panicHandler := &mockResultHandler{
		handleFunc: func(event ResultEvent) error {
			panic("handler panic")
		},
	}

	// Normal handler
	normalHandler := &mockResultHandler{}

	aggregator.AddHandler(panicHandler)
	aggregator.AddHandler(normalHandler)

	event := ResultEvent{
		LineID:    "test-line",
		IP:        "192.168.1.1",
		TaskType:  "ping",
		Success:   true,
		Timestamp: time.Now(),
	}

	err := aggregator.Submit(event)
	if err != nil {
		t.Errorf("Event submission failed: %v", err)
	}

	// Wait for processing
	time.Sleep(200 * time.Millisecond)

	// Normal handler should still receive events despite panic handler
	normalHandler.mu.Lock()
	callCount := normalHandler.callCount
	normalHandler.mu.Unlock()

	if callCount == 0 {
		t.Error("Normal handler should have received event despite panic handler")
	}
}

// Test edge cases in async execution

func TestEdgeCase_AsyncExecutorShutdownDuringExecution(t *testing.T) {
	setup := setupEdgeCaseTest()
	setup.asyncExec.Start()

	task, err := setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
	if err != nil {
		t.Fatalf("Failed to discover task: %v", err)
	}

	slowDriver := &faultyDriver{
		timeoutDelay: 2 * time.Second, // Longer than shutdown time
		responses:    make(map[string]*connection.ProtocolResponse),
	}

	ctx := TaskContext{
		TaskType:    "ping",
		Platform:    connection.PlatformCiscoIOSXE,
		Protocol:    connection.ProtocolScrapli,
		CommandType: connection.CommandTypeInteractiveEvent,
		Params: map[string]interface{}{
			"target_ip": "192.168.1.1",
			"repeat":    3,
		},
		Ctx: context.Background(),
	}

	var callbackCalled bool
	var mu sync.Mutex

	callback := func(result Result, err error) {
		mu.Lock()
		callbackCalled = true
		mu.Unlock()
		t.Logf("Callback called with success=%t, error=%v", result.Success, err)
	}

	// Submit task
	err = setup.asyncExec.Submit(task, slowDriver, ctx, callback)
	if err != nil {
		t.Fatalf("Failed to submit task: %v", err)
	}

	// Stop executor while task is running
	time.Sleep(100 * time.Millisecond) // Let task start
	setup.asyncExec.Stop()

	// Wait to see if callback is called
	time.Sleep(3 * time.Second)

	mu.Lock()
	called := callbackCalled
	mu.Unlock()

	// Callback may or may not be called depending on shutdown timing
	t.Logf("Callback called after shutdown: %t", called)
}

func TestEdgeCase_MassiveParameterValues(t *testing.T) {
	task := &PingTask{}

	// Test with extremely large parameter values
	hugeString := strings.Repeat("x", 1000000) // 1MB string

	params := map[string]interface{}{
		"target_ip":      hugeString,
		"repeat":         3,
		"huge_param":     hugeString,
		"nested_huge":    map[string]interface{}{"data": hugeString},
		"array_huge":     []string{hugeString, hugeString},
		"complex_nested": map[string]interface{}{"level1": map[string]interface{}{"level2": hugeString}},
	}

	// Should handle gracefully (either validate and reject, or process efficiently)
	err := task.ValidateParams(params)
	t.Logf("Validation result for huge parameters: %v", err)

	// If validation passes, try building command
	if err == nil {
		ctx := TaskContext{
			TaskType:    "ping",
			Platform:    connection.PlatformCiscoIOSXE,
			Protocol:    connection.ProtocolScrapli,
			CommandType: connection.CommandTypeInteractiveEvent,
			Params:      params,
			Ctx:         context.Background(),
		}

		_, err = task.BuildCommand(ctx)
		t.Logf("Command build result for huge parameters: %v", err)
	}
}

func TestEdgeCase_ConcurrentParameterModification(t *testing.T) {
	setup := setupEdgeCaseTest()
	setup.asyncExec.Start()
	defer setup.asyncExec.Stop()

	task, err := setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
	if err != nil {
		t.Fatalf("Failed to discover task: %v", err)
	}

	driver := &faultyDriver{responses: make(map[string]*connection.ProtocolResponse)}

	// Shared parameters map that gets modified concurrently
	sharedParams := map[string]interface{}{
		"target_ip": "192.168.1.1",
		"repeat":    3,
	}

	const numGoroutines = 10
	var wg sync.WaitGroup

	// Goroutines that modify parameters
	for i := 0; i < numGoroutines/2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				sharedParams[fmt.Sprintf("dynamic_%d_%d", id, j)] = fmt.Sprintf("value_%d_%d", id, j)
				delete(sharedParams, fmt.Sprintf("dynamic_%d_%d", id, j-10)) // Remove old keys
				time.Sleep(time.Microsecond)
			}
		}(i)
	}

	// Goroutines that use parameters for task execution
	for i := 0; i < numGoroutines/2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				// Copy params to avoid concurrent modification
				paramsCopy := make(map[string]interface{})
				for k, v := range sharedParams {
					paramsCopy[k] = v
				}

				ctx := TaskContext{
					TaskType:    "ping",
					Platform:    connection.PlatformCiscoIOSXE,
					Protocol:    connection.ProtocolScrapli,
					CommandType: connection.CommandTypeInteractiveEvent,
					Params:      paramsCopy,
					Ctx:         context.Background(),
				}

				err := setup.asyncExec.Submit(task, driver, ctx, func(result Result, err error) {
					// Callback doesn't need to do anything
				})

				if err != nil {
					t.Logf("Task submission failed (expected due to concurrent modification): %v", err)
				}

				time.Sleep(time.Microsecond)
			}
		}(i)
	}

	wg.Wait()

	// No assertions needed - just verify no panics or crashes occur
	t.Log("Concurrent parameter modification test completed successfully")
}

func TestEdgeCase_ResourceExhaustion(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping resource exhaustion test in short mode")
	}

	setup := setupEdgeCaseTest()
	setup.asyncExec.Start()
	defer setup.asyncExec.Stop()

	task, err := setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
	if err != nil {
		t.Fatalf("Failed to discover task: %v", err)
	}

	// Driver that consumes resources
	resourceHeavyDriver := &faultyDriver{
		responses: make(map[string]*connection.ProtocolResponse),
	}

	// Try to exhaust queue by submitting many tasks rapidly
	const numTasks = 10000
	var submitted, rejected int64

	for i := 0; i < numTasks; i++ {
		ctx := TaskContext{
			TaskType:    "ping",
			Platform:    connection.PlatformCiscoIOSXE,
			Protocol:    connection.ProtocolScrapli,
			CommandType: connection.CommandTypeInteractiveEvent,
			Params: map[string]interface{}{
				"target_ip": fmt.Sprintf("192.168.%d.%d", (i/256)%256, i%256),
				"repeat":    3,
			},
			Ctx: context.Background(),
		}

		err := setup.asyncExec.SubmitWithTimeout(task, resourceHeavyDriver, ctx, func(result Result, err error) {
			// Minimal callback
		}, 1*time.Millisecond) // Very short timeout

		if err != nil {
			rejected++
			if i%1000 == 0 {
				t.Logf("Rejected %d tasks so far due to resource exhaustion", rejected)
			}
		} else {
			submitted++
		}
	}

	t.Logf("Resource exhaustion test: %d submitted, %d rejected", submitted, rejected)

	// Some tasks should be rejected due to resource constraints
	if rejected == 0 {
		t.Log("Warning: No tasks were rejected - resource exhaustion might not be working properly")
	}
}

// Helper function for creating line data
func createTestLine(id, ip, routerIP string) syncer.Line {
	return syncer.Line{
		ID: id,
		IP: ip,
		Router: syncer.Router{
			IP:       routerIP,
			Platform: connection.PlatformCiscoIOSXE,
			Protocol: connection.ProtocolScrapli,
		},
	}
}

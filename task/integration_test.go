package task

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/charlesren/zabbix_ddl_monitor/connection"
	"github.com/charlesren/zabbix_ddl_monitor/syncer"
)

// Integration test fixtures
type integrationTestSetup struct {
	registry   Registry
	executor   *Executor
	asyncExec  *AsyncExecutor
	aggregator *Aggregator
	mockDriver *mockIntegrationDriver
	handler    *integrationResultHandler
}

type mockIntegrationDriver struct {
	responses map[string]*connection.ProtocolResponse
	calls     []string
	mu        sync.Mutex
	delay     time.Duration
}

func (m *mockIntegrationDriver) ProtocolType() connection.Protocol {
	return connection.ProtocolScrapli
}

func (m *mockIntegrationDriver) Close() error {
	return nil
}

func (m *mockIntegrationDriver) Execute(req *connection.ProtocolRequest) (*connection.ProtocolResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Record the call
	m.calls = append(m.calls, fmt.Sprintf("%s:%v", req.CommandType, req.Payload))

	// Simulate delay
	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	// Return predefined response
	key := string(req.CommandType)
	if response, ok := m.responses[key]; ok {
		return response, nil
	}

	// Default response
	return &connection.ProtocolResponse{
		Success: true,
		RawData: []byte("Success rate is 100 percent (5/5), round-trip min/avg/max = 1/2/4 ms"),
	}, nil
}

func (m *mockIntegrationDriver) GetCapability() connection.ProtocolCapability {
	return connection.ProtocolCapability{
		Protocol: connection.ProtocolScrapli,
		PlatformSupport: []connection.Platform{
			connection.PlatformCiscoIOSXE,
			connection.PlatformHuaweiVRP,
		},
		CommandTypesSupport: []connection.CommandType{
			connection.CommandTypeCommands,
			connection.CommandTypeInteractiveEvent,
		},
	}
}

func (m *mockIntegrationDriver) getCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	calls := make([]string, len(m.calls))
	copy(calls, m.calls)
	return calls
}

func (m *mockIntegrationDriver) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = nil
}

type integrationResultHandler struct {
	events []ResultEvent
	mu     sync.Mutex
}

func (h *integrationResultHandler) HandleResult(event ResultEvent) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, event)
	return nil
}

func (h *integrationResultHandler) getEvents() []ResultEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	events := make([]ResultEvent, len(h.events))
	copy(events, h.events)
	return events
}

func (h *integrationResultHandler) reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = nil
}

func setupIntegrationTest() *integrationTestSetup {
	registry := NewDefaultRegistry()
	mockDriver := &mockIntegrationDriver{
		responses: make(map[string]*connection.ProtocolResponse),
	}
	handler := &integrationResultHandler{}

	// Register PingTask
	pingTask := &PingTask{}
	registry.Register(pingTask.Meta())

	// Setup executor
	executor := NewExecutor(nil)

	// Setup async executor
	asyncExec := NewAsyncExecutor(2)

	// Setup aggregator
	aggregator := NewAggregator(1, 10, 100*time.Millisecond)
	aggregator.AddHandler(handler)

	return &integrationTestSetup{
		registry:   registry,
		executor:   executor,
		asyncExec:  asyncExec,
		aggregator: aggregator,
		mockDriver: mockDriver,
		handler:    handler,
	}
}

func TestIntegration_BasicPingWorkflow(t *testing.T) {
	setup := setupIntegrationTest()
	defer setup.asyncExec.Stop()
	defer setup.aggregator.Stop()

	// Start components
	setup.asyncExec.Start()
	setup.aggregator.Start()

	// Discover ping task
	task, err := setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
	if err != nil {
		t.Fatalf("Failed to discover ping task: %v", err)
	}

	// Create task context
	ctx := TaskContext{
		TaskType:    "ping",
		Platform:    connection.PlatformCiscoIOSXE,
		Protocol:    connection.ProtocolScrapli,
		CommandType: connection.CommandTypeInteractiveEvent,
		Params: map[string]interface{}{
			"target_ip": "192.168.1.1",
			"repeat":    5,
			"timeout":   10 * time.Second,
		},
		Ctx: context.Background(),
	}

	// Execute task synchronously
	result, err := setup.executor.Execute(task, setup.mockDriver, ctx)
	if err != nil {
		t.Fatalf("Failed to execute task: %v", err)
	}

	// Verify result
	if !result.Success {
		t.Errorf("Expected successful result, got: %v", result)
	}

	// Verify driver was called
	calls := setup.mockDriver.getCalls()
	if len(calls) == 0 {
		t.Error("Expected driver to be called")
	}
}

func TestIntegration_AsyncExecutionWithAggregation(t *testing.T) {
	setup := setupIntegrationTest()
	setup.asyncExec.Start()
	setup.aggregator.Start()
	defer setup.asyncExec.Stop()
	defer setup.aggregator.Stop()

	// Discover ping task
	task, err := setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
	if err != nil {
		t.Fatalf("Failed to discover ping task: %v", err)
	}

	// Create test line
	line := syncer.Line{
		ID: "line-001",
		IP: "192.168.1.1",
		Router: syncer.Router{
			IP:       "192.168.1.100",
			Platform: connection.PlatformCiscoIOSXE,
		},
	}

	// Submit async task
	ctx := TaskContext{
		TaskType:    "ping",
		Platform:    connection.PlatformCiscoIOSXE,
		Protocol:    connection.ProtocolScrapli,
		CommandType: connection.CommandTypeInteractiveEvent,
		Params: map[string]interface{}{
			"target_ip": line.IP,
			"repeat":    3,
		},
		Ctx: context.Background(),
	}

	var taskCompleted bool
	var taskResult Result
	var taskError error
	var mu sync.Mutex

	err = setup.asyncExec.Submit(task, setup.mockDriver, ctx, func(result Result, err error) {
		mu.Lock()
		defer mu.Unlock()
		taskCompleted = true
		taskResult = result
		taskError = err

		// Submit result to aggregator
		if submitErr := setup.aggregator.SubmitTaskResult(line, "ping", result, 150*time.Millisecond); submitErr != nil {
			t.Errorf("Failed to submit result to aggregator: %v", submitErr)
		}
	})

	if err != nil {
		t.Fatalf("Failed to submit async task: %v", err)
	}

	// Wait for task completion
	timeout := time.After(2 * time.Second)
	for {
		mu.Lock()
		completed := taskCompleted
		mu.Unlock()

		if completed {
			break
		}

		select {
		case <-timeout:
			t.Fatal("Task did not complete within timeout")
		case <-time.After(10 * time.Millisecond):
			continue
		}
	}

	// Verify task result
	mu.Lock()
	defer mu.Unlock()

	if taskError != nil {
		t.Errorf("Expected no task error, got: %v", taskError)
	}

	if !taskResult.Success {
		t.Error("Expected successful task result")
	}

	// Wait for aggregator to process
	time.Sleep(200 * time.Millisecond)

	// Verify aggregator received the event
	events := setup.handler.getEvents()
	if len(events) == 0 {
		t.Fatal("Expected at least one event in aggregator")
	}

	event := events[0]
	if event.LineID != line.ID {
		t.Errorf("Expected LineID %s, got %s", line.ID, event.LineID)
	}
	if event.IP != line.IP {
		t.Errorf("Expected IP %s, got %s", line.IP, event.IP)
	}
	if !event.Success {
		t.Error("Expected successful event")
	}
}

func TestIntegration_BatchIPProcessing(t *testing.T) {
	setup := setupIntegrationTest()
	defer setup.asyncExec.Stop()
	defer setup.aggregator.Stop()

	setup.asyncExec.Start()
	setup.aggregator.Start()

	// Discover ping task
	task, err := setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
	if err != nil {
		t.Fatalf("Failed to discover ping task: %v", err)
	}

	// Setup mock response for batch ping
	batchOutput := `Router>ping 192.168.1.1 repeat 3 timeout 10
Success rate is 100 percent (3/3), round-trip min/avg/max = 1/2/4 ms
Router>ping 192.168.1.2 repeat 3 timeout 10
Success rate is 100 percent (3/3), round-trip min/avg/max = 2/3/5 ms
Router>ping 192.168.1.3 repeat 3 timeout 10
Success rate is 0 percent (0/3)`

	setup.mockDriver.responses[string(connection.CommandTypeInteractiveEvent)] = &connection.ProtocolResponse{
		Success: true,
		RawData: []byte(batchOutput),
	}

	// Create batch task context
	targetIPs := []string{"192.168.1.1", "192.168.1.2", "192.168.1.3"}
	ctx := TaskContext{
		TaskType:    "ping",
		Platform:    connection.PlatformCiscoIOSXE,
		Protocol:    connection.ProtocolScrapli,
		CommandType: connection.CommandTypeInteractiveEvent,
		Params: map[string]interface{}{
			"target_ips": targetIPs,
			"repeat":     3,
		},
		Ctx: context.Background(),
	}

	result, err := setup.executor.Execute(task, setup.mockDriver, ctx)
	if err != nil {
		t.Fatalf("Failed to execute batch task: %v", err)
	}

	// Verify batch result
	if !result.Success {
		t.Error("Expected overall batch success")
	}

	batchMode, ok := result.Data["batch_mode"].(bool)
	if !ok || !batchMode {
		t.Error("Expected batch_mode=true")
	}

	batchResults, ok := result.Data["batch_results"].(map[string]Result)
	if !ok {
		t.Fatal("Expected batch_results map")
	}

	if len(batchResults) != 3 {
		t.Errorf("Expected 3 batch results, got %d", len(batchResults))
	}

	// Verify individual IP results
	expectedResults := map[string]bool{
		"192.168.1.1": true,  // success
		"192.168.1.2": true,  // success
		"192.168.1.3": false, // failure
	}

	for ip, expectedSuccess := range expectedResults {
		if ipResult, exists := batchResults[ip]; !exists {
			t.Errorf("Expected result for IP %s", ip)
		} else if ipResult.Success != expectedSuccess {
			t.Errorf("Expected IP %s success=%t, got %t", ip, expectedSuccess, ipResult.Success)
		}
	}

	// Verify statistics
	if successCount, ok := result.Data["success_count"].(int); !ok || successCount != 2 {
		t.Errorf("Expected success_count=2, got %v", result.Data["success_count"])
	}

	if totalCount, ok := result.Data["total_count"].(int); !ok || totalCount != 3 {
		t.Errorf("Expected total_count=3, got %v", result.Data["total_count"])
	}
}

func TestIntegration_ErrorHandling(t *testing.T) {
	setup := setupIntegrationTest()
	defer setup.asyncExec.Stop()

	setup.asyncExec.Start()

	// Test with invalid task parameters
	task, err := setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
	if err != nil {
		t.Fatalf("Failed to discover ping task: %v", err)
	}

	// Create context with invalid parameters
	ctx := TaskContext{
		TaskType:    "ping",
		Platform:    connection.PlatformCiscoIOSXE,
		Protocol:    connection.ProtocolScrapli,
		CommandType: connection.CommandTypeInteractiveEvent,
		Params: map[string]interface{}{
			// Missing target_ip - should cause validation error
			"repeat": 5,
		},
		Ctx: context.Background(),
	}

	// Execute and expect error
	result, err := setup.executor.Execute(task, setup.mockDriver, ctx)
	if err == nil {
		t.Error("Expected validation error but got none")
	}

	if result.Success {
		t.Error("Expected failed result due to validation error")
	}

	if result.Error == "" {
		t.Error("Expected error message in result")
	}
}

func TestIntegration_PlatformAdapterIntegration(t *testing.T) {
	setup := setupIntegrationTest()

	// Test with different platforms
	platforms := []connection.Platform{
		connection.PlatformCiscoIOSXE,
		connection.PlatformHuaweiVRP,
		connection.PlatformCiscoIOSXR,
	}

	for _, platform := range platforms {
		t.Run(string(platform), func(t *testing.T) {
			_, err := setup.registry.Discover("ping", platform, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
			if err != nil {
				t.Skipf("Platform %s not supported: %v", platform, err)
				return
			}

			adapter := GetAdapter(platform)
			if adapter == nil {
				t.Fatal("Expected non-nil adapter")
			}

			// Test parameter normalization
			params := map[string]interface{}{
				"target_ip": "192.168.1.1",
			}

			normalizedParams := adapter.NormalizeParams(params)
			if len(normalizedParams) == 0 {
				t.Error("Expected normalized parameters")
			}

			// Verify target_ip is preserved
			if normalizedParams["target_ip"] != "192.168.1.1" {
				t.Error("Expected target_ip to be preserved")
			}

			// Test platform-specific validation
			err = adapter.ValidatePlatformParams(normalizedParams)
			if err != nil {
				t.Errorf("Platform validation failed: %v", err)
			}

			// Test command template
			template, err := adapter.GetCommandTemplate("ping", connection.CommandTypeCommands)
			if err != nil {
				t.Errorf("Failed to get command template: %v", err)
			}
			if template == "" {
				t.Error("Expected non-empty command template")
			}

			// Test output conversion
			mockOutput := "Success rate is 100 percent"
			converted := adapter.ConvertOutput(mockOutput)
			if converted == nil {
				t.Error("Expected converted output")
			}
			if converted["raw_output"] != mockOutput {
				t.Error("Expected raw_output to be preserved")
			}
		})
	}
}

func TestIntegration_ConcurrentExecution(t *testing.T) {
	setup := setupIntegrationTest()
	setup.asyncExec.Start()
	setup.aggregator.Start()
	defer setup.asyncExec.Stop()
	defer setup.aggregator.Stop()

	// Add small delay to simulate real network conditions
	setup.mockDriver.delay = 10 * time.Millisecond

	task, err := setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
	if err != nil {
		t.Fatalf("Failed to discover ping task: %v", err)
	}

	const numConcurrentTasks = 20
	var wg sync.WaitGroup
	var completedTasks int32
	var mu sync.Mutex

	// Submit multiple concurrent tasks
	for i := 0; i < numConcurrentTasks; i++ {
		wg.Add(1)
		go func(taskID int) {
			defer wg.Done()

			ctx := TaskContext{
				TaskType:    "ping",
				Platform:    connection.PlatformCiscoIOSXE,
				Protocol:    connection.ProtocolScrapli,
				CommandType: connection.CommandTypeInteractiveEvent,
				Params: map[string]interface{}{
					"target_ip": fmt.Sprintf("192.168.1.%d", taskID%254+1),
					"repeat":    3,
				},
				Ctx: context.Background(),
			}

			err := setup.asyncExec.Submit(task, setup.mockDriver, ctx, func(result Result, err error) {
				mu.Lock()
				completedTasks++
				mu.Unlock()

				if err != nil {
					t.Errorf("Task %d failed: %v", taskID, err)
				}
			})

			if err != nil {
				t.Errorf("Failed to submit task %d: %v", taskID, err)
			}
		}(i)
	}

	wg.Wait()

	// Wait for all tasks to complete
	timeout := time.After(5 * time.Second)
	for {
		mu.Lock()
		completed := completedTasks
		mu.Unlock()

		if int(completed) >= numConcurrentTasks {
			break
		}

		select {
		case <-timeout:
			mu.Lock()
			finalCompleted := completedTasks
			mu.Unlock()
			t.Fatalf("Only %d out of %d tasks completed within timeout", finalCompleted, numConcurrentTasks)
		case <-time.After(50 * time.Millisecond):
			continue
		}
	}

	// Verify all driver calls were made
	calls := setup.mockDriver.getCalls()
	if len(calls) < numConcurrentTasks {
		t.Errorf("Expected at least %d driver calls, got %d", numConcurrentTasks, len(calls))
	}
}

func TestIntegration_EndToEndLineMonitoring(t *testing.T) {
	setup := setupIntegrationTest()
	setup.asyncExec.Start()
	setup.aggregator.Start()
	defer setup.asyncExec.Stop()
	defer setup.aggregator.Stop()

	// Simulate a complete line monitoring workflow
	task, err := setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
	if err != nil {
		t.Fatalf("Failed to discover ping task: %v", err)
	}

	// Create multiple lines for the same router
	lines := []syncer.Line{
		{
			ID: "line-001",
			IP: "192.168.1.1",
			Router: syncer.Router{
				IP:       "192.168.1.100",
				Platform: connection.PlatformCiscoIOSXE,
				Protocol: connection.ProtocolScrapli,
			},
		},
		{
			ID: "line-002",
			IP: "192.168.1.2",
			Router: syncer.Router{
				IP:       "192.168.1.100",
				Platform: connection.PlatformCiscoIOSXE,
				Protocol: connection.ProtocolScrapli,
			},
		},
		{
			ID: "line-003",
			IP: "192.168.1.3",
			Router: syncer.Router{
				IP:       "192.168.1.100",
				Platform: connection.PlatformCiscoIOSXE,
				Protocol: connection.ProtocolScrapli,
			},
		},
	}

	// Extract IPs for batch processing
	targetIPs := make([]string, len(lines))
	for i, line := range lines {
		targetIPs[i] = line.IP
	}

	// Create batch task context
	ctx := TaskContext{
		TaskType:    "ping",
		Platform:    connection.PlatformCiscoIOSXE,
		Protocol:    connection.ProtocolScrapli,
		CommandType: connection.CommandTypeInteractiveEvent,
		Params: map[string]interface{}{
			"target_ips": targetIPs,
			"repeat":     3,
			"timeout":    10 * time.Second,
		},
		Ctx: context.Background(),
	}

	// Setup batch response
	batchResponse := `Router>ping 192.168.1.1 repeat 3 timeout 10
Success rate is 100 percent (3/3)
Router>ping 192.168.1.2 repeat 3 timeout 10
Success rate is 100 percent (3/3)
Router>ping 192.168.1.3 repeat 3 timeout 10
Success rate is 0 percent (0/3)`

	setup.mockDriver.responses[string(connection.CommandTypeInteractiveEvent)] = &connection.ProtocolResponse{
		Success: true,
		RawData: []byte(batchResponse),
	}

	var taskResult Result
	var taskError error
	var taskCompleted bool
	var mu sync.Mutex

	// Submit batch task
	err = setup.asyncExec.Submit(task, setup.mockDriver, ctx, func(result Result, err error) {
		mu.Lock()
		defer mu.Unlock()
		taskResult = result
		taskError = err
		taskCompleted = true

		// Distribute results to individual lines (simulating RouterScheduler behavior)
		if err == nil {
			if batchResults, ok := result.Data["batch_results"].(map[string]Result); ok {
				for _, line := range lines {
					if ipResult, exists := batchResults[line.IP]; exists {
						submitErr := setup.aggregator.SubmitTaskResult(line, "ping", ipResult, 200*time.Millisecond)
						if submitErr != nil {
							t.Errorf("Failed to submit result for line %s: %v", line.ID, submitErr)
						}
					}
				}
			}
		}
	})

	if err != nil {
		t.Fatalf("Failed to submit batch task: %v", err)
	}

	// Wait for task completion
	timeout := time.After(3 * time.Second)
	for {
		mu.Lock()
		completed := taskCompleted
		mu.Unlock()

		if completed {
			break
		}

		select {
		case <-timeout:
			t.Fatal("Batch task did not complete within timeout")
		case <-time.After(10 * time.Millisecond):
			continue
		}
	}

	// Verify task execution
	mu.Lock()
	defer mu.Unlock()

	if taskError != nil {
		t.Errorf("Expected no task error, got: %v", taskError)
	}

	if !taskResult.Success {
		t.Error("Expected successful batch task")
	}

	// Wait for aggregator processing
	time.Sleep(300 * time.Millisecond)

	// Verify all line events were processed
	events := setup.handler.getEvents()
	if len(events) != len(lines) {
		t.Errorf("Expected %d events (one per line), got %d", len(lines), len(events))
	}

	// Verify event details
	eventsByLineID := make(map[string]ResultEvent)
	for _, event := range events {
		eventsByLineID[event.LineID] = event
	}

	for _, line := range lines {
		if event, exists := eventsByLineID[line.ID]; !exists {
			t.Errorf("Expected event for line %s", line.ID)
		} else {
			if event.IP != line.IP {
				t.Errorf("Expected IP %s for line %s, got %s", line.IP, line.ID, event.IP)
			}
			if event.RouterIP != line.Router.IP {
				t.Errorf("Expected RouterIP %s for line %s, got %s", line.Router.IP, line.ID, event.RouterIP)
			}
			// line-003 (192.168.1.3) should fail based on mock response
			expectedSuccess := line.IP != "192.168.1.3"
			if event.Success != expectedSuccess {
				t.Errorf("Expected success=%t for line %s, got %t", expectedSuccess, line.ID, event.Success)
			}
		}
	}

	// Verify aggregator stats
	stats := setup.aggregator.GetStats()
	if stats.TotalEvents != int64(len(lines)) {
		t.Errorf("Expected %d total events in stats, got %d", len(lines), stats.TotalEvents)
	}
	if stats.SuccessEvents != 2 {
		t.Errorf("Expected 2 success events in stats, got %d", stats.SuccessEvents)
	}
	if stats.FailedEvents != 1 {
		t.Errorf("Expected 1 failed event in stats, got %d", stats.FailedEvents)
	}
}

// Benchmark integration test
func BenchmarkIntegration_PingExecution(b *testing.B) {
	setup := setupIntegrationTest()
	setup.asyncExec.Start()
	defer setup.asyncExec.Stop()

	task, err := setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
	if err != nil {
		b.Fatalf("Failed to discover ping task: %v", err)
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

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = setup.executor.Execute(task, setup.mockDriver, ctx)
	}
}

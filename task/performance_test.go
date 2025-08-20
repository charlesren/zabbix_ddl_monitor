package task

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charlesren/zabbix_ddl_monitor/connection"
)

// Performance test setup
type performanceTestSetup struct {
	registry   Registry
	executor   *Executor
	asyncExec  *AsyncExecutor
	aggregator *Aggregator
	mockDriver *mockPerformanceDriver
}

type mockPerformanceDriver struct {
	executionDelay time.Duration
	responses      map[string]*connection.ProtocolResponse
	callCount      int64
	mu             sync.RWMutex
}

func (m *mockPerformanceDriver) ProtocolType() connection.Protocol {
	return connection.ProtocolScrapli
}

func (m *mockPerformanceDriver) Close() error {
	return nil
}

func (m *mockPerformanceDriver) Execute(ctx context.Context, req *connection.ProtocolRequest) (*connection.ProtocolResponse, error) {
	atomic.AddInt64(&m.callCount, 1)

	if m.executionDelay > 0 {
		time.Sleep(m.executionDelay)
	}

	m.mu.RLock()
	response, exists := m.responses[string(req.CommandType)]
	m.mu.RUnlock()

	if !exists {
		response = &connection.ProtocolResponse{
			Success: true,
			RawData: []byte("Success rate is 100 percent (3/3), round-trip min/avg/max = 1/2/4 ms"),
		}
	}

	return response, nil
}

func (m *mockPerformanceDriver) GetCapability() connection.ProtocolCapability {
	return connection.ProtocolCapability{
		Protocol:            connection.ProtocolScrapli,
		PlatformSupport:     []connection.Platform{connection.PlatformCiscoIOSXE},
		CommandTypesSupport: []connection.CommandType{connection.CommandTypeInteractiveEvent},
		MaxConcurrent:       10,
		Timeout:             30 * time.Second,
	}
}

func (m *mockPerformanceDriver) getCallCount() int64 {
	return atomic.LoadInt64(&m.callCount)
}

func (m *mockPerformanceDriver) resetCallCount() {
	atomic.StoreInt64(&m.callCount, 0)
}

func setupPerformanceTest() *performanceTestSetup {
	registry := NewDefaultRegistry()
	executor := NewExecutor(nil)
	asyncExec := NewAsyncExecutor(runtime.NumCPU())
	aggregator := NewAggregator(runtime.NumCPU(), 1000, 100*time.Millisecond)

	mockDriver := &mockPerformanceDriver{
		responses: make(map[string]*connection.ProtocolResponse),
	}

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

	return &performanceTestSetup{
		registry:   registry,
		executor:   executor,
		asyncExec:  asyncExec,
		aggregator: aggregator,
		mockDriver: mockDriver,
	}
}

// Benchmark Tests

func BenchmarkTaskExecution_Single(b *testing.B) {
	setup := setupPerformanceTest()

	task, err := setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
	if err != nil {
		b.Fatalf("Failed to discover task: %v", err)
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
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := setup.executor.Execute(task, setup.mockDriver, ctx)
		if err != nil {
			b.Errorf("Task execution failed: %v", err)
		}
	}
}

func BenchmarkTaskExecution_Batch(b *testing.B) {
	setup := setupPerformanceTest()

	task, err := setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
	if err != nil {
		b.Fatalf("Failed to discover task: %v", err)
	}

	// Batch context with multiple IPs
	targetIPs := make([]string, 10)
	for i := 0; i < 10; i++ {
		targetIPs[i] = fmt.Sprintf("192.168.1.%d", i+1)
	}

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

	batchResponse := `Router>ping 192.168.1.1 repeat 3
Success rate is 100 percent (3/3)
Router>ping 192.168.1.2 repeat 3
Success rate is 100 percent (3/3)
Router>ping 192.168.1.3 repeat 3
Success rate is 100 percent (3/3)`

	setup.mockDriver.responses[string(connection.CommandTypeInteractiveEvent)] = &connection.ProtocolResponse{
		Success: true,
		RawData: []byte(batchResponse),
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := setup.executor.Execute(task, setup.mockDriver, ctx)
		if err != nil {
			b.Errorf("Batch task execution failed: %v", err)
		}
	}
}

func BenchmarkAsyncExecution_Parallel(b *testing.B) {
	setup := setupPerformanceTest()
	setup.asyncExec.Start()
	defer setup.asyncExec.Stop()

	task, err := setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
	if err != nil {
		b.Fatalf("Failed to discover task: %v", err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
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

			done := make(chan bool, 1)
			err := setup.asyncExec.Submit(task, setup.mockDriver, ctx, func(result Result, err error) {
				done <- true
			})

			if err != nil {
				b.Errorf("Failed to submit task: %v", err)
				continue
			}

			// Wait for completion
			select {
			case <-done:
				// Task completed
			case <-time.After(1 * time.Second):
				b.Error("Task timed out")
			}
		}
	})
}

func BenchmarkAggregatorProcessing(b *testing.B) {
	setup := setupPerformanceTest()
	setup.aggregator.Start()
	defer setup.aggregator.Stop()

	handler := &mockResultHandler{}
	setup.aggregator.AddHandler(handler)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		event := ResultEvent{
			LineID:    fmt.Sprintf("line-%d", i),
			IP:        fmt.Sprintf("192.168.1.%d", (i%254)+1),
			RouterIP:  "192.168.1.100",
			TaskType:  "ping",
			Success:   true,
			Timestamp: time.Now(),
			Data: map[string]interface{}{
				"packet_loss":    0.0,
				"avg_rtt":        "2ms",
				"execution_time": time.Duration(100) * time.Millisecond,
			},
		}

		err := setup.aggregator.Submit(event)
		if err != nil {
			b.Errorf("Failed to submit event: %v", err)
		}
	}
}

// Stress Tests

func TestStress_HighVolumeTaskExecution(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	setup := setupPerformanceTest()
	setup.asyncExec.Start()
	defer setup.asyncExec.Stop()

	task, err := setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
	if err != nil {
		t.Fatalf("Failed to discover task: %v", err)
	}

	const numTasks = 1000
	const concurrency = 20

	var completed int64
	var failed int64
	var wg sync.WaitGroup

	startTime := time.Now()

	// Launch concurrent workers
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < numTasks/concurrency; j++ {
				ctx := TaskContext{
					TaskType:    "ping",
					Platform:    connection.PlatformCiscoIOSXE,
					Protocol:    connection.ProtocolScrapli,
					CommandType: connection.CommandTypeInteractiveEvent,
					Params: map[string]interface{}{
						"target_ip": fmt.Sprintf("192.168.%d.%d", workerID+1, j+1),
						"repeat":    3,
					},
					Ctx: context.Background(),
				}

				err := setup.asyncExec.Submit(task, setup.mockDriver, ctx, func(result Result, err error) {
					if err != nil {
						atomic.AddInt64(&failed, 1)
					} else {
						atomic.AddInt64(&completed, 1)
					}
				})

				if err != nil {
					t.Logf("Worker %d: Failed to submit task %d: %v", workerID, j, err)
					atomic.AddInt64(&failed, 1)
				}
			}
		}(i)
	}

	wg.Wait()

	// Wait for all tasks to complete
	timeout := time.After(30 * time.Second)
	for {
		completedCount := atomic.LoadInt64(&completed)
		failedCount := atomic.LoadInt64(&failed)
		total := completedCount + failedCount

		if total >= numTasks {
			break
		}

		select {
		case <-timeout:
			t.Errorf("Timeout waiting for tasks to complete. Completed: %d, Failed: %d, Total: %d/%d",
				completedCount, failedCount, total, numTasks)
			return
		case <-time.After(100 * time.Millisecond):
			continue
		}
	}

	duration := time.Since(startTime)
	completedCount := atomic.LoadInt64(&completed)
	failedCount := atomic.LoadInt64(&failed)

	t.Logf("Stress test completed in %v", duration)
	t.Logf("Tasks completed: %d", completedCount)
	t.Logf("Tasks failed: %d", failedCount)
	t.Logf("Success rate: %.2f%%", float64(completedCount)/float64(numTasks)*100)
	t.Logf("Throughput: %.2f tasks/second", float64(numTasks)/duration.Seconds())

	if completedCount < numTasks/2 {
		t.Errorf("Too many failures: %d/%d tasks failed", failedCount, numTasks)
	}
}

func TestStress_MemoryUsage(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	setup := setupPerformanceTest()
	setup.asyncExec.Start()
	setup.aggregator.Start()
	defer setup.asyncExec.Stop()
	defer setup.aggregator.Stop()

	// Force garbage collection before starting
	runtime.GC()
	runtime.GC() // Run twice to be sure

	var startMem, endMem runtime.MemStats
	runtime.ReadMemStats(&startMem)

	task, err := setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
	if err != nil {
		t.Fatalf("Failed to discover task: %v", err)
	}

	const numIterations = 10000
	var completed int64

	for i := 0; i < numIterations; i++ {
		ctx := TaskContext{
			TaskType:    "ping",
			Platform:    connection.PlatformCiscoIOSXE,
			Protocol:    connection.ProtocolScrapli,
			CommandType: connection.CommandTypeInteractiveEvent,
			Params: map[string]interface{}{
				"target_ip": fmt.Sprintf("192.168.1.%d", (i%254)+1),
				"repeat":    3,
			},
			Ctx: context.Background(),
		}

		err := setup.asyncExec.Submit(task, setup.mockDriver, ctx, func(result Result, err error) {
			atomic.AddInt64(&completed, 1)
		})

		if err != nil {
			t.Errorf("Failed to submit task %d: %v", i, err)
		}

		// Force GC every 1000 iterations to check for memory leaks
		if i%1000 == 0 && i > 0 {
			runtime.GC()
		}
	}

	// Wait for all tasks to complete
	timeout := time.After(60 * time.Second)
	for {
		if atomic.LoadInt64(&completed) >= numIterations {
			break
		}
		select {
		case <-timeout:
			t.Errorf("Timeout waiting for tasks. Completed: %d/%d", atomic.LoadInt64(&completed), numIterations)
			return
		case <-time.After(100 * time.Millisecond):
			continue
		}
	}

	// Force garbage collection and measure final memory
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&endMem)

	memoryIncrease := int64(endMem.Alloc) - int64(startMem.Alloc)
	memoryIncreaseKB := memoryIncrease / 1024

	t.Logf("Memory usage:")
	t.Logf("  Start: %d KB", startMem.Alloc/1024)
	t.Logf("  End: %d KB", endMem.Alloc/1024)
	t.Logf("  Increase: %d KB", memoryIncreaseKB)
	t.Logf("  Memory per task: %.2f bytes", float64(memoryIncrease)/float64(numIterations))

	// Check for significant memory leaks (more than 10MB increase)
	if memoryIncreaseKB > 10*1024 {
		t.Errorf("Potential memory leak detected: %d KB increase after %d tasks", memoryIncreaseKB, numIterations)
	}
}

func TestStress_ConcurrentAggregation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	setup := setupPerformanceTest()
	setup.aggregator.Start()
	defer setup.aggregator.Stop()

	handler := &mockResultHandler{}
	setup.aggregator.AddHandler(handler)

	const numWorkers = 10
	const eventsPerWorker = 1000
	const totalEvents = numWorkers * eventsPerWorker

	var wg sync.WaitGroup
	var submitted int64
	var errors int64

	startTime := time.Now()

	// Launch concurrent workers
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < eventsPerWorker; j++ {
				event := ResultEvent{
					LineID:    fmt.Sprintf("worker-%d-line-%d", workerID, j),
					IP:        fmt.Sprintf("192.168.%d.%d", workerID+1, (j%254)+1),
					RouterIP:  fmt.Sprintf("192.168.%d.1", workerID+1),
					TaskType:  "ping",
					Success:   j%10 != 0, // 90% success rate
					Timestamp: time.Now(),
					Data: map[string]interface{}{
						"worker_id": workerID,
						"event_id":  j,
					},
				}

				err := setup.aggregator.Submit(event)
				if err != nil {
					atomic.AddInt64(&errors, 1)
					t.Logf("Worker %d: Failed to submit event %d: %v", workerID, j, err)
				} else {
					atomic.AddInt64(&submitted, 1)
				}
			}
		}(i)
	}

	wg.Wait()

	// Wait for aggregator to process all events
	time.Sleep(2 * time.Second)

	duration := time.Since(startTime)
	submittedCount := atomic.LoadInt64(&submitted)
	errorCount := atomic.LoadInt64(&errors)

	// Get aggregator stats
	stats := setup.aggregator.GetStats()

	t.Logf("Concurrent aggregation test completed in %v", duration)
	t.Logf("Events submitted: %d", submittedCount)
	t.Logf("Submission errors: %d", errorCount)
	t.Logf("Events processed: %d", stats.TotalEvents)
	t.Logf("Success events: %d", stats.SuccessEvents)
	t.Logf("Failed events: %d", stats.FailedEvents)
	t.Logf("Throughput: %.2f events/second", float64(submittedCount)/duration.Seconds())

	// Verify results
	if submittedCount < totalEvents*9/10 { // Allow for some submission failures
		t.Errorf("Too many submission failures: %d/%d events submitted", submittedCount, totalEvents)
	}

	if stats.TotalEvents < submittedCount*9/10 { // Allow for some processing delay
		t.Errorf("Not enough events processed: %d processed vs %d submitted", stats.TotalEvents, submittedCount)
	}
}

func TestStress_BatchProcessingEfficiency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	setup := setupPerformanceTest()
	setup.mockDriver.executionDelay = 10 * time.Millisecond // Simulate network latency

	task, err := setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
	if err != nil {
		t.Fatalf("Failed to discover task: %v", err)
	}

	// Test individual executions
	const numIPs = 50
	individualStartTime := time.Now()
	setup.mockDriver.resetCallCount()

	for i := 0; i < numIPs; i++ {
		ctx := TaskContext{
			TaskType:    "ping",
			Platform:    connection.PlatformCiscoIOSXE,
			Protocol:    connection.ProtocolScrapli,
			CommandType: connection.CommandTypeInteractiveEvent,
			Params: map[string]interface{}{
				"target_ip": fmt.Sprintf("192.168.1.%d", i+1),
				"repeat":    3,
			},
			Ctx: context.Background(),
		}

		_, err := setup.executor.Execute(task, setup.mockDriver, ctx)
		if err != nil {
			t.Errorf("Individual execution failed: %v", err)
		}
	}

	individualDuration := time.Since(individualStartTime)
	individualCalls := setup.mockDriver.getCallCount()

	// Test batch execution
	targetIPs := make([]string, numIPs)
	for i := 0; i < numIPs; i++ {
		targetIPs[i] = fmt.Sprintf("192.168.1.%d", i+1)
	}

	// Generate batch response
	batchResponse := "Router>ping batch\n"
	for i := 0; i < numIPs; i++ {
		batchResponse += fmt.Sprintf("Router>ping %s repeat 3\nSuccess rate is 100 percent (3/3)\n", targetIPs[i])
	}

	setup.mockDriver.responses[string(connection.CommandTypeInteractiveEvent)] = &connection.ProtocolResponse{
		Success: true,
		RawData: []byte(batchResponse),
	}

	batchStartTime := time.Now()
	setup.mockDriver.resetCallCount()

	batchCtx := TaskContext{
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

	_, err = setup.executor.Execute(task, setup.mockDriver, batchCtx)
	if err != nil {
		t.Errorf("Batch execution failed: %v", err)
	}

	batchDuration := time.Since(batchStartTime)
	batchCalls := setup.mockDriver.getCallCount()

	// Calculate efficiency improvements
	timeImprovement := float64(individualDuration-batchDuration) / float64(individualDuration) * 100
	callReduction := float64(individualCalls-batchCalls) / float64(individualCalls) * 100

	t.Logf("Batch processing efficiency test results:")
	t.Logf("Individual execution:")
	t.Logf("  Duration: %v", individualDuration)
	t.Logf("  Driver calls: %d", individualCalls)
	t.Logf("Batch execution:")
	t.Logf("  Duration: %v", batchDuration)
	t.Logf("  Driver calls: %d", batchCalls)
	t.Logf("Improvements:")
	t.Logf("  Time reduction: %.2f%%", timeImprovement)
	t.Logf("  Call reduction: %.2f%%", callReduction)

	// Verify efficiency improvements meet expectations
	if timeImprovement < 50 {
		t.Errorf("Expected at least 50%% time improvement, got %.2f%%", timeImprovement)
	}

	if callReduction < 90 {
		t.Errorf("Expected at least 90%% call reduction, got %.2f%%", callReduction)
	}
}

// Performance regression tests

func TestPerformanceRegression_TaskCreation(t *testing.T) {
	setup := setupPerformanceTest()

	const maxCreationTime = 1 * time.Millisecond

	task, err := setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
	if err != nil {
		t.Fatalf("Failed to discover task: %v", err)
	}

	// Warm up
	for i := 0; i < 100; i++ {
		setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
	}

	// Measure task creation time
	const numMeasurements = 1000
	var totalDuration time.Duration

	for i := 0; i < numMeasurements; i++ {
		startTime := time.Now()
		_, err := setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
		if err != nil {
			t.Errorf("Task discovery failed: %v", err)
		}
		totalDuration += time.Since(startTime)
	}

	avgCreationTime := totalDuration / numMeasurements

	t.Logf("Average task creation time: %v", avgCreationTime)

	if avgCreationTime > maxCreationTime {
		t.Errorf("Task creation time regression detected: %v > %v", avgCreationTime, maxCreationTime)
	}

	// Verify task is functional
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

	_, err = setup.executor.Execute(task, setup.mockDriver, ctx)
	if err != nil {
		t.Errorf("Task execution failed: %v", err)
	}
}

func TestPerformanceRegression_MemoryAllocation(t *testing.T) {
	setup := setupPerformanceTest()

	task, err := setup.registry.Discover("ping", connection.PlatformCiscoIOSXE, connection.ProtocolScrapli, connection.CommandTypeInteractiveEvent)
	if err != nil {
		t.Fatalf("Failed to discover task: %v", err)
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

	// Warm up
	for i := 0; i < 100; i++ {
		setup.executor.Execute(task, setup.mockDriver, ctx)
	}

	// Force GC and measure allocations
	runtime.GC()
	runtime.GC()

	var memBefore, memAfter runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	const numExecutions = 1000
	for i := 0; i < numExecutions; i++ {
		_, err := setup.executor.Execute(task, setup.mockDriver, ctx)
		if err != nil {
			t.Errorf("Task execution failed: %v", err)
		}
	}

	runtime.ReadMemStats(&memAfter)

	allocPerExecution := float64(memAfter.TotalAlloc-memBefore.TotalAlloc) / float64(numExecutions)
	const maxAllocPerExecution = 10 * 1024 // 10KB per execution

	t.Logf("Memory allocation per execution: %.2f bytes", allocPerExecution)

	if allocPerExecution > maxAllocPerExecution {
		t.Errorf("Memory allocation regression detected: %.2f bytes > %d bytes per execution",
			allocPerExecution, maxAllocPerExecution)
	}
}

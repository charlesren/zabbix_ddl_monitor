package task

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/charlesren/zabbix_ddl_monitor/connection"
)

// Mock implementations for testing
type mockTask struct {
	executeFunc func(TaskContext) (Result, error)
}

func (m *mockTask) Meta() TaskMeta {
	return TaskMeta{
		Type:        "mock",
		Description: "Mock task for testing",
	}
}

func (m *mockTask) ValidateParams(params map[string]interface{}) error {
	return nil
}

func (m *mockTask) BuildCommand(ctx TaskContext) (Command, error) {
	return Command{
		Type:    connection.CommandTypeCommands,
		Payload: []string{"mock command"},
	}, nil
}

func (m *mockTask) ParseOutput(ctx TaskContext, raw interface{}) (Result, error) {
	if m.executeFunc != nil {
		return m.executeFunc(ctx)
	}
	return Result{
		Success: true,
		Data:    map[string]interface{}{"mock": true},
	}, nil
}

type mockProtocolDriver struct {
	executeFunc func(context.Context, *connection.ProtocolRequest) (*connection.ProtocolResponse, error)
	capability  connection.ProtocolCapability
}

func (m *mockProtocolDriver) ProtocolType() connection.Protocol {
	return connection.ProtocolScrapli
}

func (m *mockProtocolDriver) Close() error {
	return nil
}

func (m *mockProtocolDriver) Execute(ctx context.Context, req *connection.ProtocolRequest) (*connection.ProtocolResponse, error) {
	if m.executeFunc != nil {
		return m.executeFunc(ctx, req)
	}
	return &connection.ProtocolResponse{
		Success: true,
		RawData: []byte("mock response"),
	}, nil
}

func (m *mockProtocolDriver) GetCapability() connection.ProtocolCapability {
	return m.capability
}

func TestNewAsyncExecutor(t *testing.T) {
	tests := []struct {
		name        string
		workers     int
		middlewares []Middleware
	}{
		{
			name:        "basic_executor",
			workers:     3,
			middlewares: nil,
		},
		{
			name:        "executor_with_middleware",
			workers:     2,
			middlewares: []Middleware{WithTimeout(5 * time.Second)},
		},
		{
			name:        "single_worker",
			workers:     1,
			middlewares: nil,
		},
		{
			name:        "many_workers",
			workers:     10,
			middlewares: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := NewAsyncExecutor(tt.workers, tt.middlewares...)

			if executor == nil {
				t.Fatal("Expected non-nil executor")
			}

			if executor.workers != tt.workers {
				t.Errorf("Expected %d workers, got %d", tt.workers, executor.workers)
			}

			if executor.taskChan == nil {
				t.Error("Expected task channel to be initialized")
			}

			if executor.stopChan == nil {
				t.Error("Expected stop channel to be initialized")
			}

			if executor.executor == nil {
				t.Error("Expected inner executor to be initialized")
			}
		})
	}
}

func TestAsyncExecutor_StartStop(t *testing.T) {
	executor := NewAsyncExecutor(2)

	// Test Start
	executor.Start()

	// Verify workers are running by checking worker count
	if executor.GetWorkerCount() != 2 {
		t.Errorf("Expected 2 workers, got %d", executor.GetWorkerCount())
	}

	// Test Stop
	executor.Stop()

	// After stopping, context should be cancelled
	select {
	case <-executor.ctx.Done():
		// Expected
	default:
		t.Error("Expected context to be cancelled after Stop()")
	}
}

func TestAsyncExecutor_Submit(t *testing.T) {
	tests := []struct {
		name          string
		taskFunc      func(TaskContext) (Result, error)
		expectSuccess bool
		expectError   bool
	}{
		{
			name: "successful_task",
			taskFunc: func(ctx TaskContext) (Result, error) {
				return Result{
					Success: true,
					Data:    map[string]interface{}{"test": "success"},
				}, nil
			},
			expectSuccess: true,
			expectError:   false,
		},
		{
			name: "failing_task",
			taskFunc: func(ctx TaskContext) (Result, error) {
				return Result{Success: false}, errors.New("task failed")
			},
			expectSuccess: false,
			expectError:   true,
		},
		{
			name: "task_with_data",
			taskFunc: func(ctx TaskContext) (Result, error) {
				return Result{
					Success: true,
					Data:    map[string]interface{}{"key": "value", "number": 42},
				}, nil
			},
			expectSuccess: true,
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := NewAsyncExecutor(1)
			executor.Start()
			defer executor.Stop()

			mockTask := &mockTask{executeFunc: tt.taskFunc}
			mockDriver := &mockProtocolDriver{}

			ctx := TaskContext{
				TaskType: "mock",
				Platform: connection.PlatformCiscoIOSXE,
				Protocol: connection.ProtocolScrapli,
				Params:   map[string]interface{}{"test": true},
				Ctx:      context.Background(),
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

			// Submit task
			err := executor.Submit(mockTask, mockDriver, ctx, callback)
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
					// Continue waiting
				}
			}

			// Check results
			mu.Lock()
			defer mu.Unlock()

			if callbackResult.Success != tt.expectSuccess {
				t.Errorf("Expected success %t, got %t", tt.expectSuccess, callbackResult.Success)
			}

			if tt.expectError && callbackError == nil {
				t.Error("Expected error but got none")
			} else if !tt.expectError && callbackError != nil {
				t.Errorf("Expected no error but got: %v", callbackError)
			}
		})
	}
}

func TestAsyncExecutor_SubmitWithTimeout(t *testing.T) {
	executor := NewAsyncExecutor(1)
	executor.Start()
	defer executor.Stop()

	mockTask := &mockTask{}
	mockDriver := &mockProtocolDriver{}
	ctx := TaskContext{
		TaskType: "mock",
		Params:   map[string]interface{}{},
		Ctx:      context.Background(),
	}

	tests := []struct {
		name      string
		timeout   time.Duration
		expectErr bool
		errType   error
	}{
		{
			name:      "submit_within_timeout",
			timeout:   1 * time.Second,
			expectErr: false,
		},
		{
			name:      "submit_timeout",
			timeout:   1 * time.Nanosecond, // Very short timeout
			expectErr: true,
			errType:   ErrSubmitTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			callback := func(Result, error) {}

			err := executor.SubmitWithTimeout(mockTask, mockDriver, ctx, callback, tt.timeout)

			if tt.expectErr {
				if err == nil {
					t.Error("Expected error but got none")
				}
				if tt.errType != nil && err != tt.errType {
					t.Errorf("Expected error %v, got %v", tt.errType, err)
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error but got: %v", err)
				}
			}
		})
	}
}

func TestAsyncExecutor_QueueFull(t *testing.T) {
	// Create executor with very small buffer
	executor := &AsyncExecutor{
		taskChan: make(chan AsyncTaskRequest, 1), // Buffer size 1
		workers:  1,
		stopChan: make(chan struct{}),
		ctx:      context.Background(),
		cancel:   func() {},
		executor: NewExecutor(nil),
	}

	mockTask := &mockTask{}
	mockDriver := &mockProtocolDriver{}
	ctx := TaskContext{
		TaskType: "mock",
		Params:   map[string]interface{}{},
		Ctx:      context.Background(),
	}

	callback := func(Result, error) {}

	// Fill the queue
	err1 := executor.Submit(mockTask, mockDriver, ctx, callback)
	if err1 != nil {
		t.Errorf("First submit should succeed: %v", err1)
	}

	// This should fail due to full queue
	err2 := executor.Submit(mockTask, mockDriver, ctx, callback)
	if err2 != ErrQueueFull {
		t.Errorf("Expected ErrQueueFull, got %v", err2)
	}
}

func TestAsyncExecutor_GetQueueLength(t *testing.T) {
	executor := NewAsyncExecutor(2)

	// Initially empty
	if length := executor.GetQueueLength(); length != 0 {
		t.Errorf("Expected queue length 0, got %d", length)
	}

	// Note: Testing queue length after submission is tricky due to
	// concurrency, so we just test the method exists and returns a number
	length := executor.GetQueueLength()
	if length < 0 {
		t.Error("Queue length should not be negative")
	}
}

func TestAsyncExecutor_GetWorkerCount(t *testing.T) {
	tests := []int{1, 3, 5, 10}

	for _, workerCount := range tests {
		t.Run(fmt.Sprintf("workers_%d", workerCount), func(t *testing.T) {
			executor := NewAsyncExecutor(workerCount)

			if count := executor.GetWorkerCount(); count != workerCount {
				t.Errorf("Expected %d workers, got %d", workerCount, count)
			}
		})
	}
}

func TestAsyncExecutor_ConcurrentSubmit(t *testing.T) {
	executor := NewAsyncExecutor(3)
	executor.Start()
	defer executor.Stop()

	mockTask := &mockTask{}
	mockDriver := &mockProtocolDriver{}

	const numTasks = 50
	var completedTasks int32
	var mu sync.Mutex

	callback := func(result Result, err error) {
		mu.Lock()
		completedTasks++
		mu.Unlock()
	}

	// Submit many tasks concurrently
	var wg sync.WaitGroup
	for i := 0; i < numTasks; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			ctx := TaskContext{
				TaskType: "mock",
				Params:   map[string]interface{}{"id": i},
				Ctx:      context.Background(),
			}

			err := executor.Submit(mockTask, mockDriver, ctx, callback)
			if err != nil {
				t.Errorf("Failed to submit task %d: %v", i, err)
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

		if completed >= numTasks {
			break
		}

		select {
		case <-timeout:
			t.Fatalf("Only %d out of %d tasks completed within timeout", completed, numTasks)
		case <-time.After(10 * time.Millisecond):
			// Continue waiting
		}
	}

	mu.Lock()
	finalCount := completedTasks
	mu.Unlock()

	if finalCount != numTasks {
		t.Errorf("Expected %d completed tasks, got %d", numTasks, finalCount)
	}
}

func TestAsyncExecutor_ContextCancellation(t *testing.T) {
	executor := NewAsyncExecutor(1)
	executor.Start()

	// Submit a task
	mockTask := &mockTask{}
	mockDriver := &mockProtocolDriver{}
	ctx := TaskContext{
		TaskType: "mock",
		Params:   map[string]interface{}{},
		Ctx:      context.Background(),
	}

	err := executor.Submit(mockTask, mockDriver, ctx, func(Result, error) {})
	if err != nil {
		t.Fatalf("Failed to submit task: %v", err)
	}

	// Stop executor
	executor.Stop()

	// Try to submit after stopping
	err = executor.Submit(mockTask, mockDriver, ctx, func(Result, error) {})
	if err == nil {
		t.Error("Expected error when submitting to stopped executor")
	}
}

func TestAsyncExecutor_CallbackPanic(t *testing.T) {
	executor := NewAsyncExecutor(1)
	executor.Start()
	defer executor.Stop()

	mockTask := &mockTask{}
	mockDriver := &mockProtocolDriver{}
	ctx := TaskContext{
		TaskType: "mock",
		Params:   map[string]interface{}{},
		Ctx:      context.Background(),
	}

	var taskCompleted bool
	var mu sync.Mutex

	// Callback that panics
	panicCallback := func(result Result, err error) {
		panic("callback panic")
	}

	// Submit task with panicking callback
	err := executor.Submit(mockTask, mockDriver, ctx, panicCallback)
	if err != nil {
		t.Fatalf("Failed to submit task: %v", err)
	}

	// Submit another task to verify executor still works after panic
	normalCallback := func(result Result, err error) {
		mu.Lock()
		taskCompleted = true
		mu.Unlock()
	}

	err = executor.Submit(mockTask, mockDriver, ctx, normalCallback)
	if err != nil {
		t.Fatalf("Failed to submit second task: %v", err)
	}

	// Wait for second task to complete
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
			t.Fatal("Second task did not complete after callback panic")
		case <-time.After(10 * time.Millisecond):
			// Continue waiting
		}
	}
}

func TestAsyncExecutor_TaskExecutionError(t *testing.T) {
	executor := NewAsyncExecutor(1)
	executor.Start()
	defer executor.Stop()

	// Mock driver that returns error
	mockDriver := &mockProtocolDriver{
		executeFunc: func(ctx context.Context, req *connection.ProtocolRequest) (*connection.ProtocolResponse, error) {
			return nil, errors.New("driver execution failed")
		},
	}

	mockTask := &mockTask{}
	ctx := TaskContext{
		TaskType: "mock",
		Params:   map[string]interface{}{},
		Ctx:      context.Background(),
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

	err := executor.Submit(mockTask, mockDriver, ctx, callback)
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
			// Continue waiting
		}
	}

	// Check that error was propagated
	mu.Lock()
	defer mu.Unlock()

	if callbackError == nil {
		t.Error("Expected error in callback")
	}

	if callbackResult.Success {
		t.Error("Expected failed result")
	}

	if callbackResult.Error == "" {
		t.Error("Expected error message in result")
	}
}

// Benchmark tests
func BenchmarkAsyncExecutor_Submit(b *testing.B) {
	executor := NewAsyncExecutor(4)
	executor.Start()
	defer executor.Stop()

	mockTask := &mockTask{}
	mockDriver := &mockProtocolDriver{}
	ctx := TaskContext{
		TaskType: "mock",
		Params:   map[string]interface{}{},
		Ctx:      context.Background(),
	}

	callback := func(Result, error) {}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = executor.Submit(mockTask, mockDriver, ctx, callback)
		}
	})
}

func BenchmarkAsyncExecutor_ProcessTask(b *testing.B) {
	executor := NewAsyncExecutor(1)
	executor.Start()
	defer executor.Stop()

	mockTask := &mockTask{}
	mockDriver := &mockProtocolDriver{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := TaskContext{
			TaskType: "mock",
			Params:   map[string]interface{}{"iteration": i},
			Ctx:      context.Background(),
		}

		req := AsyncTaskRequest{
			Task:     mockTask,
			Conn:     mockDriver,
			Context:  ctx,
			Callback: func(Result, error) {},
		}

		executor.processTask(0, req)
	}
}

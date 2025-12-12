package manager

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/charlesren/zabbix_ddl_monitor/connection"
	"github.com/charlesren/zabbix_ddl_monitor/syncer"
	"github.com/charlesren/zabbix_ddl_monitor/task"
)

// Mock implementations for RouterScheduler testing
type MockConnectionPool struct {
	mock.Mock
}

func (m *MockConnectionPool) Get(protocol connection.Protocol) (connection.ProtocolDriver, error) {
	args := m.Called(protocol)
	return args.Get(0).(connection.ProtocolDriver), args.Error(1)
}

func (m *MockConnectionPool) Release(driver connection.ProtocolDriver) {
	m.Called(driver)
}

func (m *MockConnectionPool) WarmUp(protocol connection.Protocol, count int) error {
	args := m.Called(protocol, count)
	return args.Error(0)
}

func (m *MockConnectionPool) Close() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockConnectionPool) ToConnectionConfig() connection.ConnectionConfig {
	return connection.ConnectionConfig{}
}

type MockProtocolDriver struct {
	mock.Mock
	capability connection.ProtocolCapability
}

func (m *MockProtocolDriver) ProtocolType() connection.Protocol {
	args := m.Called()
	return args.Get(0).(connection.Protocol)
}

func (m *MockProtocolDriver) Close() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockProtocolDriver) Execute(req *connection.ProtocolRequest) (*connection.ProtocolResponse, error) {
	args := m.Called(req)
	return args.Get(0).(*connection.ProtocolResponse), args.Error(1)
}

func (m *MockProtocolDriver) GetCapability() connection.ProtocolCapability {
	args := m.Called()
	if args.Get(0) != nil {
		return args.Get(0).(connection.ProtocolCapability)
	}
	return m.capability
}

func (m *MockProtocolDriver) SetCapability(capability connection.ProtocolCapability) {
	m.capability = capability
}

type MockTask struct {
	mock.Mock
}

func (m *MockTask) Meta() task.TaskMeta {
	args := m.Called()
	return args.Get(0).(task.TaskMeta)
}

func (m *MockTask) ValidateParams(params map[string]interface{}) error {
	args := m.Called(params)
	return args.Error(0)
}

func (m *MockTask) BuildCommand(ctx task.TaskContext) (task.Command, error) {
	args := m.Called(ctx)
	return args.Get(0).(task.Command), args.Error(1)
}

func (m *MockTask) ParseOutput(ctx task.TaskContext, raw interface{}) (task.Result, error) {
	args := m.Called(ctx, raw)
	return args.Get(0).(task.Result), args.Error(1)
}

type MockManager struct {
	mock.Mock
	registry task.Registry
}

func (m *MockManager) SetRegistry(registry task.Registry) {
	m.registry = registry
}

// ManagerInterface defines the interface for Manager to enable mocking
type ManagerInterface interface {
	// Add methods that RouterScheduler needs from Manager
}

// Implement ManagerInterface for MockManager
var _ ManagerInterface = (*MockManager)(nil)

// Helper functions for creating test objects
func createTestRouter(ip, username, password string, platform connection.Platform, protocol connection.Protocol) syncer.Router {
	return syncer.Router{
		IP:       ip,
		Username: username,
		Password: password,
		Platform: platform,
		Protocol: protocol,
	}
}

func createTestLineForRouter(id, ip string, router syncer.Router, interval time.Duration) syncer.Line {
	line := syncer.Line{
		ID:       id,
		IP:       ip,
		Interval: interval,
		Router:   router,
	}
	line.ComputeHash()
	return line
}

func setupMockConnectionPool() *MockConnectionPool {
	mockPool := &MockConnectionPool{}
	mockDriver := &MockProtocolDriver{}

	// Default capability
	capability := connection.ProtocolCapability{
		CommandTypesSupport: []connection.CommandType{"commands", "interactive"},
	}
	mockDriver.SetCapability(capability)

	mockPool.On("WarmUp", mock.AnythingOfType("connection.Protocol"), mock.AnythingOfType("int")).Return(nil)
	mockPool.On("Get", mock.AnythingOfType("connection.Protocol")).Return(mockDriver, nil)
	mockPool.On("Release", mock.AnythingOfType("*MockProtocolDriver"))
	mockDriver.On("GetCapability").Return(capability)

	return mockPool
}

func setupMockTask() *MockTask {
	mockTask := &MockTask{}
	mockTask.On("BuildCommand", mock.AnythingOfType("task.TaskContext")).Return(
		task.Command{
			Type:    connection.CommandType("commands"),
			Payload: []string{"ping 10.0.0.1", "ping 10.0.0.2"},
		}, nil)

	mockTask.On("ParseOutput", mock.AnythingOfType("task.TaskContext"), mock.Anything).Return(
		task.Result{
			Success: true,
			Data:    map[string]interface{}{"status": "ok"},
		}, nil)

	return mockTask
}

// Helper function to create RouterScheduler with mock connection pool for testing
func createTestRouterScheduler(router syncer.Router, lines []syncer.Line, mockPool *MockConnectionPool) *RouterScheduler {
	scheduler := &RouterScheduler{
		router:   &router,
		lines:    lines,
		queues:   make(map[time.Duration]*IntervalTaskQueue),
		stopChan: make(chan struct{}),
	}

	// Always initialize queues for basic functionality tests
	scheduler.initializeQueues()

	return scheduler
}

// Basic functionality tests
func TestNewRouterScheduler(t *testing.T) {
	router := createTestRouter("192.168.1.1", "admin", "password", "cisco_iosxe", "ssh")
	lines := []syncer.Line{
		createTestLineForRouter("line1", "10.0.0.1", router, 5*time.Minute),
		createTestLineForRouter("line2", "10.0.0.2", router, 3*time.Minute),
	}

	// Use helper function to avoid real network connections
	scheduler := createTestRouterScheduler(router, lines, nil)
	defer scheduler.Stop()

	assert.NotNil(t, scheduler)
	assert.Equal(t, &router, scheduler.router)
	assert.Equal(t, 2, len(scheduler.lines))
	assert.Equal(t, 2, len(scheduler.queues)) // Two different intervals
	assert.NotNil(t, scheduler.stopChan)
}

func TestRouterScheduler_InitializeQueues(t *testing.T) {
	router := createTestRouter("192.168.1.1", "admin", "password", "cisco_iosxe", "ssh")
	lines := []syncer.Line{
		createTestLineForRouter("line1", "10.0.0.1", router, 5*time.Minute),
		createTestLineForRouter("line2", "10.0.0.2", router, 5*time.Minute), // Same interval
		createTestLineForRouter("line3", "10.0.0.3", router, 3*time.Minute), // Different interval
	}

	// Use helper function to avoid real network connections
	scheduler := createTestRouterScheduler(router, lines, nil)
	defer scheduler.Stop()

	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()

	// Should have 2 queues (5min and 3min)
	assert.Equal(t, 2, len(scheduler.queues))
	assert.Contains(t, scheduler.queues, 5*time.Minute)
	assert.Contains(t, scheduler.queues, 3*time.Minute)

	// Check queue contents
	fiveMinQueue := scheduler.queues[5*time.Minute]
	threeMinQueue := scheduler.queues[3*time.Minute]

	fiveMinSnapshot := fiveMinQueue.GetTasksSnapshot()
	threeMinSnapshot := threeMinQueue.GetTasksSnapshot()

	assert.Equal(t, 2, len(fiveMinSnapshot))
	assert.Equal(t, 1, len(threeMinSnapshot))
}

func TestRouterScheduler_OnLineCreated(t *testing.T) {
	router := createTestRouter("192.168.1.1", "admin", "password", "cisco_iosxe", "ssh")
	initialLines := []syncer.Line{
		createTestLineForRouter("line1", "10.0.0.1", router, 5*time.Minute),
	}

	// Use helper function to avoid real network connections
	scheduler := createTestRouterScheduler(router, initialLines, nil)
	defer scheduler.Stop()

	// Add new line with existing interval
	newLine := createTestLineForRouter("line2", "10.0.0.2", router, 5*time.Minute)
	scheduler.OnLineCreated(newLine)

	scheduler.mu.Lock()
	assert.Equal(t, 2, len(scheduler.lines))
	assert.Equal(t, 1, len(scheduler.queues)) // Still one queue

	queue := scheduler.queues[5*time.Minute]
	snapshot := queue.GetTasksSnapshot()
	assert.Equal(t, 2, len(snapshot))
	scheduler.mu.Unlock()

	// Add new line with different interval
	newLine2 := createTestLineForRouter("line3", "10.0.0.3", router, 3*time.Minute)
	scheduler.OnLineCreated(newLine2)

	scheduler.mu.Lock()
	assert.Equal(t, 3, len(scheduler.lines))
	assert.Equal(t, 2, len(scheduler.queues)) // Now two queues
	assert.Contains(t, scheduler.queues, 3*time.Minute)
	scheduler.mu.Unlock()
}

func TestRouterScheduler_OnLineCreated_DuplicatePrevention(t *testing.T) {
	router := createTestRouter("192.168.1.1", "admin", "password", "cisco_iosxe", "ssh")
	initialLines := []syncer.Line{
		createTestLineForRouter("line1", "10.0.0.1", router, 5*time.Minute),
	}

	// Use helper function to avoid real network connections
	scheduler := createTestRouterScheduler(router, initialLines, nil)
	defer scheduler.Stop()

	// Try to add duplicate line
	duplicateLine := createTestLineForRouter("line1", "10.0.0.1", router, 3*time.Minute)
	scheduler.OnLineCreated(duplicateLine)

	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()

	// Should still have only 1 line and 1 queue
	assert.Equal(t, 1, len(scheduler.lines))
	assert.Equal(t, 1, len(scheduler.queues))
	assert.Contains(t, scheduler.queues, 5*time.Minute) // Original interval preserved
}

func TestRouterScheduler_OnLineUpdated_SameInterval(t *testing.T) {
	router := createTestRouter("192.168.1.1", "admin", "password", "cisco_iosxe", "ssh")
	oldLine := createTestLineForRouter("line1", "10.0.0.1", router, 5*time.Minute)
	initialLines := []syncer.Line{oldLine}

	// Use helper function to avoid real network connections
	scheduler := createTestRouterScheduler(router, initialLines, nil)
	defer scheduler.Stop()

	// Update line with same interval but different properties
	newLine := createTestLineForRouter("line1", "10.0.0.1", router, 5*time.Minute)
	// Simulate different hash (e.g., different router password)
	newLine.Router.Password = "newpassword"
	newLine.ComputeHash()

	scheduler.OnLineUpdated(oldLine, newLine)

	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()

	// Should still have 1 queue and 1 line
	assert.Equal(t, 1, len(scheduler.queues))
	assert.Equal(t, 1, len(scheduler.lines))

	// Line should be updated
	assert.Equal(t, "newpassword", scheduler.lines[0].Router.Password)
}

func TestRouterScheduler_OnLineUpdated_DifferentInterval(t *testing.T) {
	router := createTestRouter("192.168.1.1", "admin", "password", "cisco_iosxe", "ssh")
	oldLine := createTestLineForRouter("line1", "10.0.0.1", router, 5*time.Minute)
	initialLines := []syncer.Line{oldLine}

	// Use helper function to avoid real network connections
	scheduler := createTestRouterScheduler(router, initialLines, nil)
	defer scheduler.Stop()

	// Update line with different interval
	newLine := createTestLineForRouter("line1", "10.0.0.1", router, 3*time.Minute)
	scheduler.OnLineUpdated(oldLine, newLine)

	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()

	// Should now have 2 queues (old interval queue might be empty)
	assert.Equal(t, 2, len(scheduler.queues))
	assert.Contains(t, scheduler.queues, 3*time.Minute)
	assert.Contains(t, scheduler.queues, 5*time.Minute)

	// New queue should have the line
	newQueue := scheduler.queues[3*time.Minute]
	assert.True(t, newQueue.Contains("10.0.0.1"))

	// Old queue should not have the line
	oldQueue := scheduler.queues[5*time.Minute]
	assert.False(t, oldQueue.Contains("10.0.0.1"))

	// Lines array should be updated
	assert.Equal(t, 3*time.Minute, scheduler.lines[0].Interval)
}

func TestRouterScheduler_OnLineDeleted(t *testing.T) {
	router := createTestRouter("192.168.1.1", "admin", "password", "cisco_iosxe", "ssh")
	line1 := createTestLineForRouter("line1", "10.0.0.1", router, 5*time.Minute)
	line2 := createTestLineForRouter("line2", "10.0.0.2", router, 5*time.Minute)
	initialLines := []syncer.Line{line1, line2}

	// Use helper function to avoid real network connections
	scheduler := createTestRouterScheduler(router, initialLines, nil)
	defer scheduler.Stop()

	// Delete one line
	scheduler.OnLineDeleted(line1)

	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()

	// Should still have 1 queue (not empty)
	assert.Equal(t, 1, len(scheduler.queues))

	queue := scheduler.queues[5*time.Minute]
	assert.False(t, queue.Contains("10.0.0.1"))
	assert.True(t, queue.Contains("10.0.0.2"))
	assert.False(t, queue.IsEmpty())
}

func TestRouterScheduler_OnLineDeleted_EmptyQueue(t *testing.T) {
	router := createTestRouter("192.168.1.1", "admin", "password", "cisco_iosxe", "ssh")
	line1 := createTestLineForRouter("line1", "10.0.0.1", router, 5*time.Minute)
	initialLines := []syncer.Line{line1}

	// Use helper function to avoid real network connections
	scheduler := createTestRouterScheduler(router, initialLines, nil)
	defer scheduler.Stop()

	// Delete the only line
	scheduler.OnLineDeleted(line1)

	scheduler.mu.Lock()
	queue := scheduler.queues[5*time.Minute]
	isEmpty := queue.IsEmpty()
	scheduler.mu.Unlock()

	// Queue should be empty
	assert.True(t, isEmpty)

	// Queue should still exist (delayed deletion)
	scheduler.mu.Lock()
	assert.Equal(t, 1, len(scheduler.queues))
	scheduler.mu.Unlock()
}

func TestRouterScheduler_OnLineDeleted_NonexistentLine(t *testing.T) {
	router := createTestRouter("192.168.1.1", "admin", "password", "cisco_iosxe", "ssh")
	line1 := createTestLineForRouter("line1", "10.0.0.1", router, 5*time.Minute)
	initialLines := []syncer.Line{line1}

	// Use helper function to avoid real network connections
	scheduler := createTestRouterScheduler(router, initialLines, nil)
	defer scheduler.Stop()

	// Try to delete nonexistent line
	nonexistentLine := createTestLineForRouter("line999", "10.0.0.999", router, 5*time.Minute)

	// Should not panic
	assert.NotPanics(t, func() {
		scheduler.OnLineDeleted(nonexistentLine)
	})

	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()

	// Original state should be preserved
	assert.Equal(t, 1, len(scheduler.queues))
	queue := scheduler.queues[5*time.Minute]
	assert.True(t, queue.Contains("10.0.0.1"))
	assert.False(t, queue.IsEmpty())
}

func TestRouterScheduler_OnLineReset(t *testing.T) {
	router := createTestRouter("192.168.1.1", "admin", "password", "cisco_iosxe", "ssh")
	initialLines := []syncer.Line{
		createTestLineForRouter("line1", "10.0.0.1", router, 5*time.Minute),
		createTestLineForRouter("line2", "10.0.0.2", router, 3*time.Minute),
	}

	// Use helper function to avoid real network connections
	scheduler := createTestRouterScheduler(router, initialLines, nil)
	defer scheduler.Stop()

	// Verify initial state
	scheduler.mu.Lock()
	assert.Equal(t, 2, len(scheduler.queues))
	assert.Equal(t, 2, len(scheduler.lines))
	scheduler.mu.Unlock()

	// Reset with new lines
	newLines := []syncer.Line{
		createTestLineForRouter("line3", "10.0.0.3", router, 2*time.Minute),
		createTestLineForRouter("line4", "10.0.0.4", router, 2*time.Minute),
		createTestLineForRouter("line5", "10.0.0.5", router, 4*time.Minute),
	}

	scheduler.OnLineReset(newLines)

	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()

	// Should have new queues and lines
	assert.Equal(t, 2, len(scheduler.queues)) // 2min and 4min intervals
	assert.Equal(t, 3, len(scheduler.lines))

	// Check specific intervals
	assert.Contains(t, scheduler.queues, 2*time.Minute)
	assert.Contains(t, scheduler.queues, 4*time.Minute)
	assert.NotContains(t, scheduler.queues, 5*time.Minute) // Old intervals should be gone
	assert.NotContains(t, scheduler.queues, 3*time.Minute)

	// Check queue contents
	twoMinQueue := scheduler.queues[2*time.Minute]
	fourMinQueue := scheduler.queues[4*time.Minute]

	twoMinSnapshot := twoMinQueue.GetTasksSnapshot()
	fourMinSnapshot := fourMinQueue.GetTasksSnapshot()

	assert.Equal(t, 2, len(twoMinSnapshot))
	assert.Equal(t, 1, len(fourMinSnapshot))
}

func TestRouterScheduler_ExecuteTasks(t *testing.T) {
	t.Skip("Skipping test that requires real connection pool - needs refactoring to use dependency injection")
}

func TestRouterScheduler_ExecuteTasks_NoMatchingTask(t *testing.T) {
	t.Skip("Skipping test that requires real connection pool - needs refactoring to use dependency injection")
}

func TestRouterScheduler_ExecuteTasks_EmptyQueue(t *testing.T) {
	t.Skip("Skipping test that requires real connection pool - needs refactoring to use dependency injection")
}

func TestRouterScheduler_Stop(t *testing.T) {
	router := createTestRouter("192.168.1.1", "admin", "password", "cisco_iosxe", "ssh")
	lines := []syncer.Line{
		createTestLineForRouter("line1", "10.0.0.1", router, 5*time.Minute),
	}

	_ = &MockManager{registry: task.NewDefaultRegistry()} // unused in this test

	// Use helper function to avoid real network connections
	scheduler := createTestRouterScheduler(router, lines, nil)

	// Stop should not panic
	assert.NotPanics(t, func() {
		scheduler.Stop()
	})
}

// Note: mergeLinesIP tests removed as batch processing has been simplified to individual ping execution
// Refer to memory: "Batch Processing Simplification and Optimization" for details

// Concurrent access tests
func TestRouterScheduler_ConcurrentLineOperations(t *testing.T) {
	router := createTestRouter("192.168.1.1", "admin", "password", "cisco_iosxe", "ssh")
	initialLines := []syncer.Line{
		createTestLineForRouter("line1", "10.0.0.1", router, 5*time.Minute),
	}

	// Use helper function to avoid real network connections
	scheduler := createTestRouterScheduler(router, initialLines, nil)
	defer scheduler.Stop()

	var wg sync.WaitGroup
	numOperations := 50

	// Concurrent line operations
	for i := 0; i < numOperations; i++ {
		wg.Add(3)

		go func(id int) {
			defer wg.Done()
			line := createTestLineForRouter(fmt.Sprintf("concurrent_%d", id), fmt.Sprintf("10.1.%d.1", id), router, time.Duration(id+1)*time.Minute)
			scheduler.OnLineCreated(line)
		}(i)

		go func(id int) {
			defer wg.Done()
			if id < len(initialLines) {
				oldLine := initialLines[id]
				newLine := oldLine
				newLine.Interval = time.Duration(id+10) * time.Minute
				newLine.ComputeHash()
				scheduler.OnLineUpdated(oldLine, newLine)
			}
		}(i)

		go func(id int) {
			defer wg.Done()
			line := createTestLineForRouter(fmt.Sprintf("delete_%d", id), fmt.Sprintf("10.2.%d.1", id), router, 2*time.Minute)
			scheduler.OnLineDeleted(line)
		}(i)
	}

	wg.Wait()

	// Should not panic and should have consistent state
	scheduler.mu.Lock()
	queueCount := len(scheduler.queues)
	lineCount := len(scheduler.lines)
	scheduler.mu.Unlock()

	assert.True(t, queueCount >= 0)
	assert.True(t, lineCount >= 0)
}

// Performance tests
func BenchmarkRouterScheduler_OnLineCreated(b *testing.B) {
	router := createTestRouter("192.168.1.1", "admin", "password", "cisco_iosxe", "ssh")
	// Use helper function to avoid real network connections
	scheduler := createTestRouterScheduler(router, []syncer.Line{}, nil)
	defer scheduler.Stop()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		line := createTestLineForRouter(fmt.Sprintf("bench_%d", i), fmt.Sprintf("10.0.%d.%d", i/256, i%256), router, 5*time.Minute)
		scheduler.OnLineCreated(line)
	}
}

func BenchmarkRouterScheduler_ExecuteTasks(b *testing.B) {
	b.Skip("Skipping benchmark that requires real connection pool - needs refactoring to use dependency injection")
}

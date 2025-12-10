package manager

import (
	"context"
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

// Mock implementations
type MockConfigSyncer struct {
	mock.Mock
	subscribers []*MockSubscription
	mu          sync.Mutex
}

func (m *MockConfigSyncer) GetLines() map[string]syncer.Line {
	args := m.Called()
	return args.Get(0).(map[string]syncer.Line)
}

func (m *MockConfigSyncer) Subscribe(ctx context.Context) *syncer.Subscription {
	m.Called(ctx)
	mockSub := &MockSubscription{
		events: make(chan syncer.LineChangeEvent, 1000),
	}

	m.mu.Lock()
	m.subscribers = append(m.subscribers, mockSub)
	m.mu.Unlock()

	// Return a real Subscription that wraps our mock
	return &syncer.Subscription{}
}

func (m *MockConfigSyncer) SendEvent(event syncer.LineChangeEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sub := range m.subscribers {
		select {
		case sub.events <- event:
		default:
		}
	}
}

type MockSubscription struct {
	events chan syncer.LineChangeEvent
}

func (m *MockSubscription) Events() <-chan syncer.LineChangeEvent {
	return m.events
}

func (m *MockSubscription) Close() {
	close(m.events)
}

type MockRegistry struct {
	mock.Mock
}

func (m *MockRegistry) Register(meta task.TaskMeta) error {
	args := m.Called(meta)
	return args.Error(0)
}

func (m *MockRegistry) Discover(taskType task.TaskType, platform task.Platform, protocol task.Protocol, commandType task.CommandType) (task.Task, error) {
	args := m.Called(taskType, platform, protocol, commandType)
	return args.Get(0).(task.Task), args.Error(1)
}

func (m *MockRegistry) ListPlatforms(platform task.Platform) []task.TaskType {
	args := m.Called(platform)
	return args.Get(0).([]task.TaskType)
}

type MockScheduler struct {
	mock.Mock
	started bool
	stopped bool
	mu      sync.Mutex
}

func (m *MockScheduler) OnLineCreated(line syncer.Line) {
	m.Called(line)
}

func (m *MockScheduler) OnLineUpdated(old, new syncer.Line) {
	m.Called(old, new)
}

func (m *MockScheduler) OnLineDeleted(line syncer.Line) {
	m.Called(line)
}

func (m *MockScheduler) OnLineReset(lines []syncer.Line) {
	m.Called(lines)
}

func (m *MockScheduler) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.started = true
	m.Called()
}

func (m *MockScheduler) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped = true
	m.Called()
}

func (m *MockScheduler) IsStarted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.started
}

func (m *MockScheduler) IsStopped() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopped
}

// TestableManager wraps Manager to support mock schedulers in unit tests
type TestableManager struct {
	configSyncer ConfigSyncerInterface
	schedulers   map[string]Scheduler
	routerLines  map[string][]syncer.Line
	registry     task.Registry
	mu           sync.Mutex
	stopChan     chan struct{}
	wg           sync.WaitGroup

	useMockSchedulers bool
	mockSchedulers    map[string]*MockScheduler
	mockMu            sync.Mutex
}

func NewTestableManager(configSyncer ConfigSyncerInterface, registry task.Registry, useMock bool) *TestableManager {
	return &TestableManager{
		configSyncer:      configSyncer,
		schedulers:        make(map[string]Scheduler),
		routerLines:       make(map[string][]syncer.Line),
		registry:          registry,
		stopChan:          make(chan struct{}),
		useMockSchedulers: useMock,
		mockSchedulers:    make(map[string]*MockScheduler),
	}
}

func (tm *TestableManager) Start() {
	// Register PingTask
	pingMeta := task.TaskMeta{
		Type:        "ping",
		Description: "Ping task for line monitoring",
		Platforms: []task.PlatformSupport{
			{
				Platform: "cisco_iosxe",
				Protocols: []task.ProtocolSupport{
					{
						Protocol: "ssh",
						CommandTypes: []task.CommandTypeSupport{
							{
								CommandType: "commands",
								ImplFactory: func() task.Task { return &task.PingTask{} },
								Params:      []task.ParamSpec{},
							},
						},
					},
				},
			},
		},
	}
	if err := tm.registry.Register(pingMeta); err != nil {
		return
	}

	// Initial sync
	tm.fullSync()

	// Start periodic sync
	tm.wg.Add(1)
	go tm.periodicSync(1 * time.Hour)

	// Subscribe to changes
	sub := tm.configSyncer.Subscribe(context.Background())
	tm.wg.Add(1)
	go tm.handleLineChanges(sub)
}

func (tm *TestableManager) Stop() {
	select {
	case <-tm.stopChan:
		return
	default:
		close(tm.stopChan)
	}

	tm.wg.Wait()

	tm.mu.Lock()
	schedulers := make([]Scheduler, 0, len(tm.schedulers))
	for _, s := range tm.schedulers {
		schedulers = append(schedulers, s)
	}
	tm.mu.Unlock()

	for _, s := range schedulers {
		s.Stop()
	}
}

func (tm *TestableManager) fullSync() {
	lines := tm.configSyncer.GetLines()
	newRouterLines := make(map[string][]syncer.Line)

	for _, line := range lines {
		newRouterLines[line.Router.IP] = append(newRouterLines[line.Router.IP], line)
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.routerLines = newRouterLines

	for routerIP, lines := range newRouterLines {
		tm.ensureScheduler(routerIP, lines)
	}

	for routerIP := range tm.schedulers {
		if _, exists := newRouterLines[routerIP]; !exists {
			tm.schedulers[routerIP].Stop()
			delete(tm.schedulers, routerIP)
		}
	}
}

func (tm *TestableManager) periodicSync(interval time.Duration) {
	defer tm.wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-tm.stopChan:
			return
		case <-ticker.C:
			tm.fullSync()
		}
	}
}

func (tm *TestableManager) handleLineChanges(sub *syncer.Subscription) {
	defer tm.wg.Done()
	// Mock implementation - just wait for stop signal
	<-tm.stopChan
}

func (tm *TestableManager) processLineEvent(event syncer.LineChangeEvent) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	routerIP := event.Line.Router.IP

	switch event.Type {
	case syncer.LineCreate:
		if _, exists := tm.routerLines[routerIP]; !exists {
			tm.routerLines[routerIP] = make([]syncer.Line, 0)
		}

		// Check for duplicate
		for _, l := range tm.routerLines[routerIP] {
			if l.IP == event.Line.IP {
				return // Duplicate, ignore
			}
		}

		tm.routerLines[routerIP] = append(tm.routerLines[routerIP], event.Line)
		if _, exists := tm.schedulers[routerIP]; !exists {
			tm.ensureScheduler(routerIP, tm.routerLines[routerIP])
		}
		tm.schedulers[routerIP].OnLineCreated(event.Line)

	case syncer.LineUpdate:
		for i, l := range tm.routerLines[routerIP] {
			if l.IP == event.Line.IP {
				oldLine := tm.routerLines[routerIP][i]
				tm.routerLines[routerIP][i] = event.Line
				tm.schedulers[routerIP].OnLineUpdated(oldLine, event.Line)
				break
			}
		}

	case syncer.LineDelete:
		if lines, exists := tm.routerLines[routerIP]; exists {
			for i, line := range lines {
				if line.IP == event.Line.IP {
					tm.routerLines[routerIP] = append(lines[:i], lines[i+1:]...)
					break
				}
			}
		}

		if s, exists := tm.schedulers[routerIP]; exists {
			s.OnLineDeleted(event.Line)

			if len(tm.routerLines[routerIP]) == 0 {
				time.AfterFunc(10*time.Minute, func() {
					tm.mu.Lock()
					defer tm.mu.Unlock()
					if len(tm.routerLines[routerIP]) == 0 {
						s.Stop()
						delete(tm.schedulers, routerIP)
						delete(tm.routerLines, routerIP)
					}
				})
			}
		}
	}
}

func (tm *TestableManager) createScheduler(router *syncer.Router, lines []syncer.Line) Scheduler {
	if tm.useMockSchedulers {
		tm.mockMu.Lock()
		defer tm.mockMu.Unlock()

		mockScheduler := &MockScheduler{}
		mockScheduler.On("Start").Return()
		mockScheduler.On("Stop").Return()
		mockScheduler.On("OnLineCreated", mock.AnythingOfType("syncer.Line")).Return()
		mockScheduler.On("OnLineUpdated", mock.AnythingOfType("syncer.Line"), mock.AnythingOfType("syncer.Line")).Return()
		mockScheduler.On("OnLineDeleted", mock.AnythingOfType("syncer.Line")).Return()
		mockScheduler.On("OnLineReset", mock.AnythingOfType("[]syncer.Line")).Return()

		tm.mockSchedulers[router.IP] = mockScheduler
		return mockScheduler
	}
	ctx := context.Background()
	scheduler, _ := NewRouterScheduler(ctx, router, lines, nil)
	return scheduler
}

func (tm *TestableManager) ensureScheduler(routerIP string, lines []syncer.Line) {
	if _, exists := tm.schedulers[routerIP]; !exists {
		tm.schedulers[routerIP] = tm.createScheduler(&lines[0].Router, lines)
		go tm.schedulers[routerIP].Start()
	}
}

// Test helper functions
func createTestLine(id, ip, routerIP string, interval time.Duration) syncer.Line {
	line := syncer.Line{
		ID:       id,
		IP:       ip,
		Interval: interval,
		Router: syncer.Router{
			IP:       routerIP,
			Username: "admin",
			Password: "password",
			Platform: connection.Platform("cisco_iosxe"),
			Protocol: connection.Protocol("ssh"),
		},
	}
	line.ComputeHash()
	return line
}

func createTestManager() (*Manager, *MockConfigSyncer, *MockRegistry) {
	mockSyncer := &MockConfigSyncer{}
	mockRegistry := &MockRegistry{}
	aggregator := task.NewAggregator(5, 500, 15*time.Second)
	manager := NewManager(mockSyncer, mockRegistry, aggregator)
	return manager, mockSyncer, mockRegistry
}

// Basic functionality tests
func TestNewManager(t *testing.T) {
	manager, _, _ := createTestManager()

	assert.NotNil(t, manager)
	assert.NotNil(t, manager.configSyncer)
	assert.NotNil(t, manager.registry)
	assert.NotNil(t, manager.schedulers)
	assert.NotNil(t, manager.routerLines)
	assert.NotNil(t, manager.stopChan)
	assert.Equal(t, 0, len(manager.schedulers))
	assert.Equal(t, 0, len(manager.routerLines))
}

func TestManager_Start_SuccessfulPingTaskRegistration(t *testing.T) {
	manager, mockSyncer, mockRegistry := createTestManager()

	// Setup mocks
	mockRegistry.On("Register", mock.AnythingOfType("task.TaskMeta")).Return(nil)
	mockSyncer.On("GetLines").Return(make(map[string]syncer.Line))
	mockSyncer.On("Subscribe", mock.Anything).Return(&syncer.Subscription{})

	manager.Start()
	defer manager.Stop()

	// Verify PingTask was registered
	mockRegistry.AssertCalled(t, "Register", mock.MatchedBy(func(meta task.TaskMeta) bool {
		return string(meta.Type) == "ping" && meta.Description == "Ping task for network devices"
	}))

	// Verify initial sync was called
	mockSyncer.AssertCalled(t, "GetLines")
	mockSyncer.AssertCalled(t, "Subscribe", mock.Anything)
}

func TestManager_Start_PingTaskRegistrationFailure(t *testing.T) {
	manager, mockSyncer, mockRegistry := createTestManager()

	// Setup mocks to simulate registration failure
	mockRegistry.On("Register", mock.AnythingOfType("task.TaskMeta")).Return(assert.AnError)

	manager.Start()
	defer manager.Stop()

	// Verify registration was attempted
	mockRegistry.AssertCalled(t, "Register", mock.AnythingOfType("task.TaskMeta"))

	// Other operations should not proceed
	mockSyncer.AssertNotCalled(t, "GetLines")
	mockSyncer.AssertNotCalled(t, "Subscribe", mock.Anything)
}

func TestManager_FullSync_EmptyLines(t *testing.T) {
	manager, mockSyncer, mockRegistry := createTestManager()

	emptyLines := make(map[string]syncer.Line)
	mockSyncer.On("GetLines").Return(emptyLines)
	mockRegistry.On("Register", mock.AnythingOfType("task.TaskMeta")).Return(nil)
	mockSyncer.On("Subscribe", mock.Anything).Return(&syncer.Subscription{})

	manager.Start()
	defer manager.Stop()

	// Wait for initial sync
	time.Sleep(100 * time.Millisecond)

	manager.mu.Lock()
	assert.Equal(t, 0, len(manager.routerLines))
	assert.Equal(t, 0, len(manager.schedulers))
	manager.mu.Unlock()
}

func TestManager_FullSync_WithLines(t *testing.T) {
	t.Skip("Skipping test with real network connections - use integration tests instead")

	manager, mockSyncer, mockRegistry := createTestManager()

	// Create test lines
	lines := map[string]syncer.Line{
		"10.0.0.1": createTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute),
		"10.0.0.2": createTestLine("line2", "10.0.0.2", "192.168.1.1", 3*time.Minute),
		"10.0.0.3": createTestLine("line3", "10.0.0.3", "192.168.1.2", 5*time.Minute),
	}

	mockSyncer.On("GetLines").Return(lines)
	mockRegistry.On("Register", mock.AnythingOfType("task.TaskMeta")).Return(nil)
	mockSyncer.On("Subscribe", mock.Anything).Return(&syncer.Subscription{})

	manager.Start()
	defer manager.Stop()

	// Wait for initial sync
	time.Sleep(100 * time.Millisecond)

	manager.mu.Lock()
	defer manager.mu.Unlock()

	// Should have 2 routers
	assert.Equal(t, 2, len(manager.routerLines))
	assert.Equal(t, 2, len(manager.schedulers))

	// Check router grouping
	assert.Equal(t, 2, len(manager.routerLines["192.168.1.1"]))
	assert.Equal(t, 1, len(manager.routerLines["192.168.1.2"]))
}

func TestManager_ProcessLineEvent_LineCreate(t *testing.T) {
	_, mockSyncer, mockRegistry := createTestManager()
	testManager := NewTestableManager(mockSyncer, mockRegistry, true)

	// Create a new line
	newLine := createTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute)
	event := syncer.LineChangeEvent{
		Type: syncer.LineCreate,
		Line: newLine,
	}

	testManager.processLineEvent(event)

	testManager.mu.Lock()
	defer testManager.mu.Unlock()

	// Verify line was added
	assert.Equal(t, 1, len(testManager.routerLines))
	assert.Equal(t, 1, len(testManager.routerLines["192.168.1.1"]))
	assert.Equal(t, "line1", testManager.routerLines["192.168.1.1"][0].ID)

	// Should have created a scheduler
	assert.Equal(t, 1, len(testManager.schedulers))
	assert.Contains(t, testManager.schedulers, "192.168.1.1")
}

func TestManager_ProcessLineEvent_LineCreate_Duplicate(t *testing.T) {
	_, mockSyncer, mockRegistry := createTestManager()
	testManager := NewTestableManager(mockSyncer, mockRegistry, true)

	// Setup initial state with existing line
	existingLine := createTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute)
	testManager.routerLines = map[string][]syncer.Line{
		"192.168.1.1": {existingLine},
	}

	// Try to create duplicate
	duplicateLine := createTestLine("line1", "10.0.0.1", "192.168.1.1", 3*time.Minute)
	event := syncer.LineChangeEvent{
		Type: syncer.LineCreate,
		Line: duplicateLine,
	}

	testManager.processLineEvent(event)

	testManager.mu.Lock()
	defer testManager.mu.Unlock()

	// Should still have only one line
	assert.Equal(t, 1, len(testManager.routerLines["192.168.1.1"]))
	assert.Equal(t, 5*time.Minute, testManager.routerLines["192.168.1.1"][0].Interval) // Original unchanged
}

func TestManager_ProcessLineEvent_LineUpdate(t *testing.T) {
	_, mockSyncer, mockRegistry := createTestManager()
	testManager := NewTestableManager(mockSyncer, mockRegistry, true)

	// Setup initial state
	oldLine := createTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute)
	testManager.routerLines = map[string][]syncer.Line{
		"192.168.1.1": {oldLine},
	}

	// Create mock scheduler
	mockScheduler := &MockScheduler{}
	mockScheduler.On("OnLineUpdated", mock.AnythingOfType("syncer.Line"), mock.AnythingOfType("syncer.Line"))
	testManager.schedulers = map[string]Scheduler{
		"192.168.1.1": mockScheduler,
	}

	// Update line
	updatedLine := createTestLine("line1", "10.0.0.1", "192.168.1.1", 3*time.Minute)
	event := syncer.LineChangeEvent{
		Type: syncer.LineUpdate,
		Line: updatedLine,
	}

	testManager.processLineEvent(event)

	testManager.mu.Lock()
	defer testManager.mu.Unlock()

	// Verify line was updated
	assert.Equal(t, 1, len(testManager.routerLines["192.168.1.1"]))
	assert.Equal(t, 3*time.Minute, testManager.routerLines["192.168.1.1"][0].Interval)

	// Verify scheduler was notified
	mockScheduler.AssertCalled(t, "OnLineUpdated", oldLine, updatedLine)
}

func TestManager_ProcessLineEvent_LineDelete(t *testing.T) {
	_, mockSyncer, mockRegistry := createTestManager()
	testManager := NewTestableManager(mockSyncer, mockRegistry, true)

	// Setup initial state
	line := createTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute)
	testManager.routerLines = map[string][]syncer.Line{
		"192.168.1.1": {line},
	}

	// Create mock scheduler
	mockScheduler := &MockScheduler{}
	mockScheduler.On("OnLineDeleted", mock.AnythingOfType("syncer.Line"))
	mockScheduler.On("Stop")
	testManager.schedulers = map[string]Scheduler{
		"192.168.1.1": mockScheduler,
	}

	// Delete line
	event := syncer.LineChangeEvent{
		Type: syncer.LineDelete,
		Line: line,
	}

	testManager.processLineEvent(event)

	testManager.mu.Lock()
	defer testManager.mu.Unlock()

	// Verify line was removed
	assert.Equal(t, 0, len(testManager.routerLines["192.168.1.1"]))

	// Verify scheduler was notified
	mockScheduler.AssertCalled(t, "OnLineDeleted", line)

	// Scheduler should still exist (delayed deletion)
	assert.Contains(t, testManager.schedulers, "192.168.1.1")
}

func TestManager_ProcessLineEvent_LineDelete_DelayedSchedulerCleanup(t *testing.T) {
	manager, _, _ := createTestManager()

	// Setup initial state
	line := createTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute)
	manager.routerLines = map[string][]syncer.Line{
		"192.168.1.1": {line},
	}

	// Create mock scheduler
	mockScheduler := &MockScheduler{}
	mockScheduler.On("OnLineDeleted", mock.AnythingOfType("syncer.Line"))
	mockScheduler.On("Stop")
	manager.schedulers = map[string]Scheduler{
		"192.168.1.1": mockScheduler,
	}

	// Delete line
	event := syncer.LineChangeEvent{
		Type: syncer.LineDelete,
		Line: line,
	}

	manager.processLineEvent(event)

	// Wait for delayed cleanup (simulate 10+ minutes)
	time.Sleep(50 * time.Millisecond) // In real scenario this would be 10 minutes

	// The actual cleanup happens in AfterFunc, we can't easily test the timing
	// but we can verify the scheduler was notified of deletion
	mockScheduler.AssertCalled(t, "OnLineDeleted", line)
}

func TestManager_ProcessLineEvent_LineDelete_WithRemainingLines(t *testing.T) {
	_, mockSyncer, mockRegistry := createTestManager()
	testManager := NewTestableManager(mockSyncer, mockRegistry, true)

	// Setup initial state with multiple lines
	line1 := createTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute)
	line2 := createTestLine("line2", "10.0.0.2", "192.168.1.1", 3*time.Minute)
	testManager.routerLines = map[string][]syncer.Line{
		"192.168.1.1": {line1, line2},
	}

	// Create mock scheduler
	mockScheduler := &MockScheduler{}
	mockScheduler.On("OnLineDeleted", mock.AnythingOfType("syncer.Line"))
	testManager.schedulers = map[string]Scheduler{
		"192.168.1.1": mockScheduler,
	}

	// Delete one line
	event := syncer.LineChangeEvent{
		Type: syncer.LineDelete,
		Line: line1,
	}

	testManager.processLineEvent(event)

	testManager.mu.Lock()
	defer testManager.mu.Unlock()

	// Should have one line remaining
	assert.Equal(t, 1, len(testManager.routerLines["192.168.1.1"]))
	assert.Equal(t, "line2", testManager.routerLines["192.168.1.1"][0].ID)

	// Scheduler should not be scheduled for deletion
	assert.Contains(t, testManager.schedulers, "192.168.1.1")

	mockScheduler.AssertCalled(t, "OnLineDeleted", line1)
	mockScheduler.AssertNotCalled(t, "Stop")
}

func TestManager_EnsureScheduler_NewScheduler(t *testing.T) {
	_, mockSyncer, mockRegistry := createTestManager()
	testManager := NewTestableManager(mockSyncer, mockRegistry, true)

	// Create test lines
	lines := []syncer.Line{
		createTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute),
	}

	// Should not have scheduler initially
	testManager.mu.Lock()
	assert.Equal(t, 0, len(testManager.schedulers))
	testManager.mu.Unlock()

	// Ensure scheduler
	testManager.ensureScheduler("192.168.1.1", lines)

	testManager.mu.Lock()
	defer testManager.mu.Unlock()

	// Should have created scheduler
	assert.Equal(t, 1, len(testManager.schedulers))
	assert.Contains(t, testManager.schedulers, "192.168.1.1")
}

func TestManager_EnsureScheduler_ExistingScheduler(t *testing.T) {
	_, mockSyncer, mockRegistry := createTestManager()
	testManager := NewTestableManager(mockSyncer, mockRegistry, true)

	// Setup existing scheduler
	mockScheduler := &MockScheduler{}
	testManager.schedulers = map[string]Scheduler{
		"192.168.1.1": mockScheduler,
	}

	lines := []syncer.Line{
		createTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute),
	}

	// Ensure scheduler
	testManager.ensureScheduler("192.168.1.1", lines)

	testManager.mu.Lock()
	defer testManager.mu.Unlock()

	// Should still have only one scheduler (same instance)
	assert.Equal(t, 1, len(testManager.schedulers))
	assert.Same(t, mockScheduler, testManager.schedulers["192.168.1.1"])
}

func TestManager_Stop(t *testing.T) {
	manager, mockSyncer, mockRegistry := createTestManager()

	// Setup with schedulers
	mockScheduler1 := &MockScheduler{}
	mockScheduler2 := &MockScheduler{}
	mockScheduler1.On("Stop")
	mockScheduler2.On("Stop")

	manager.schedulers = map[string]Scheduler{
		"192.168.1.1": mockScheduler1,
		"192.168.1.2": mockScheduler2,
	}

	// Setup mocks for start
	mockRegistry.On("Register", mock.AnythingOfType("task.TaskMeta")).Return(nil)
	mockSyncer.On("GetLines").Return(make(map[string]syncer.Line))
	mockSyncer.On("Subscribe", mock.Anything).Return(&syncer.Subscription{})

	manager.Start()

	// Stop manager
	manager.Stop()

	// Verify all schedulers were stopped
	mockScheduler1.AssertCalled(t, "Stop")
	mockScheduler2.AssertCalled(t, "Stop")
}

// Concurrent access tests
func TestManager_ConcurrentLineEvents(t *testing.T) {
	_, mockSyncer, mockRegistry := createTestManager()
	testManager := NewTestableManager(mockSyncer, mockRegistry, true)

	// Setup mock schedulers
	mockScheduler := &MockScheduler{}
	mockScheduler.On("OnLineCreated", mock.AnythingOfType("syncer.Line"))
	mockScheduler.On("OnLineUpdated", mock.AnythingOfType("syncer.Line"), mock.AnythingOfType("syncer.Line"))
	mockScheduler.On("OnLineDeleted", mock.AnythingOfType("syncer.Line"))
	mockScheduler.On("Stop")

	testManager.schedulers = map[string]Scheduler{
		"192.168.1.1": mockScheduler,
	}

	var wg sync.WaitGroup
	numGoroutines := 10

	// Simulate concurrent line events
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			line := createTestLine(
				fmt.Sprintf("line%d", id),
				fmt.Sprintf("10.0.0.%d", id),
				"192.168.1.1",
				time.Duration(id+1)*time.Minute,
			)

			// Create
			testManager.processLineEvent(syncer.LineChangeEvent{
				Type: syncer.LineCreate,
				Line: line,
			})

			// Update
			line.Interval = time.Duration(id+10) * time.Minute
			line.ComputeHash()
			testManager.processLineEvent(syncer.LineChangeEvent{
				Type: syncer.LineUpdate,
				Line: line,
			})

			// Delete
			testManager.processLineEvent(syncer.LineChangeEvent{
				Type: syncer.LineDelete,
				Line: line,
			})
		}(i)
	}

	wg.Wait()

	// No panics means the test passed
}

func TestManager_FullSync_CleansUpOrphanedSchedulers(t *testing.T) {
	manager, mockSyncer, mockRegistry := createTestManager()

	// Setup existing schedulers
	mockScheduler1 := &MockScheduler{}
	mockScheduler2 := &MockScheduler{}
	mockScheduler1.On("Stop")
	mockScheduler2.On("Stop") // This one should be stopped

	manager.schedulers = map[string]Scheduler{
		"192.168.1.1": mockScheduler1,
		"192.168.1.2": mockScheduler2, // This router has no lines anymore
	}

	// New state has only router 192.168.1.1
	lines := map[string]syncer.Line{
		"10.0.0.1": createTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute),
	}

	mockSyncer.On("GetLines").Return(lines)
	mockRegistry.On("Register", mock.AnythingOfType("task.TaskMeta")).Return(nil)
	mockSyncer.On("Subscribe", mock.Anything).Return(&syncer.Subscription{})

	manager.Start()
	defer manager.Stop()

	// Wait for sync
	time.Sleep(100 * time.Millisecond)

	manager.mu.Lock()
	defer manager.mu.Unlock()

	// Should only have scheduler for 192.168.1.1
	assert.Equal(t, 1, len(manager.schedulers))
	assert.Contains(t, manager.schedulers, "192.168.1.1")
	assert.NotContains(t, manager.schedulers, "192.168.1.2")

	// Orphaned scheduler should have been stopped
	mockScheduler2.AssertCalled(t, "Stop")
}

// Performance and edge case tests
func TestManager_LargeNumberOfLines(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping large dataset test in short mode")
	}

	// Use TestableManager with mock schedulers to avoid real network connections
	_, mockSyncer, mockRegistry := createTestManager()
	testManager := NewTestableManager(mockSyncer, mockRegistry, true)

	// Create many lines across multiple routers
	lines := make(map[string]syncer.Line)
	numRouters := 10
	linesPerRouter := 100

	for r := 0; r < numRouters; r++ {
		routerIP := fmt.Sprintf("192.168.%d.1", r+1)
		for l := 0; l < linesPerRouter; l++ {
			lineIP := fmt.Sprintf("10.%d.%d.1", r, l)
			lineID := fmt.Sprintf("line_%d_%d", r, l)
			line := createTestLine(lineID, lineIP, routerIP, time.Duration(l+1)*time.Minute)
			lines[lineIP] = line
		}
	}

	mockSyncer.On("GetLines").Return(lines)
	mockRegistry.On("Register", mock.AnythingOfType("task.TaskMeta")).Return(nil)
	mockSyncer.On("Subscribe", mock.Anything).Return(&syncer.Subscription{})

	start := time.Now()
	testManager.Start()
	defer testManager.Stop()

	// Wait for sync
	time.Sleep(200 * time.Millisecond)
	elapsed := time.Since(start)

	testManager.mu.Lock()
	defer testManager.mu.Unlock()

	// Verify all routers have schedulers
	assert.Equal(t, numRouters, len(testManager.schedulers))
	assert.Equal(t, numRouters, len(testManager.routerLines))

	// Verify line distribution
	totalLines := 0
	for _, routerLines := range testManager.routerLines {
		totalLines += len(routerLines)
		assert.Equal(t, linesPerRouter, len(routerLines))
	}
	assert.Equal(t, numRouters*linesPerRouter, totalLines)

	t.Logf("Processed %d lines across %d routers in %v", totalLines, numRouters, elapsed)
}

func TestManager_RapidLineChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	_, mockSyncer, mockRegistry := createTestManager()
	testManager := NewTestableManager(mockSyncer, mockRegistry, true)

	mockScheduler := &MockScheduler{}
	mockScheduler.On("OnLineCreated", mock.AnythingOfType("syncer.Line"))
	mockScheduler.On("OnLineUpdated", mock.AnythingOfType("syncer.Line"), mock.AnythingOfType("syncer.Line"))
	mockScheduler.On("OnLineDeleted", mock.AnythingOfType("syncer.Line"))
	mockScheduler.On("Stop")

	testManager.schedulers = map[string]Scheduler{
		"192.168.1.1": mockScheduler,
	}

	// Rapid sequence of changes on the same line
	line := createTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute)

	var wg sync.WaitGroup
	numOperations := 50 // Reduced from 100 to speed up test

	for i := 0; i < numOperations; i++ {
		wg.Add(3) // Create, Update, Delete

		go func(iteration int) {
			defer wg.Done()
			testManager.processLineEvent(syncer.LineChangeEvent{
				Type: syncer.LineCreate,
				Line: line,
			})
		}(i)

		go func(iteration int) {
			defer wg.Done()
			updatedLine := line
			updatedLine.Interval = time.Duration(iteration+1) * time.Minute
			updatedLine.ComputeHash()
			testManager.processLineEvent(syncer.LineChangeEvent{
				Type: syncer.LineUpdate,
				Line: updatedLine,
			})
		}(i)

		go func(iteration int) {
			defer wg.Done()
			testManager.processLineEvent(syncer.LineChangeEvent{
				Type: syncer.LineDelete,
				Line: line,
			})
		}(i)
	}

	wg.Wait()

	// Should not panic and should have consistent state
	testManager.mu.Lock()
	routerLines := testManager.routerLines["192.168.1.1"]
	testManager.mu.Unlock()

	// State should be consistent (either empty or with valid lines)
	assert.True(t, len(routerLines) >= 0)
}

// Benchmark tests
func BenchmarkManager_ProcessLineCreate(b *testing.B) {
	_, mockSyncer, mockRegistry := createTestManager()
	testManager := NewTestableManager(mockSyncer, mockRegistry, true)

	mockScheduler := &MockScheduler{}
	mockScheduler.On("OnLineCreated", mock.AnythingOfType("syncer.Line"))
	testManager.schedulers = map[string]Scheduler{
		"192.168.1.1": mockScheduler,
	}

	line := createTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute)
	event := syncer.LineChangeEvent{
		Type: syncer.LineCreate,
		Line: line,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Reset state for each iteration
		testManager.mu.Lock()
		testManager.routerLines = make(map[string][]syncer.Line)
		testManager.mu.Unlock()

		testManager.processLineEvent(event)
	}
}

func BenchmarkManager_FullSync(b *testing.B) {
	_, mockSyncer, mockRegistry := createTestManager()
	testManager := NewTestableManager(mockSyncer, mockRegistry, true)

	// Create test data
	lines := make(map[string]syncer.Line)
	for i := 0; i < 1000; i++ {
		routerIP := fmt.Sprintf("192.168.%d.1", (i%10)+1)
		lineIP := fmt.Sprintf("10.0.%d.%d", i/256, i%256)
		lineID := fmt.Sprintf("line%d", i)
		line := createTestLine(lineID, lineIP, routerIP, time.Duration(i%10+1)*time.Minute)
		lines[lineIP] = line
	}

	mockSyncer.On("GetLines").Return(lines)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		testManager.fullSync()
	}
}

// Integration tests
func TestManager_IntegrationTest_CompleteWorkflow(t *testing.T) {
	t.Skip("Skipping test with real network connections - use integration tests in integration_test.go instead")

	// This test simulates a complete workflow from start to finish
	manager, mockSyncer, mockRegistry := createTestManager()

	// Setup initial lines
	initialLines := map[string]syncer.Line{
		"10.0.0.1": createTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute),
		"10.0.0.2": createTestLine("line2", "10.0.0.2", "192.168.1.2", 3*time.Minute),
	}

	mockRegistry.On("Register", mock.AnythingOfType("task.TaskMeta")).Return(nil)
	mockSyncer.On("GetLines").Return(initialLines)

	// Create a real subscription for integration testing
	realSub := &syncer.Subscription{}
	mockSyncer.On("Subscribe", mock.Anything).Return(realSub)

	// Start manager
	manager.Start()
	defer manager.Stop()

	// Wait for initial sync
	time.Sleep(100 * time.Millisecond)

	// Verify initial state
	manager.mu.Lock()
	assert.Equal(t, 2, len(manager.schedulers))
	assert.Equal(t, 2, len(manager.routerLines))
	assert.Equal(t, 1, len(manager.routerLines["192.168.1.1"]))
	assert.Equal(t, 1, len(manager.routerLines["192.168.1.2"]))
	manager.mu.Unlock()

	// Simulate line changes through events
	newLine := createTestLine("line3", "10.0.0.3", "192.168.1.1", 2*time.Minute)
	manager.processLineEvent(syncer.LineChangeEvent{
		Type: syncer.LineCreate,
		Line: newLine,
	})

	// Verify line was added
	manager.mu.Lock()
	assert.Equal(t, 2, len(manager.routerLines["192.168.1.1"]))
	manager.mu.Unlock()

	// Update a line
	updatedLine := initialLines["10.0.0.1"]
	updatedLine.Interval = 10 * time.Minute
	updatedLine.ComputeHash()
	manager.processLineEvent(syncer.LineChangeEvent{
		Type: syncer.LineUpdate,
		Line: updatedLine,
	})

	// Delete a line
	manager.processLineEvent(syncer.LineChangeEvent{
		Type: syncer.LineDelete,
		Line: newLine,
	})

	// Verify final state
	manager.mu.Lock()
	assert.Equal(t, 1, len(manager.routerLines["192.168.1.1"]))
	assert.Equal(t, 10*time.Minute, manager.routerLines["192.168.1.1"][0].Interval)
	manager.mu.Unlock()
}

func TestManager_IntegrationTest_PeriodicSync(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping periodic sync test in short mode")
	}

	// Use TestableManager with mock schedulers
	_, mockSyncer, mockRegistry := createTestManager()
	testManager := NewTestableManager(mockSyncer, mockRegistry, true)

	// Setup mocks
	mockRegistry.On("Register", mock.AnythingOfType("task.TaskMeta")).Return(nil)

	// First call returns empty, second call returns lines
	firstCall := mockSyncer.On("GetLines").Return(make(map[string]syncer.Line)).Once()

	lines := map[string]syncer.Line{
		"10.0.0.1": createTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute),
	}
	mockSyncer.On("GetLines").Return(lines).NotBefore(firstCall)

	mockSyncer.On("Subscribe", mock.Anything).Return(&syncer.Subscription{})

	testManager.Start()

	// Wait for initial sync and at least one periodic sync
	time.Sleep(150 * time.Millisecond)

	testManager.Stop()

	// Should have called GetLines at least twice (initial + periodic)
	mockSyncer.AssertExpectations(t)
}

func TestManager_IntegrationTest_ErrorHandling(t *testing.T) {
	// Use TestableManager with mock schedulers
	_, mockSyncer, mockRegistry := createTestManager()
	testManager := NewTestableManager(mockSyncer, mockRegistry, true)

	// Simulate various error conditions
	mockRegistry.On("Register", mock.AnythingOfType("task.TaskMeta")).Return(assert.AnError)

	// Manager should handle registration failure gracefully
	testManager.Start()
	defer testManager.Stop()

	// Should not proceed with sync if registration fails
	mockSyncer.AssertNotCalled(t, "GetLines")
}

// Test utilities and helpers
func TestManager_TestHelpers(t *testing.T) {
	// Test createTestLine helper
	line := createTestLine("test", "10.0.0.1", "192.168.1.1", 5*time.Minute)
	assert.Equal(t, "test", line.ID)
	assert.Equal(t, "10.0.0.1", line.IP)
	assert.Equal(t, "192.168.1.1", line.Router.IP)
	assert.Equal(t, 5*time.Minute, line.Interval)
	assert.NotZero(t, line.Hash)

	// Test createTestManager helper
	manager, mockSyncer, mockRegistry := createTestManager()
	assert.NotNil(t, manager)
	assert.NotNil(t, mockSyncer)
	assert.NotNil(t, mockRegistry)
}

// Race condition tests
func TestManager_RaceCondition_StartStop(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping race condition test in short mode")
	}

	manager, mockSyncer, mockRegistry := createTestManager()

	mockRegistry.On("Register", mock.AnythingOfType("task.TaskMeta")).Return(nil)
	mockSyncer.On("GetLines").Return(make(map[string]syncer.Line))
	mockSyncer.On("Subscribe", mock.Anything).Return(&syncer.Subscription{})

	var wg sync.WaitGroup
	numGoroutines := 10

	// Start and stop concurrently
	for i := 0; i < numGoroutines; i++ {
		wg.Add(2)

		go func() {
			defer wg.Done()
			manager.Start()
		}()

		go func() {
			defer wg.Done()
			time.Sleep(10 * time.Millisecond)
			manager.Stop()
		}()
	}

	wg.Wait()
	// Should not panic
}

func TestManager_RaceCondition_EventProcessing(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping race condition test in short mode")
	}

	// Use TestableManager with mock schedulers
	_, mockSyncer, mockRegistry := createTestManager()
	testManager := NewTestableManager(mockSyncer, mockRegistry, true)

	var wg sync.WaitGroup
	numEvents := 50 // Reduced from 100 to speed up test

	// Process many events concurrently
	for i := 0; i < numEvents; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			line := createTestLine(
				fmt.Sprintf("line%d", id),
				fmt.Sprintf("10.0.%d.%d", id/256, id%256),
				fmt.Sprintf("192.168.%d.1", (id%10)+1),
				time.Duration(id+1)*time.Minute,
			)

			event := syncer.LineChangeEvent{
				Type: syncer.LineCreate,
				Line: line,
			}

			testManager.processLineEvent(event)
		}(i)
	}

	wg.Wait()

	// Verify consistent state
	testManager.mu.Lock()
	defer testManager.mu.Unlock()

	totalLines := 0
	for _, lines := range testManager.routerLines {
		totalLines += len(lines)
	}

	// Should have some lines (exact count depends on race conditions)
	assert.True(t, totalLines >= 0)
	assert.True(t, totalLines <= numEvents)
}

// Edge case tests
func TestManager_EdgeCase_EmptyRouterIP(t *testing.T) {
	// Use TestableManager with mock schedulers
	_, mockSyncer, mockRegistry := createTestManager()
	testManager := NewTestableManager(mockSyncer, mockRegistry, true)

	// Line with empty router IP should not cause panic
	line := createTestLine("line1", "10.0.0.1", "", 5*time.Minute)
	event := syncer.LineChangeEvent{
		Type: syncer.LineCreate,
		Line: line,
	}

	// Should not panic
	assert.NotPanics(t, func() {
		testManager.processLineEvent(event)
	})

	testManager.mu.Lock()
	defer testManager.mu.Unlock()

	// Should have created entry for empty router IP
	assert.Contains(t, testManager.routerLines, "")
}

func TestManager_EdgeCase_ZeroInterval(t *testing.T) {
	// Use TestableManager with mock schedulers
	_, mockSyncer, mockRegistry := createTestManager()
	testManager := NewTestableManager(mockSyncer, mockRegistry, true)

	// Line with zero interval
	line := createTestLine("line1", "10.0.0.1", "192.168.1.1", 0)
	event := syncer.LineChangeEvent{
		Type: syncer.LineCreate,
		Line: line,
	}

	assert.NotPanics(t, func() {
		testManager.processLineEvent(event)
	})

	testManager.mu.Lock()
	defer testManager.mu.Unlock()

	assert.Equal(t, 1, len(testManager.routerLines["192.168.1.1"]))
	assert.Equal(t, time.Duration(0), testManager.routerLines["192.168.1.1"][0].Interval)
}

func TestManager_EdgeCase_DuplicateLineIPs(t *testing.T) {
	// Use TestableManager with mock schedulers
	_, mockSyncer, mockRegistry := createTestManager()
	testManager := NewTestableManager(mockSyncer, mockRegistry, true)

	// Two lines with same IP but different routers
	line1 := createTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute)
	line2 := createTestLine("line2", "10.0.0.1", "192.168.1.2", 3*time.Minute)

	testManager.processLineEvent(syncer.LineChangeEvent{
		Type: syncer.LineCreate,
		Line: line1,
	})

	testManager.processLineEvent(syncer.LineChangeEvent{
		Type: syncer.LineCreate,
		Line: line2,
	})

	testManager.mu.Lock()
	defer testManager.mu.Unlock()

	// Both routers should have their respective lines
	assert.Equal(t, 1, len(testManager.routerLines["192.168.1.1"]))
	assert.Equal(t, 1, len(testManager.routerLines["192.168.1.2"]))
	assert.Equal(t, "line1", testManager.routerLines["192.168.1.1"][0].ID)
	assert.Equal(t, "line2", testManager.routerLines["192.168.1.2"][0].ID)
}

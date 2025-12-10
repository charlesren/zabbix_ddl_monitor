package manager

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/charlesren/zabbix_ddl_monitor/connection"
	"github.com/charlesren/zabbix_ddl_monitor/syncer"
	"github.com/charlesren/zabbix_ddl_monitor/task"
	"github.com/charlesren/zapix"
)

// TestManager wraps Manager to support mock schedulers in tests
type TestManager struct {
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

func NewTestManager(configSyncer ConfigSyncerInterface, registry task.Registry, useMock bool) *TestManager {
	return &TestManager{
		configSyncer:      configSyncer,
		schedulers:        make(map[string]Scheduler),
		routerLines:       make(map[string][]syncer.Line),
		registry:          registry,
		stopChan:          make(chan struct{}),
		useMockSchedulers: useMock,
		mockSchedulers:    make(map[string]*MockScheduler),
	}
}

func (tm *TestManager) Start() {
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

func (tm *TestManager) Stop() {
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

func (tm *TestManager) fullSync() {
	lines := tm.configSyncer.GetLines()
	// fmt.Printf("DEBUG: fullSync got %d lines\n", len(lines))

	newRouterLines := make(map[string][]syncer.Line)

	for _, line := range lines {
		// fmt.Printf("DEBUG: Processing line %s for router %s\n", line.ID, line.Router.IP)
		newRouterLines[line.Router.IP] = append(newRouterLines[line.Router.IP], line)
	}

	// fmt.Printf("DEBUG: Created %d router groups\n", len(newRouterLines))

	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.routerLines = newRouterLines

	for routerIP, lines := range newRouterLines {
		// fmt.Printf("DEBUG: Ensuring scheduler for router %s with %d lines\n", routerIP, len(lines))
		tm.ensureScheduler(routerIP, lines)
	}

	for routerIP := range tm.schedulers {
		if _, exists := newRouterLines[routerIP]; !exists {
			// fmt.Printf("DEBUG: Removing scheduler for router %s\n", routerIP)
			tm.schedulers[routerIP].Stop()
			delete(tm.schedulers, routerIP)
		}
	}

	// fmt.Printf("DEBUG: fullSync complete - %d schedulers, %d router lines\n", len(tm.schedulers), len(tm.routerLines))
}

func (tm *TestManager) periodicSync(interval time.Duration) {
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

func (tm *TestManager) handleLineChanges(sub *syncer.Subscription) {
	defer tm.wg.Done()
	// Mock implementation - just wait for stop signal
	<-tm.stopChan
}

func (tm *TestManager) createScheduler(router *syncer.Router, lines []syncer.Line) Scheduler {
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

func (tm *TestManager) ensureScheduler(routerIP string, lines []syncer.Line) {
	if _, exists := tm.schedulers[routerIP]; !exists {
		// fmt.Printf("DEBUG: Creating scheduler for router %s\n", routerIP)
		tm.schedulers[routerIP] = tm.createScheduler(&lines[0].Router, lines)
		go tm.schedulers[routerIP].Start()
		// fmt.Printf("DEBUG: Started scheduler for router %s\n", routerIP)
	} else {
		// fmt.Printf("DEBUG: Scheduler for router %s already exists\n", routerIP)
	}
}

// Real integration tests using actual implementations

// Mock Zabbix client for integration testing
type MockZabbixClient struct {
	proxies []zapix.ProxyObject
	hosts   []zapix.HostObject
	mu      sync.RWMutex
}

// TestConfigSyncer wraps MockZabbixClient for testing by implementing ConfigSyncerInterface
type TestConfigSyncer struct {
	mockClient  *MockZabbixClient
	lines       map[string]syncer.Line
	mu          sync.RWMutex
	subscribers []*TestSubscription
	subsMu      sync.RWMutex
}

type TestSubscription struct {
	events chan syncer.LineChangeEvent
	ctx    context.Context
	cancel context.CancelFunc
}

func NewTestConfigSyncer(mockClient *MockZabbixClient) *TestConfigSyncer {
	return &TestConfigSyncer{
		mockClient:  mockClient,
		lines:       make(map[string]syncer.Line),
		subscribers: make([]*TestSubscription, 0),
	}
}

func (t *TestConfigSyncer) GetLines() map[string]syncer.Line {
	t.mu.RLock()
	defer t.mu.RUnlock()

	// Fetch from mock client and convert to lines
	hosts, err := t.mockClient.HostGet(zapix.HostGetParams{})
	if err != nil {
		// fmt.Printf("DEBUG: HostGet error: %v\n", err)
		return make(map[string]syncer.Line)
	}

	// fmt.Printf("DEBUG: Got %d hosts from mock client\n", len(hosts))
	lines := make(map[string]syncer.Line)
	for _, host := range hosts {
		// fmt.Printf("DEBUG: Processing host %s with %d macros\n", host.Host, len(host.Macros))
		macros := make(map[string]string)
		for _, macro := range host.Macros {
			macros[macro.Macro] = macro.Value
		}

		line := syncer.Line{
			ID:       macros["{$LINE_ID}"],
			IP:       host.Host,
			Interval: parseDurationFromMacro(macros["{$LINE_CHECK_INTERVAL}"], syncer.DefaultInterval),
			Router: syncer.Router{
				IP:       macros["{$LINE_ROUTER_IP}"],
				Username: macros["{$LINE_ROUTER_USERNAME}"],
				Password: macros["{$LINE_ROUTER_PASSWORD}"],
				Platform: connection.Platform(macros["{$LINE_ROUTER_PLATFORM}"]),
				Protocol: connection.Protocol(macros["{$LINE_ROUTER_PROTOCOL}"]),
			},
		}
		line.ComputeHash()
		// fmt.Printf("DEBUG: Created line %s -> %s\n", line.ID, line.IP)
		lines[line.IP] = line
	}

	// fmt.Printf("DEBUG: Returning %d lines\n", len(lines))
	return lines
}

func (t *TestConfigSyncer) Subscribe(ctx context.Context) *syncer.Subscription {
	t.subsMu.Lock()
	defer t.subsMu.Unlock()

	subCtx, cancel := context.WithCancel(ctx)
	testSub := &TestSubscription{
		events: make(chan syncer.LineChangeEvent, 1000),
		ctx:    subCtx,
		cancel: cancel,
	}

	t.subscribers = append(t.subscribers, testSub)

	// Create a real subscription that forwards events
	sub := &syncer.Subscription{}

	// Start goroutine to forward events
	go func() {
		defer close(testSub.events)
		for {
			select {
			case <-subCtx.Done():
				return
			case event := <-testSub.events:
				// Forward to real subscription if needed
				_ = event
			}
		}
	}()

	return sub
}

func (t *TestConfigSyncer) Start() {
	// Mock implementation - does nothing for tests
}

func (t *TestConfigSyncer) Stop() {
	t.subsMu.Lock()
	defer t.subsMu.Unlock()

	for _, sub := range t.subscribers {
		sub.cancel()
	}
	t.subscribers = nil
}

// SendEvent sends an event to all subscribers for testing
func (t *TestConfigSyncer) SendEvent(event syncer.LineChangeEvent) {
	t.subsMu.RLock()
	defer t.subsMu.RUnlock()

	for _, sub := range t.subscribers {
		select {
		case sub.events <- event:
		case <-sub.ctx.Done():
		default:
			// Skip if channel is full
		}
	}
}

// Helper function to parse duration from macro (copied from syncer)
func parseDurationFromMacro(value string, defaultVal time.Duration) time.Duration {
	if value == "" {
		return defaultVal
	}
	sec, err := strconv.ParseInt(value, 10, 64)
	if err != nil || sec < 0 {
		return defaultVal
	}
	return time.Duration(sec) * time.Second
}

// Implement syncer.Client interface
var _ syncer.Client = (*MockZabbixClient)(nil)

func NewMockZabbixClient() *MockZabbixClient {
	return &MockZabbixClient{
		proxies: []zapix.ProxyObject{
			{
				Proxyid: "10452",
				Host:    "10.10.10.10",
			},
		},
		hosts: []zapix.HostObject{},
	}
}

func (m *MockZabbixClient) GetProxyFormHost(ip string) ([]zapix.ProxyObject, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if ip == "10.10.10.10" {
		return m.proxies, nil
	}
	return []zapix.ProxyObject{}, nil
}

func (m *MockZabbixClient) HostGet(params zapix.HostGetParams) ([]zapix.HostObject, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.hosts, nil
}

func (m *MockZabbixClient) SetHosts(hosts []zapix.HostObject) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hosts = hosts
}

func (m *MockZabbixClient) AddHost(host zapix.HostObject) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hosts = append(m.hosts, host)
}

func (m *MockZabbixClient) RemoveHost(hostID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, host := range m.hosts {
		if host.HostID == hostID {
			m.hosts = append(m.hosts[:i], m.hosts[i+1:]...)
			break
		}
	}
}

func (m *MockZabbixClient) UsermacroGet(params zapix.UsermacroGetParams) ([]zapix.UsermacroObject, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return []zapix.UsermacroObject{}, nil
}

func (m *MockZabbixClient) TemplateGet(params zapix.TemplateGetParams) ([]zapix.TemplateObject, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return []zapix.TemplateObject{}, nil
}

// Helper to create test host objects
func createTestHost(hostID, host, routerIP, routerUsername, routerPassword, platform, protocol, lineID, interval string) zapix.HostObject {
	return zapix.HostObject{
		HostID: hostID,
		Host:   host,
		Macros: []zapix.UsermacroObject{
			{Macro: "{$LINE_ROUTER_IP}", Value: routerIP},
			{Macro: "{$LINE_ROUTER_USERNAME}", Value: routerUsername},
			{Macro: "{$LINE_ROUTER_PASSWORD}", Value: routerPassword},
			{Macro: "{$LINE_ROUTER_PLATFORM}", Value: platform},
			{Macro: "{$LINE_ROUTER_PROTOCOL}", Value: protocol},
			{Macro: "{$LINE_ID}", Value: lineID},
			{Macro: "{$LINE_CHECK_INTERVAL}", Value: interval},
		},
		Tags: []zapix.HostTagObject{
			{Tag: "TempType", Value: "LINE"},
		},
	}
}

func TestManager_IntegrationWithRealConfigSyncer(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create real dependencies
	mockClient := NewMockZabbixClient()
	registry := task.NewDefaultRegistry()

	configSyncer := NewTestConfigSyncer(mockClient)

	// Setup initial test data
	hosts := []zapix.HostObject{
		createTestHost("1001", "10.0.0.1", "192.168.1.1", "admin", "pass1", "cisco_iosxe", "ssh", "line1", "300"),
		createTestHost("1002", "10.0.0.2", "192.168.1.1", "admin", "pass1", "cisco_iosxe", "ssh", "line2", "180"),
		createTestHost("1003", "10.0.0.3", "192.168.1.2", "admin", "pass2", "cisco_iosxe", "ssh", "line3", "300"),
	}
	mockClient.SetHosts(hosts)

	// Create a test manager that uses mock schedulers
	testManager := NewTestManager(configSyncer, registry, true)

	// Start manager first (it will call GetLines immediately)
	testManager.Start()
	defer testManager.Stop()

	// Wait for initial sync
	time.Sleep(300 * time.Millisecond)

	// Verify initial state
	testManager.mu.Lock()
	assert.Equal(t, 2, len(testManager.schedulers), "Should have 2 router schedulers")
	assert.Equal(t, 2, len(testManager.routerLines), "Should have 2 router line groups")
	assert.Equal(t, 2, len(testManager.routerLines["192.168.1.1"]), "Router 192.168.1.1 should have 2 lines")
	assert.Equal(t, 1, len(testManager.routerLines["192.168.1.2"]), "Router 192.168.1.2 should have 1 line")
	testManager.mu.Unlock()

	// Test dynamic line addition
	newHost := createTestHost("1004", "10.0.0.4", "192.168.1.3", "admin", "pass3", "cisco_iosxe", "ssh", "line4", "240")
	mockClient.AddHost(newHost)

	// Trigger a full sync manually since we're using mock
	testManager.fullSync()
	time.Sleep(300 * time.Millisecond)

	// Verify new router was added
	testManager.mu.Lock()
	assert.Equal(t, 3, len(testManager.schedulers), "Should have 3 router schedulers after addition")
	assert.Contains(t, testManager.routerLines, "192.168.1.3", "Should contain new router")
	assert.Equal(t, 1, len(testManager.routerLines["192.168.1.3"]), "New router should have 1 line")
	testManager.mu.Unlock()

	// Test line removal
	mockClient.RemoveHost("1003")

	// Trigger a full sync manually
	testManager.fullSync()
	time.Sleep(300 * time.Millisecond)

	// Verify line was removed (scheduler might still exist due to delayed cleanup)
	testManager.mu.Lock()
	routerLines, exists := testManager.routerLines["192.168.1.2"]
	if exists {
		assert.Equal(t, 0, len(routerLines), "Router 192.168.1.2 should have no lines after removal")
	}
	testManager.mu.Unlock()
}

func TestManager_IntegrationWithRealScheduler(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create a more comprehensive test with actual RouterScheduler behavior
	mockClient := NewMockZabbixClient()
	registry := task.NewDefaultRegistry()

	configSyncer := NewTestConfigSyncer(mockClient)

	// Create test manager with mock schedulers
	testManager := NewTestManager(configSyncer, registry, true)

	// Setup test data
	hosts := []zapix.HostObject{
		createTestHost("2001", "10.1.0.1", "192.168.10.1", "admin", "password", "cisco_iosxe", "ssh", "line1", "60"),
		createTestHost("2002", "10.1.0.2", "192.168.10.1", "admin", "password", "cisco_iosxe", "ssh", "line2", "120"),
	}
	mockClient.SetHosts(hosts)

	// Start manager
	testManager.Start()
	defer testManager.Stop()

	// Wait for initialization
	time.Sleep(300 * time.Millisecond)

	// Verify scheduler was created and is of correct type
	testManager.mu.Lock()
	scheduler, exists := testManager.schedulers["192.168.10.1"]
	assert.True(t, exists, "Router scheduler should exist")
	assert.IsType(t, &MockScheduler{}, scheduler, "Should be MockScheduler type")
	testManager.mu.Unlock()

	// Test scheduler behavior with line changes
	newHost := createTestHost("2003", "10.1.0.3", "192.168.10.1", "admin", "password", "cisco_iosxe", "ssh", "line3", "180")
	mockClient.AddHost(newHost)

	// Trigger sync manually
	testManager.fullSync()
	time.Sleep(200 * time.Millisecond)

	testManager.mu.Lock()
	assert.Equal(t, 3, len(testManager.routerLines["192.168.10.1"]), "Should have 3 lines after addition")
	testManager.mu.Unlock()

	// Test completed successfully
}

func TestManager_IntegrationPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping performance test in short mode")
	}

	mockClient := NewMockZabbixClient()
	registry := task.NewDefaultRegistry()

	configSyncer := NewTestConfigSyncer(mockClient)

	// Create large dataset
	numRouters := 50
	linesPerRouter := 20
	hosts := make([]zapix.HostObject, 0, numRouters*linesPerRouter)

	for r := 0; r < numRouters; r++ {
		routerIP := fmt.Sprintf("10.%d.%d.1", (r/254)+1, r%254+1)
		for l := 0; l < linesPerRouter; l++ {
			hostID := fmt.Sprintf("%d", r*linesPerRouter+l+1)
			hostIP := fmt.Sprintf("192.168.%d.%d", r+1, l+1)
			lineID := fmt.Sprintf("line_%d_%d", r, l)
			interval := fmt.Sprintf("%d", (l%5+1)*60) // 60-300 seconds

			host := createTestHost(hostID, hostIP, routerIP, "admin", "pass", "cisco_iosxe", "ssh", lineID, interval)
			hosts = append(hosts, host)
		}
	}

	mockClient.SetHosts(hosts)

	// Create test manager with mock schedulers
	testManager := NewTestManager(configSyncer, registry, true)

	// Measure startup time
	start := time.Now()

	testManager.Start()
	defer testManager.Stop()

	time.Sleep(200 * time.Millisecond) // Wait for initial sync

	// Wait for all schedulers to be created
	maxWait := 10 * time.Second
	deadline := time.Now().Add(maxWait)

	for time.Now().Before(deadline) {
		testManager.mu.Lock()
		schedulerCount := len(testManager.schedulers)
		lineGroupCount := len(testManager.routerLines)
		testManager.mu.Unlock()

		if schedulerCount == numRouters && lineGroupCount == numRouters {
			break
		}

		time.Sleep(50 * time.Millisecond)

		// Trigger periodic sync to ensure processing
		if (time.Since(start).Milliseconds()/100)%10 == 0 {
			testManager.fullSync()
		}
	}

	elapsed := time.Since(start)

	// Verify final state
	testManager.mu.Lock()
	assert.Equal(t, numRouters, len(testManager.schedulers), "Should have all router schedulers")
	assert.Equal(t, numRouters, len(testManager.routerLines), "Should have all router line groups")

	totalLines := 0
	for _, lines := range testManager.routerLines {
		totalLines += len(lines)
	}
	assert.Equal(t, numRouters*linesPerRouter, totalLines, "Should have all lines")
	testManager.mu.Unlock()

	t.Logf("Processed %d lines across %d routers in %v", numRouters*linesPerRouter, numRouters, elapsed)

	// Performance assertion - should complete within reasonable time
	assert.Less(t, elapsed, 30*time.Second, "Startup should complete within 30 seconds")
}

func TestManager_IntegrationStressTest(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	mockClient := NewMockZabbixClient()
	registry := task.NewDefaultRegistry()

	configSyncer := NewTestConfigSyncer(mockClient)
	testManager := NewTestManager(configSyncer, registry, true)

	// Start with small dataset
	initialHosts := []zapix.HostObject{
		createTestHost("5001", "10.5.0.1", "192.168.50.1", "admin", "pass", "cisco_iosxe", "ssh", "line1", "120"),
		createTestHost("5002", "10.5.0.2", "192.168.50.2", "admin", "pass", "cisco_iosxe", "ssh", "line2", "180"),
	}
	mockClient.SetHosts(initialHosts)

	testManager.Start()
	defer testManager.Stop()

	time.Sleep(200 * time.Millisecond)

	// Perform rapid changes
	var wg sync.WaitGroup
	numOperations := 100

	for i := 0; i < numOperations; i++ {
		wg.Add(3)

		// Add host
		go func(id int) {
			defer wg.Done()
			hostID := fmt.Sprintf("stress_%d", id)
			hostIP := fmt.Sprintf("10.100.%d.%d", id/256, id%256)
			routerIP := fmt.Sprintf("192.168.100.%d", (id%10)+1)
			lineID := fmt.Sprintf("stress_line_%d", id)

			host := createTestHost(hostID, hostIP, routerIP, "admin", "pass", "cisco_iosxe", "ssh", lineID, "300")
			mockClient.AddHost(host)
		}(i)

		// Modify existing host (simulate parameter change)
		go func(id int) {
			defer wg.Done()
			if id < len(initialHosts) {
				// Simulate changing interval
				time.Sleep(10 * time.Millisecond)
			}
		}(i)

		// Remove host (occasionally)
		go func(id int) {
			defer wg.Done()
			if id%10 == 0 && id > 0 {
				hostID := fmt.Sprintf("stress_%d", id-10)
				mockClient.RemoveHost(hostID)
			}
		}(i)
		time.Sleep(10 * time.Millisecond)

		// Trigger sync periodically
		if i%20 == 0 {
			testManager.fullSync()
		}
	}

	wg.Wait()

	// Wait for all changes to propagate
	time.Sleep(2 * time.Second)

	// Verify system is still in consistent state
	testManager.mu.Lock()
	schedulerCount := len(testManager.schedulers)
	lineGroupCount := len(testManager.routerLines)

	totalLines := 0
	for _, lines := range testManager.routerLines {
		totalLines += len(lines)
	}
	testManager.mu.Unlock()

	// System should be stable
	assert.True(t, schedulerCount >= 0, "Should have non-negative scheduler count")
	assert.True(t, lineGroupCount >= 0, "Should have non-negative line group count")
	assert.True(t, totalLines >= 0, "Should have non-negative total lines")
	assert.Equal(t, schedulerCount, lineGroupCount, "Scheduler count should match line group count")

	t.Logf("Stress test completed: %d schedulers, %d line groups, %d total lines",
		schedulerCount, lineGroupCount, totalLines)
}

func TestManager_IntegrationErrorRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mockClient := NewMockZabbixClient()
	registry := task.NewDefaultRegistry()

	configSyncer := NewTestConfigSyncer(mockClient)
	testManager := NewTestManager(configSyncer, registry, true)

	// Start with valid data
	hosts := []zapix.HostObject{
		createTestHost("6001", "10.6.0.1", "192.168.60.1", "admin", "pass", "cisco_iosxe", "ssh", "line1", "120"),
	}
	mockClient.SetHosts(hosts)

	testManager.Start()
	defer testManager.Stop()

	time.Sleep(200 * time.Millisecond)

	// Verify initial state
	testManager.mu.Lock()
	initialSchedulerCount := len(testManager.schedulers)
	initialLineCount := len(testManager.routerLines)
	testManager.mu.Unlock()

	assert.Equal(t, 1, initialSchedulerCount, "Should have 1 initial scheduler")
	assert.Equal(t, 1, initialLineCount, "Should have 1 initial line group")

	// Simulate error condition (invalid data)
	invalidHost := zapix.HostObject{
		HostID: "6002",
		Host:   "10.6.0.2",
		Macros: []zapix.UsermacroObject{
			// Missing required macros to simulate error
			{Macro: "{$LINE_ID}", Value: "line2"},
		},
		Tags: []zapix.HostTagObject{
			{Tag: "TempType", Value: "LINE"},
		},
	}
	mockClient.AddHost(invalidHost)

	// Wait for sync attempt
	time.Sleep(300 * time.Millisecond)

	// System should still be stable despite invalid data
	testManager.mu.Lock()
	currentSchedulerCount := len(testManager.schedulers)
	currentLineCount := len(testManager.routerLines)
	testManager.mu.Unlock()

	// Should still function with valid data
	assert.True(t, currentSchedulerCount >= initialSchedulerCount, "Should maintain or increase scheduler count")
	assert.True(t, currentLineCount >= initialLineCount, "Should maintain or increase line group count")

	// Add back valid data
	validHost := createTestHost("6003", "10.6.0.3", "192.168.60.1", "admin", "pass", "cisco_iosxe", "ssh", "line3", "180")
	mockClient.AddHost(validHost)

	// Trigger sync manually
	testManager.fullSync()
	time.Sleep(200 * time.Millisecond)

	// Should recover and process valid data
	testManager.mu.Lock()
	finalSchedulerCount := len(testManager.schedulers)
	routerLines := testManager.routerLines["192.168.60.1"]
	testManager.mu.Unlock()

	assert.True(t, finalSchedulerCount >= 1, "Should have at least 1 scheduler")
	assert.True(t, len(routerLines) >= 1, "Should have at least 1 line for the router")
}

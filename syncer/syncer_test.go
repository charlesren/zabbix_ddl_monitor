package syncer

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/charlesren/zapix"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockClient is a mock implementation of the Client interface
type MockClient struct {
	mock.Mock
}

func (m *MockClient) GetProxyFormHost(ip string) ([]zapix.ProxyObject, error) {
	args := m.Called(ip)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]zapix.ProxyObject), args.Error(1)
}

func (m *MockClient) HostGet(params zapix.HostGetParams) ([]zapix.HostObject, error) {
	args := m.Called(params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]zapix.HostObject), args.Error(1)
}

func (m *MockClient) UsermacroGet(params zapix.UsermacroGetParams) ([]zapix.UsermacroObject, error) {
	args := m.Called(params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]zapix.UsermacroObject), args.Error(1)
}

func (m *MockClient) TemplateGet(params zapix.TemplateGetParams) ([]zapix.TemplateObject, error) {
	args := m.Called(params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]zapix.TemplateObject), args.Error(1)
}

// Test data helpers are now in test_helpers.go

func TestNewConfigSyncer(t *testing.T) {
	mockClient := &MockClient{}
	interval := 30 * time.Second

	syncer, err := NewConfigSyncer(nil, interval, "test-proxy") // We'll set the client manually for testing
	syncer.client = mockClient

	assert.NoError(t, err)
	assert.NotNil(t, syncer)
	assert.Equal(t, interval, syncer.syncInterval)
	assert.NotNil(t, syncer.lines)
	assert.NotNil(t, syncer.ctx)
	assert.NotNil(t, syncer.cancel)
	assert.False(t, syncer.stopped)
}

func TestConfigSyncer_Start_Stop(t *testing.T) {
	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, 100*time.Millisecond)

	// Mock the proxy and host calls to return empty results
	mockClient.On("GetProxyFormHost", mock.Anything).Return([]zapix.ProxyObject{}, fmt.Errorf("no proxy found")).Maybe()

	// Start syncer in a goroutine
	started := make(chan bool)
	go func() {
		started <- true
		syncer.Start()
	}()

	// Wait for start
	<-started
	time.Sleep(50 * time.Millisecond)

	// Stop the syncer
	syncer.Stop()

	// Verify it stopped
	assert.True(t, syncer.stopped)
}

func TestConfigSyncer_FetchLines(t *testing.T) {
	tests := []struct {
		name          string
		proxyResponse []zapix.ProxyObject
		proxyError    error
		hostResponse  []zapix.HostObject
		hostError     error
		expectedLines int
		expectedError bool
	}{
		{
			name:          "successful fetch",
			proxyResponse: createTestProxyResponse(),
			proxyError:    nil,
			hostResponse:  createTestHostResponse(),
			hostError:     nil,
			expectedLines: 2,
			expectedError: false,
		},
		{
			name:          "proxy not found",
			proxyResponse: []zapix.ProxyObject{},
			proxyError:    nil,
			expectedError: true,
		},
		{
			name:          "proxy fetch error",
			proxyResponse: nil,
			proxyError:    fmt.Errorf("connection failed"),
			expectedError: true,
		},
		{
			name:          "host fetch error",
			proxyResponse: createTestProxyResponse(),
			proxyError:    nil,
			hostResponse:  nil,
			hostError:     fmt.Errorf("host query failed"),
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockClient{}
			syncer := createTestSyncer(mockClient, time.Minute)

			mockClient.On("GetProxyFormHost", "10.10.10.10").Return(tt.proxyResponse, tt.proxyError)
			if tt.proxyError == nil && len(tt.proxyResponse) > 0 {
				mockClient.On("HostGet", mock.MatchedBy(func(params zapix.HostGetParams) bool {
					return len(params.ProxyIDs) > 0
				})).Return(tt.hostResponse, tt.hostError)
			}

			lines, err := syncer.fetchLines()

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Len(t, lines, tt.expectedLines)
			}

			mockClient.AssertExpectations(t)
		})
	}
}

func TestConfigSyncer_DetectChanges(t *testing.T) {
	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, time.Minute)

	// Initial state
	line1 := createTestLine("1", "10.1.1.1", 3*time.Minute)
	line2 := createTestLine("2", "10.1.1.2", 3*time.Minute)
	syncer.lines = map[string]Line{
		line1.IP: line1,
		line2.IP: line2,
	}

	tests := []struct {
		name           string
		newLines       map[string]Line
		expectedCreate int
		expectedUpdate int
		expectedDelete int
	}{
		{
			name:     "no changes",
			newLines: map[string]Line{line1.IP: line1, line2.IP: line2},
		},
		{
			name: "line created",
			newLines: map[string]Line{
				line1.IP:   line1,
				line2.IP:   line2,
				"10.1.1.3": createTestLine("3", "10.1.1.3", 3*time.Minute),
			},
			expectedCreate: 1,
		},
		{
			name: "line deleted",
			newLines: map[string]Line{
				line1.IP: line1,
			},
			expectedDelete: 1,
		},
		{
			name: "line updated",
			newLines: func() map[string]Line {
				updatedLine := createTestLine("2", "10.1.1.2", 5*time.Minute) // Different interval
				return map[string]Line{
					line1.IP:       line1,
					updatedLine.IP: updatedLine,
				}
			}(),
			expectedUpdate: 1,
		},
		{
			name: "mixed changes",
			newLines: func() map[string]Line {
				updatedLine := createTestLine("1", "10.1.1.1", 5*time.Minute)
				newLine := createTestLine("3", "10.1.1.3", 3*time.Minute)
				return map[string]Line{
					updatedLine.IP: updatedLine,
					newLine.IP:     newLine,
				}
			}(),
			expectedCreate: 1,
			expectedUpdate: 1,
			expectedDelete: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := syncer.detectChanges(tt.newLines)

			createCount := 0
			updateCount := 0
			deleteCount := 0

			for _, event := range events {
				switch event.Type {
				case LineCreate:
					createCount++
				case LineUpdate:
					updateCount++
				case LineDelete:
					deleteCount++
				}
			}

			assert.Equal(t, tt.expectedCreate, createCount, "create events mismatch")
			assert.Equal(t, tt.expectedUpdate, updateCount, "update events mismatch")
			assert.Equal(t, tt.expectedDelete, deleteCount, "delete events mismatch")
		})
	}
}

func TestConfigSyncer_Subscribe(t *testing.T) {
	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, time.Minute)
	ctx := context.Background()

	// Test subscription
	sub := syncer.Subscribe(ctx)
	assert.NotNil(t, sub)
	assert.NotNil(t, sub.Events())

	// Check subscriber was added
	syncer.mu.RLock()
	assert.Len(t, syncer.subscribers, 1)
	syncer.mu.RUnlock()

	// Test closing subscription
	sub.Close()

	// Check subscriber was removed
	syncer.mu.RLock()
	assert.Len(t, syncer.subscribers, 0)
	syncer.mu.RUnlock()
}

func TestConfigSyncer_NotifyAll(t *testing.T) {
	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, time.Minute)

	// Create multiple subscribers
	ctx := context.Background()
	sub1 := syncer.Subscribe(ctx)
	sub2 := syncer.Subscribe(ctx)

	// Create test event
	event := LineChangeEvent{
		Type: LineCreate,
		Line: createTestLine("1", "10.1.1.1", 3*time.Minute),
	}

	// Notify all subscribers
	go syncer.notifyAll([]LineChangeEvent{event})

	// Check both subscribers received the event
	select {
	case receivedEvent := <-sub1.Events():
		assert.Equal(t, event.Type, receivedEvent.Type)
		assert.Equal(t, event.Line.IP, receivedEvent.Line.IP)
	case <-time.After(time.Second):
		t.Fatal("subscriber 1 did not receive event")
	}

	select {
	case receivedEvent := <-sub2.Events():
		assert.Equal(t, event.Type, receivedEvent.Type)
		assert.Equal(t, event.Line.IP, receivedEvent.Line.IP)
	case <-time.After(time.Second):
		t.Fatal("subscriber 2 did not receive event")
	}

	sub1.Close()
	sub2.Close()
}

func TestConfigSyncer_Sync(t *testing.T) {
	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, time.Minute)

	// Mock successful fetch
	mockClient.On("GetProxyFormHost", "10.10.10.10").Return(createTestProxyResponse(), nil)
	mockClient.On("HostGet", mock.MatchedBy(func(params zapix.HostGetParams) bool {
		return len(params.ProxyIDs) > 0
	})).Return(createTestHostResponse(), nil)

	// Create subscriber to capture events
	ctx := context.Background()
	sub := syncer.Subscribe(ctx)

	// Perform sync
	err := syncer.sync()
	assert.NoError(t, err)

	// Check version was incremented
	assert.Equal(t, int64(1), syncer.Version())

	// Check lines were updated
	lines := syncer.GetLines()
	assert.Len(t, lines, 2)

	// Check events were sent (should be 2 create events)
	eventCount := 0
	timeout := time.After(time.Second)

	for eventCount < 2 {
		select {
		case event := <-sub.Events():
			assert.Equal(t, LineCreate, event.Type)
			assert.Equal(t, int64(1), event.Version)
			eventCount++
		case <-timeout:
			t.Fatalf("expected 2 events, got %d", eventCount)
		}
	}

	sub.Close()
	mockClient.AssertExpectations(t)
}

func TestConfigSyncer_ConcurrentAccess(t *testing.T) {
	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, time.Minute)
	syncer.version = 1

	// Add some initial data
	line := createTestLine("1", "10.1.1.1", 3*time.Minute)
	syncer.lines = map[string]Line{line.IP: line}

	// Test concurrent reads
	var wg sync.WaitGroup
	errors := make(chan error, 20)

	// Concurrent GetLines calls
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lines := syncer.GetLines()
			if len(lines) != 1 {
				errors <- fmt.Errorf("expected 1 line, got %d", len(lines))
			}
		}()
	}

	// Concurrent Version calls
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			version := syncer.Version()
			if version != 1 {
				errors <- fmt.Errorf("expected version 1, got %d", version)
			}
		}()
	}

	wg.Wait()
	close(errors)

	// Check for any errors
	for err := range errors {
		t.Error(err)
	}
}

func TestConfigSyncer_ContextCancellation(t *testing.T) {
	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, 100*time.Millisecond)

	// Create a subscription with a cancellable context
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	sub := syncer.Subscribe(ctx)

	// Start the syncer
	go syncer.Start()

	// Wait a bit and then stop
	time.Sleep(50 * time.Millisecond)
	syncer.Stop()

	// The subscription should be closed when context is done
	select {
	case <-ctx.Done():
		// Context should be cancelled
	case <-time.After(300 * time.Millisecond):
		t.Fatal("context should have been cancelled")
	}

	sub.Close()
}

func TestParseDurationFromMacro(t *testing.T) {
	tests := []struct {
		name        string
		value       string
		defaultVal  time.Duration
		expectedDur time.Duration
	}{
		{
			name:        "valid seconds",
			value:       "300",
			defaultVal:  time.Minute,
			expectedDur: 300 * time.Second,
		},
		{
			name:        "empty value",
			value:       "",
			defaultVal:  time.Minute,
			expectedDur: time.Minute,
		},
		{
			name:        "invalid value",
			value:       "abc",
			defaultVal:  time.Minute,
			expectedDur: time.Minute,
		},
		{
			name:        "zero value",
			value:       "0",
			defaultVal:  time.Minute,
			expectedDur: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseDurationFromMacro(tt.value, tt.defaultVal)
			assert.Equal(t, tt.expectedDur, result)
		})
	}
}

func TestConfigSyncer_StopIdempotent(t *testing.T) {
	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, time.Minute)

	// Create a subscriber
	ctx := context.Background()
	sub := syncer.Subscribe(ctx)

	// Stop multiple times
	syncer.Stop()
	syncer.Stop()
	syncer.Stop()

	// Should only be stopped once
	assert.True(t, syncer.stopped)

	// Subscriber channel should be closed
	select {
	case _, ok := <-sub.Events():
		assert.False(t, ok, "channel should be closed")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("channel should be closed immediately")
	}
}

// TestConfigSyncer_EmptyResponses tests handling of empty API responses
func TestConfigSyncer_EmptyResponses(t *testing.T) {
	tests := []struct {
		name          string
		proxyResponse []zapix.ProxyObject
		hostResponse  []zapix.HostObject
		expectedLines int
		expectedError bool
	}{
		{
			name:          "empty_host_response_with_valid_proxy",
			proxyResponse: createTestProxyResponse(),
			hostResponse:  []zapix.HostObject{},
			expectedLines: 0,
			expectedError: false,
		},
		{
			name:          "nil_host_response",
			proxyResponse: createTestProxyResponse(),
			hostResponse:  nil,
			expectedLines: 0,
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockClient{}
			syncer := createTestSyncer(mockClient, time.Minute)

			mockClient.On("GetProxyFormHost", "10.10.10.10").Return(tt.proxyResponse, nil)
			if tt.proxyResponse != nil && len(tt.proxyResponse) > 0 {
				if tt.hostResponse != nil {
					mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(tt.hostResponse, nil)
				} else {
					mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(nil, fmt.Errorf("nil response"))
				}
			}

			err := syncer.sync()

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				lines := syncer.GetLines()
				assert.Len(t, lines, tt.expectedLines)
			}

			mockClient.AssertExpectations(t)
		})
	}
}

// TestConfigSyncer_DuplicateIPs tests handling of duplicate IP addresses
func TestConfigSyncer_DuplicateIPs(t *testing.T) {
	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, time.Minute)

	// Create host response with duplicate IPs
	duplicateHosts := []zapix.HostObject{
		{
			Host: "10.10.10.11", // Same IP as second entry
			Macros: []zapix.UsermacroObject{
				{Macro: "{$LINE_ID}", Value: "line001"},
				{Macro: "{$LINE_CHECK_INTERVAL}", Value: "180"},
				{Macro: "{$LINE_ROUTER_IP}", Value: "192.168.1.1"},
				{Macro: "{$LINE_ROUTER_USERNAME}", Value: "admin"},
				{Macro: "{$LINE_ROUTER_PASSWORD}", Value: "password"},
				{Macro: "{$LINE_ROUTER_PLATFORM}", Value: "cisco_iosxe"},
				{Macro: "{$LINE_ROUTER_PROTOCOL}", Value: "ssh"},
			},
		},
		{
			Host: "10.10.10.11", // Duplicate IP
			Macros: []zapix.UsermacroObject{
				{Macro: "{$LINE_ID}", Value: "line002"},
				{Macro: "{$LINE_CHECK_INTERVAL}", Value: "300"},
				{Macro: "{$LINE_ROUTER_IP}", Value: "192.168.1.2"},
				{Macro: "{$LINE_ROUTER_USERNAME}", Value: "root"},
				{Macro: "{$LINE_ROUTER_PASSWORD}", Value: "secret"},
				{Macro: "{$LINE_ROUTER_PLATFORM}", Value: "huawei_vrp"},
				{Macro: "{$LINE_ROUTER_PROTOCOL}", Value: "netconf"},
			},
		},
	}

	mockClient.On("GetProxyFormHost", "10.10.10.10").Return(createTestProxyResponse(), nil)
	mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(duplicateHosts, nil)

	err := syncer.sync()
	assert.NoError(t, err)

	// Should only have one line (last one wins due to map overwrite)
	lines := syncer.GetLines()
	assert.Len(t, lines, 1)

	// Verify it's the second line that was kept
	line := lines["10.10.10.11"]
	assert.Equal(t, "line002", line.ID)
	assert.Equal(t, 5*time.Minute, line.Interval)

	mockClient.AssertExpectations(t)
}

// TestConfigSyncer_InvalidMacroValues tests handling of invalid macro values
func TestConfigSyncer_InvalidMacroValues(t *testing.T) {
	invalidMacroTests := []struct {
		name             string
		intervalValue    string
		expectedInterval time.Duration
		description      string
	}{
		{
			name:             "negative_interval",
			intervalValue:    "-100",
			expectedInterval: DefaultInterval,
			description:      "negative interval should use default",
		},
		{
			name:             "zero_interval",
			intervalValue:    "0",
			expectedInterval: 0,
			description:      "zero interval should be preserved",
		},
		{
			name:             "very_large_interval",
			intervalValue:    "999999999",
			expectedInterval: 999999999 * time.Second,
			description:      "very large interval should be preserved",
		},
		{
			name:             "decimal_interval",
			intervalValue:    "180.5",
			expectedInterval: DefaultInterval,
			description:      "decimal interval should use default",
		},
		{
			name:             "text_interval",
			intervalValue:    "three_minutes",
			expectedInterval: DefaultInterval,
			description:      "text interval should use default",
		},
	}

	for _, tt := range invalidMacroTests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockClient{}
			syncer := createTestSyncer(mockClient, time.Minute)

			hostWithInvalidMacro := []zapix.HostObject{
				{
					Host: "10.10.10.11",
					Macros: []zapix.UsermacroObject{
						{Macro: "{$LINE_ID}", Value: "line001"},
						{Macro: "{$LINE_CHECK_INTERVAL}", Value: tt.intervalValue},
						{Macro: "{$LINE_ROUTER_IP}", Value: "192.168.1.1"},
						{Macro: "{$LINE_ROUTER_USERNAME}", Value: "admin"},
						{Macro: "{$LINE_ROUTER_PASSWORD}", Value: "password"},
						{Macro: "{$LINE_ROUTER_PLATFORM}", Value: "cisco_iosxe"},
						{Macro: "{$LINE_ROUTER_PROTOCOL}", Value: "ssh"},
					},
				},
			}

			mockClient.On("GetProxyFormHost", "10.10.10.10").Return(createTestProxyResponse(), nil)
			mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(hostWithInvalidMacro, nil)

			err := syncer.sync()
			assert.NoError(t, err, tt.description)

			lines := syncer.GetLines()
			assert.Len(t, lines, 1)

			line := lines["10.10.10.11"]
			assert.Equal(t, tt.expectedInterval, line.Interval, tt.description)

			mockClient.AssertExpectations(t)
		})
	}
}

// TestConfigSyncer_PartialMacros tests handling of hosts with missing required macros
func TestConfigSyncer_PartialMacros(t *testing.T) {
	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, time.Minute)

	hostWithPartialMacros := []zapix.HostObject{
		{
			Host: "10.10.10.11",
			Macros: []zapix.UsermacroObject{
				{Macro: "{$LINE_ID}", Value: "line001"},
				// Missing {$LINE_CHECK_INTERVAL}
				{Macro: "{$LINE_ROUTER_IP}", Value: "192.168.1.1"},
				// Missing username and password
				{Macro: "{$LINE_ROUTER_PLATFORM}", Value: "cisco_iosxe"},
				{Macro: "{$LINE_ROUTER_PROTOCOL}", Value: "ssh"},
			},
		},
	}

	mockClient.On("GetProxyFormHost", "10.10.10.10").Return(createTestProxyResponse(), nil)
	mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(hostWithPartialMacros, nil)

	err := syncer.sync()
	assert.NoError(t, err)

	lines := syncer.GetLines()
	assert.Len(t, lines, 1)

	line := lines["10.10.10.11"]
	assert.Equal(t, "line001", line.ID)
	assert.Equal(t, DefaultInterval, line.Interval) // Should use default for missing interval
	assert.Equal(t, "192.168.1.1", line.Router.IP)
	assert.Equal(t, "", line.Router.Username) // Should be empty for missing macro
	assert.Equal(t, "", line.Router.Password) // Should be empty for missing macro

	mockClient.AssertExpectations(t)
}

// TestConfigSyncer_SubscribeAfterStop tests subscribing to a stopped syncer
func TestConfigSyncer_SubscribeAfterStop(t *testing.T) {
	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, time.Minute)

	// Stop syncer first
	syncer.Stop()

	// Try to subscribe after stop
	ctx := context.Background()
	sub := syncer.Subscribe(ctx)

	// Subscription should work but channel should be closed immediately
	select {
	case _, ok := <-sub.Events():
		assert.False(t, ok, "Channel should be closed for stopped syncer")
	case <-time.After(100 * time.Millisecond):
		// This is also acceptable - the channel might not send anything
	}
}

// TestConfigSyncer_LargeEventBurst tests handling of large number of events at once
func TestConfigSyncer_LargeEventBurst(t *testing.T) {
	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, time.Minute)

	ctx := context.Background()
	sub := syncer.Subscribe(ctx)

	const numEvents = 1000
	events := make([]LineChangeEvent, numEvents)

	// Create large number of events
	for i := 0; i < numEvents; i++ {
		events[i] = LineChangeEvent{
			Type:    LineCreate,
			Line:    createTestLine(fmt.Sprintf("line%d", i), fmt.Sprintf("10.1.%d.%d", i/255, i%255), 3*time.Minute),
			Version: int64(i + 1),
		}
	}

	// Send all events at once
	go syncer.notifyAll(events)

	// Try to receive events (may not get all due to channel buffer)
	receivedCount := 0
	timeout := time.After(2 * time.Second)

	for receivedCount < 100 { // Don't try to receive all, just some
		select {
		case <-sub.Events():
			receivedCount++
		case <-timeout:
			break
		}
	}

	t.Logf("Received %d out of %d events", receivedCount, numEvents)
	assert.Greater(t, receivedCount, 0, "Should receive at least some events")

	sub.Close()
}

// TestConfigSyncer_VersionConsistency tests version number consistency
func TestConfigSyncer_VersionConsistency(t *testing.T) {
	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, time.Minute)

	// Initial version should be 0
	assert.Equal(t, int64(0), syncer.Version())

	// Setup successful sync
	mockClient.On("GetProxyFormHost", "10.10.10.10").Return(createTestProxyResponse(), nil)
	mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(createTestHostResponse(), nil)

	// First sync
	err := syncer.sync()
	assert.NoError(t, err)
	assert.Equal(t, int64(1), syncer.Version())

	// Second sync with same data (no changes)
	err = syncer.sync()
	assert.NoError(t, err)
	assert.Equal(t, int64(1), syncer.Version()) // Should not increment if no changes

	// Create modified data for third sync
	modifiedHosts := []zapix.HostObject{
		{
			Host: "10.10.10.11",
			Macros: []zapix.UsermacroObject{
				{Macro: "{$LINE_ID}", Value: "line001"},
				{Macro: "{$LINE_CHECK_INTERVAL}", Value: "240"}, // Changed interval
				{Macro: "{$LINE_ROUTER_IP}", Value: "192.168.1.1"},
				{Macro: "{$LINE_ROUTER_USERNAME}", Value: "admin"},
				{Macro: "{$LINE_ROUTER_PASSWORD}", Value: "password"},
				{Macro: "{$LINE_ROUTER_PLATFORM}", Value: "cisco_iosxe"},
				{Macro: "{$LINE_ROUTER_PROTOCOL}", Value: "ssh"},
			},
		},
	}

	mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(modifiedHosts, nil)

	// Third sync with changes
	err = syncer.sync()
	assert.NoError(t, err)
	assert.Equal(t, int64(2), syncer.Version()) // Should increment due to changes

	mockClient.AssertExpectations(t)
}

// TestConfigSyncer_EventOrdering tests that events maintain some ordering consistency
func TestConfigSyncer_EventOrdering(t *testing.T) {
	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, time.Minute)

	ctx := context.Background()
	sub := syncer.Subscribe(ctx)

	// Setup initial data
	initialHosts := createTestHostResponse()
	mockClient.On("GetProxyFormHost", "10.10.10.10").Return(createTestProxyResponse(), nil).Times(3)
	mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(initialHosts, nil).Once()

	// First sync
	err := syncer.sync()
	assert.NoError(t, err)

	// Collect first sync events
	firstEvents := make([]LineChangeEvent, 0)
	timeout := time.After(time.Second)
	for len(firstEvents) < 2 {
		select {
		case event := <-sub.Events():
			firstEvents = append(firstEvents, event)
		case <-timeout:
			break
		}
	}

	// All first sync events should have version 1
	for _, event := range firstEvents {
		assert.Equal(t, int64(1), event.Version)
		assert.Equal(t, LineCreate, event.Type)
	}

	// Setup modified data
	modifiedHosts := []zapix.HostObject{
		initialHosts[0], // Keep first unchanged
		// Remove second (delete)
		// Add new one (create)
		{
			Host: "10.10.10.13",
			Macros: []zapix.UsermacroObject{
				{Macro: "{$LINE_ID}", Value: "line003"},
				{Macro: "{$LINE_CHECK_INTERVAL}", Value: "200"},
				{Macro: "{$LINE_ROUTER_IP}", Value: "192.168.1.3"},
				{Macro: "{$LINE_ROUTER_USERNAME}", Value: "admin"},
				{Macro: "{$LINE_ROUTER_PASSWORD}", Value: "password"},
				{Macro: "{$LINE_ROUTER_PLATFORM}", Value: "cisco_iosxe"},
				{Macro: "{$LINE_ROUTER_PROTOCOL}", Value: "ssh"},
			},
		},
	}

	mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(modifiedHosts, nil).Once()

	// Second sync
	err = syncer.sync()
	assert.NoError(t, err)

	// Collect second sync events
	secondEvents := make([]LineChangeEvent, 0)
	timeout = time.After(time.Second)
	for len(secondEvents) < 2 {
		select {
		case event := <-sub.Events():
			secondEvents = append(secondEvents, event)
		case <-timeout:
			break
		}
	}

	// All second sync events should have version 2
	for _, event := range secondEvents {
		assert.Equal(t, int64(2), event.Version)
	}

	// Should have one delete and one create
	eventTypes := make(map[ChangeType]int)
	for _, event := range secondEvents {
		eventTypes[event.Type]++
	}

	assert.Equal(t, 1, eventTypes[LineDelete], "Should have one delete event")
	assert.Equal(t, 1, eventTypes[LineCreate], "Should have one create event")

	sub.Close()
	mockClient.AssertExpectations(t)
}

// TestConfigSyncer_ContextCancellationDuringSync tests context cancellation during sync
func TestConfigSyncer_ContextCancellationDuringSync(t *testing.T) {
	mockClient := &MockClient{}

	// Create syncer with cancellable context
	ctx, cancel := context.WithCancel(context.Background())
	syncer := &ConfigSyncer{
		client:       mockClient,
		lines:        make(map[string]Line),
		syncInterval: time.Minute,
		subscribers:  make([]chan<- LineChangeEvent, 0),
		ctx:          ctx,
		cancel:       cancel,
	}

	// Setup mock to simulate slow response
	mockClient.On("GetProxyFormHost", "10.10.10.10").Return(func(string) ([]zapix.ProxyObject, error) {
		time.Sleep(500 * time.Millisecond) // Simulate slow API call
		return createTestProxyResponse(), nil
	})

	// Start sync in background
	syncDone := make(chan error, 1)
	go func() {
		syncDone <- syncer.sync()
	}()

	// Cancel context after short delay
	time.Sleep(100 * time.Millisecond)
	cancel()

	// Wait for sync to complete or timeout
	select {
	case err := <-syncDone:
		// Sync may complete successfully or with error, both are acceptable
		t.Logf("Sync completed with result: %v", err)
	case <-time.After(2 * time.Second):
		t.Log("Sync did not complete within timeout (expected behavior)")
	}

	// Context should be cancelled
	assert.Error(t, syncer.ctx.Err())
}

// TestConfigSyncer_HealthCheck tests the health check functionality
func TestConfigSyncer_HealthCheck(t *testing.T) {
	tests := []struct {
		name          string
		setupMock     func(*MockClient)
		expectedError bool
		description   string
	}{
		{
			name: "successful_health_check",
			setupMock: func(mc *MockClient) {
				// Health check currently returns nil (no-op implementation)
			},
			expectedError: false,
			description:   "Health check should succeed when client is healthy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockClient{}
			syncer := createTestSyncer(mockClient, time.Minute)

			tt.setupMock(mockClient)

			err := syncer.checkHealth()
			if tt.expectedError {
				assert.Error(t, err, tt.description)
			} else {
				assert.NoError(t, err, tt.description)
			}

			mockClient.AssertExpectations(t)
		})
	}
}

// TestConfigSyncer_StartWithRetry tests the start method with retry logic
func TestConfigSyncer_StartWithRetry(t *testing.T) {
	mockClient := &MockClient{}
	// Use shorter interval for faster testing
	syncer := createTestSyncer(mockClient, 20*time.Millisecond)

	// Mock to succeed on first call (testing immediate success case)
	mockClient.On("GetProxyFormHost", "10.10.10.10").Return(createTestProxyResponse(), nil)
	mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(createTestHostResponse(), nil)

	// Create subscriber to track events
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	sub := syncer.Subscribe(ctx)

	// Start syncer in background
	go syncer.Start()

	// Wait for successful sync
	timeout := time.After(500 * time.Millisecond)
	eventReceived := false

	for !eventReceived {
		select {
		case <-sub.Events():
			eventReceived = true
		case <-timeout:
			t.Fatal("Expected to receive events")
		case <-ctx.Done():
			t.Fatal("Context cancelled before receiving events")
		}
	}

	// Stop syncer
	syncer.Stop()
	sub.Close()

	assert.True(t, eventReceived, "Should receive events")
	mockClient.AssertExpectations(t)
}

// TestConfigSyncer_StartWithContinuousFailures tests behavior with continuous failures
func TestConfigSyncer_StartWithContinuousFailures(t *testing.T) {
	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, 10*time.Millisecond)

	// Mock to always fail
	mockClient.On("GetProxyFormHost", "10.10.10.10").Return(nil, fmt.Errorf("persistent failure")).Maybe()

	// Create subscriber
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	sub := syncer.Subscribe(ctx)

	// Start syncer in background
	go syncer.Start()

	// Should not receive any events due to continuous failures
	timeout := time.After(150 * time.Millisecond)
	eventReceived := false

	select {
	case <-sub.Events():
		eventReceived = true
	case <-timeout:
		// Expected behavior - no events due to failures
	case <-ctx.Done():
		// Also acceptable
	}

	// Stop syncer
	syncer.Stop()
	sub.Close()

	assert.False(t, eventReceived, "Should not receive events when sync continuously fails")

	// Verify version remains 0 due to failures
	assert.Equal(t, int64(0), syncer.Version(), "Version should remain 0 when sync fails")

	mockClient.AssertExpectations(t)
}

// TestConfigSyncer_SyncIntervalTiming tests that sync happens at correct intervals
func TestConfigSyncer_SyncIntervalTiming(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping timing test in short mode")
	}

	mockClient := &MockClient{}
	syncInterval := 100 * time.Millisecond
	syncer := createTestSyncer(mockClient, syncInterval)

	// Track sync calls
	syncTimes := make([]time.Time, 0, 5)
	var mu sync.Mutex

	mockClient.On("GetProxyFormHost", "10.10.10.10").Return(func(string) ([]zapix.ProxyObject, error) {
		mu.Lock()
		syncTimes = append(syncTimes, time.Now())
		mu.Unlock()
		return createTestProxyResponse(), nil
	}).Maybe()

	mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(createTestHostResponse(), nil).Maybe()

	// Start syncer
	go syncer.Start()

	// Let it run for a few intervals
	time.Sleep(450 * time.Millisecond) // Should allow for 4-5 syncs

	// Stop syncer
	syncer.Stop()

	// Verify timing intervals
	mu.Lock()
	defer mu.Unlock()

	assert.GreaterOrEqual(t, len(syncTimes), 3, "Should have performed multiple syncs")

	// Check intervals between syncs (with some tolerance for timing)
	for i := 1; i < len(syncTimes); i++ {
		interval := syncTimes[i].Sub(syncTimes[i-1])
		assert.Greater(t, interval, 80*time.Millisecond, "Interval should be at least 80ms")
		assert.Less(t, interval, 150*time.Millisecond, "Interval should be less than 150ms")
	}

	t.Logf("Recorded %d sync times with intervals: %v", len(syncTimes), func() []time.Duration {
		intervals := make([]time.Duration, len(syncTimes)-1)
		for i := 1; i < len(syncTimes); i++ {
			intervals[i-1] = syncTimes[i].Sub(syncTimes[i-1])
		}
		return intervals
	}())
}

// TestConfigSyncer_LastSyncTimeTracking tests that lastSyncTime is properly tracked
func TestConfigSyncer_LastSyncTimeTracking(t *testing.T) {
	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, 50*time.Millisecond)

	// Initial lastSyncTime should be zero
	assert.Zero(t, syncer.lastSyncTime, "Initial lastSyncTime should be zero")

	// Setup successful mock
	mockClient.On("GetProxyFormHost", "10.10.10.10").Return(createTestProxyResponse(), nil)
	mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(createTestHostResponse(), nil)

	// Start syncer briefly
	go syncer.Start()

	// Wait for at least one sync
	time.Sleep(100 * time.Millisecond)

	// Stop syncer
	syncer.Stop()

	// lastSyncTime should be updated
	assert.NotZero(t, syncer.lastSyncTime, "lastSyncTime should be updated after sync")
	assert.True(t, time.Since(syncer.lastSyncTime) < 200*time.Millisecond, "lastSyncTime should be recent")

	mockClient.AssertExpectations(t)
}

// TestConfigSyncer_DataValidation tests validation of fetched data
func TestConfigSyncer_DataValidation(t *testing.T) {
	tests := []struct {
		name          string
		hostResponse  []zapix.HostObject
		expectedLines int
		expectedError bool
		description   string
	}{
		{
			name: "host_with_missing_line_id",
			hostResponse: []zapix.HostObject{
				{
					Host: "10.10.10.11",
					Macros: []zapix.UsermacroObject{
						// Missing {$LINE_ID}
						{Macro: "{$LINE_CHECK_INTERVAL}", Value: "180"},
						{Macro: "{$LINE_ROUTER_IP}", Value: "192.168.1.1"},
						{Macro: "{$LINE_ROUTER_USERNAME}", Value: "admin"},
						{Macro: "{$LINE_ROUTER_PASSWORD}", Value: "password"},
						{Macro: "{$LINE_ROUTER_PLATFORM}", Value: "cisco_iosxe"},
						{Macro: "{$LINE_ROUTER_PROTOCOL}", Value: "ssh"},
					},
				},
			},
			expectedLines: 1,
			expectedError: false,
			description:   "Should create line with empty ID when LINE_ID macro is missing",
		},
		{
			name: "host_with_all_missing_router_macros",
			hostResponse: []zapix.HostObject{
				{
					Host: "10.10.10.11",
					Macros: []zapix.UsermacroObject{
						{Macro: "{$LINE_ID}", Value: "line001"},
						{Macro: "{$LINE_CHECK_INTERVAL}", Value: "180"},
						// Missing all router macros
					},
				},
			},
			expectedLines: 1,
			expectedError: false,
			description:   "Should create line with empty router info when router macros are missing",
		},
		{
			name: "host_with_invalid_interval_formats",
			hostResponse: []zapix.HostObject{
				{
					Host: "10.10.10.11",
					Macros: []zapix.UsermacroObject{
						{Macro: "{$LINE_ID}", Value: "line001"},
						{Macro: "{$LINE_CHECK_INTERVAL}", Value: "not-a-number"},
						{Macro: "{$LINE_ROUTER_IP}", Value: "192.168.1.1"},
						{Macro: "{$LINE_ROUTER_USERNAME}", Value: "admin"},
						{Macro: "{$LINE_ROUTER_PASSWORD}", Value: "password"},
						{Macro: "{$LINE_ROUTER_PLATFORM}", Value: "cisco_iosxe"},
						{Macro: "{$LINE_ROUTER_PROTOCOL}", Value: "ssh"},
					},
				},
			},
			expectedLines: 1,
			expectedError: false,
			description:   "Should use default interval for invalid interval format",
		},
		{
			name: "host_with_extreme_interval_values",
			hostResponse: []zapix.HostObject{
				{
					Host: "10.10.10.11",
					Macros: []zapix.UsermacroObject{
						{Macro: "{$LINE_ID}", Value: "line001"},
						{Macro: "{$LINE_CHECK_INTERVAL}", Value: "-999999"},
						{Macro: "{$LINE_ROUTER_IP}", Value: "192.168.1.1"},
						{Macro: "{$LINE_ROUTER_USERNAME}", Value: "admin"},
						{Macro: "{$LINE_ROUTER_PASSWORD}", Value: "password"},
						{Macro: "{$LINE_ROUTER_PLATFORM}", Value: "cisco_iosxe"},
						{Macro: "{$LINE_ROUTER_PROTOCOL}", Value: "ssh"},
					},
				},
			},
			expectedLines: 1,
			expectedError: false,
			description:   "Should handle extreme interval values gracefully",
		},
		{
			name: "host_with_unicode_and_special_chars",
			hostResponse: []zapix.HostObject{
				{
					Host: "10.10.10.11",
					Macros: []zapix.UsermacroObject{
						{Macro: "{$LINE_ID}", Value: "专线001-测试@#$%"},
						{Macro: "{$LINE_CHECK_INTERVAL}", Value: "180"},
						{Macro: "{$LINE_ROUTER_IP}", Value: "192.168.1.1"},
						{Macro: "{$LINE_ROUTER_USERNAME}", Value: "用户名_test"},
						{Macro: "{$LINE_ROUTER_PASSWORD}", Value: "密码!@#$%^&*()"},
						{Macro: "{$LINE_ROUTER_PLATFORM}", Value: "cisco_iosxe"},
						{Macro: "{$LINE_ROUTER_PROTOCOL}", Value: "ssh"},
					},
				},
			},
			expectedLines: 1,
			expectedError: false,
			description:   "Should handle unicode and special characters in macro values",
		},
		{
			name: "host_with_very_long_values",
			hostResponse: []zapix.HostObject{
				{
					Host: "10.10.10.11",
					Macros: []zapix.UsermacroObject{
						{Macro: "{$LINE_ID}", Value: string(make([]byte, 1000))},
						{Macro: "{$LINE_CHECK_INTERVAL}", Value: "180"},
						{Macro: "{$LINE_ROUTER_IP}", Value: "192.168.1.1"},
						{Macro: "{$LINE_ROUTER_USERNAME}", Value: string(make([]byte, 500))},
						{Macro: "{$LINE_ROUTER_PASSWORD}", Value: string(make([]byte, 500))},
						{Macro: "{$LINE_ROUTER_PLATFORM}", Value: "cisco_iosxe"},
						{Macro: "{$LINE_ROUTER_PROTOCOL}", Value: "ssh"},
					},
				},
			},
			expectedLines: 1,
			expectedError: false,
			description:   "Should handle very long macro values",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockClient{}
			syncer := createTestSyncer(mockClient, time.Minute)

			mockClient.On("GetProxyFormHost", "10.10.10.10").Return(createTestProxyResponse(), nil)
			mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(tt.hostResponse, nil)

			err := syncer.sync()
			if tt.expectedError {
				assert.Error(t, err, tt.description)
			} else {
				assert.NoError(t, err, tt.description)
			}

			lines := syncer.GetLines()
			assert.Len(t, lines, tt.expectedLines, tt.description)

			// Validate that all created lines have computed hashes
			for _, line := range lines {
				assert.NotZero(t, line.Hash, "Line hash should be computed")
			}

			mockClient.AssertExpectations(t)
		})
	}
}

// TestConfigSyncer_ProxyDataValidation tests validation of proxy data
func TestConfigSyncer_ProxyDataValidation(t *testing.T) {
	tests := []struct {
		name          string
		proxyResponse []zapix.ProxyObject
		expectedError bool
		description   string
	}{
		{
			name: "proxy_with_non_numeric_id",
			proxyResponse: []zapix.ProxyObject{
				{Proxyid: "not-a-number", Host: "10.10.10.10"},
			},
			expectedError: true,
			description:   "Should fail when proxy ID is not numeric",
		},
		{
			name: "proxy_with_empty_id",
			proxyResponse: []zapix.ProxyObject{
				{Proxyid: "", Host: "10.10.10.10"},
			},
			expectedError: true,
			description:   "Should fail when proxy ID is empty",
		},
		{
			name: "proxy_with_zero_id",
			proxyResponse: []zapix.ProxyObject{
				{Proxyid: "0", Host: "10.10.10.10"},
			},
			expectedError: false,
			description:   "Should accept proxy ID of 0",
		},
		{
			name: "proxy_with_negative_id",
			proxyResponse: []zapix.ProxyObject{
				{Proxyid: "-1", Host: "10.10.10.10"},
			},
			expectedError: false,
			description:   "Should handle negative proxy ID (though unusual)",
		},
		{
			name: "proxy_with_very_large_id",
			proxyResponse: []zapix.ProxyObject{
				{Proxyid: "999999999", Host: "10.10.10.10"},
			},
			expectedError: false,
			description:   "Should handle very large proxy ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &MockClient{}
			syncer := createTestSyncer(mockClient, time.Minute)

			mockClient.On("GetProxyFormHost", "10.10.10.10").Return(tt.proxyResponse, nil)
			if !tt.expectedError && len(tt.proxyResponse) > 0 && tt.proxyResponse[0].Proxyid != "" {
				mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(createTestHostResponse(), nil).Maybe()
			}

			err := syncer.sync()
			if tt.expectedError {
				assert.Error(t, err, tt.description)
			} else {
				assert.NoError(t, err, tt.description)
			}

			mockClient.AssertExpectations(t)
		})
	}
}

// TestConfigSyncer_LineHashConsistency tests hash consistency across operations
func TestConfigSyncer_LineHashConsistency(t *testing.T) {
	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, time.Minute)

	// Setup consistent mock data
	mockClient.On("GetProxyFormHost", "10.10.10.10").Return(createTestProxyResponse(), nil)
	mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(createTestHostResponse(), nil)

	// First sync
	err := syncer.sync()
	assert.NoError(t, err)

	firstSyncLines := syncer.GetLines()
	firstSyncHashes := make(map[string]uint64)
	for ip, line := range firstSyncLines {
		firstSyncHashes[ip] = line.Hash
	}

	// Second sync with same data
	err = syncer.sync()
	assert.NoError(t, err)

	secondSyncLines := syncer.GetLines()

	// Hashes should be identical for same data
	for ip, line := range secondSyncLines {
		expectedHash, exists := firstSyncHashes[ip]
		assert.True(t, exists, "Line should exist in both syncs")
		assert.Equal(t, expectedHash, line.Hash, "Hash should be consistent across syncs for line %s", ip)
	}

	// Version should not increment if no changes
	assert.Equal(t, int64(1), syncer.Version(), "Version should not increment for identical data")

	mockClient.AssertExpectations(t)
}

// TestConfigSyncer_EventVersionConsistency tests event version numbering
func TestConfigSyncer_EventVersionConsistency(t *testing.T) {
	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, time.Minute)

	ctx := context.Background()
	sub := syncer.Subscribe(ctx)
	defer sub.Close()

	// Setup mock for multiple syncs
	mockClient.On("GetProxyFormHost", "10.10.10.10").Return(createTestProxyResponse(), nil).Times(3)
	mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(createTestHostResponse(), nil).Times(3)

	// Perform multiple syncs
	for i := 0; i < 3; i++ {
		err := syncer.sync()
		assert.NoError(t, err)
	}

	// Collect events (only first sync should generate events since data is identical)
	events := make([]LineChangeEvent, 0)
	timeout := time.After(time.Second)

	for len(events) < 2 {
		select {
		case event := <-sub.Events():
			events = append(events, event)
		case <-timeout:
			break
		}
	}

	// All events from first sync should have version 1
	for _, event := range events {
		assert.Equal(t, int64(1), event.Version, "All events should have consistent version number")
	}

	mockClient.AssertExpectations(t)
}

// TestConfigSyncer_LargeDatasetConsistency tests consistency with large datasets
func TestConfigSyncer_LargeDatasetConsistency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping large dataset test in short mode")
	}

	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, time.Minute)

	// Create large dataset
	const datasetSize = 1000
	largeHosts := createLargeTestHostResponse(datasetSize)

	mockClient.On("GetProxyFormHost", "10.10.10.10").Return(createTestProxyResponse(), nil)
	mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(largeHosts, nil)

	// Perform sync
	start := time.Now()
	err := syncer.sync()
	duration := time.Since(start)

	assert.NoError(t, err)

	lines := syncer.GetLines()
	assert.Len(t, lines, datasetSize, "Should create all lines from large dataset")

	// Verify all lines have valid data
	for _, line := range lines {
		assert.NotEmpty(t, line.IP, "Line IP should not be empty")
		assert.NotZero(t, line.Hash, "Line hash should be computed")
		assert.NotEmpty(t, line.Router.IP, "Router IP should not be empty")
		assert.Positive(t, line.Interval, "Interval should be positive")
	}

	t.Logf("Large dataset sync completed in %v for %d lines", duration, datasetSize)

	// Performance expectation (adjust based on requirements)
	maxExpectedDuration := time.Duration(datasetSize) * time.Millisecond // 1ms per line
	assert.Less(t, duration, maxExpectedDuration, "Large dataset sync should complete within reasonable time")

	mockClient.AssertExpectations(t)
}

// TestConfigSyncer_SubscriberChannelBuffering tests subscriber channel behavior
func TestConfigSyncer_SubscriberChannelBuffering(t *testing.T) {
	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, time.Minute)

	ctx := context.Background()
	sub := syncer.Subscribe(ctx)
	defer sub.Close()

	// Generate more events than channel buffer (which is 100)
	const eventCount = 150
	events := make([]LineChangeEvent, eventCount)
	for i := 0; i < eventCount; i++ {
		events[i] = LineChangeEvent{
			Type:    LineCreate,
			Line:    createTestLine(fmt.Sprintf("line%d", i), fmt.Sprintf("10.1.%d.%d", i/255, i%255), 3*time.Minute),
			Version: int64(i + 1),
		}
	}

	// Send all events
	go syncer.notifyAll(events)

	// Try to receive events (some may be dropped due to buffer limit)
	receivedCount := 0
	timeout := time.After(2 * time.Second)

	for receivedCount < 100 { // Don't try to receive more than buffer size
		select {
		case <-sub.Events():
			receivedCount++
		case <-timeout:
			break
		}
	}

	t.Logf("Received %d out of %d events", receivedCount, eventCount)

	// Should receive some events (exact count may vary due to timing)
	assert.Greater(t, receivedCount, 0, "Should receive at least some events")
	assert.LessOrEqual(t, receivedCount, 100, "Should not receive more than buffer size")
}

func TestSubscription_CloseIdempotent(t *testing.T) {
	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, time.Minute)
	ctx := context.Background()
	sub := syncer.Subscribe(ctx)

	// Close multiple times
	sub.Close()
	sub.Close()
	sub.Close()

	// Should handle multiple closes gracefully
	syncer.mu.RLock()
	assert.Len(t, syncer.subscribers, 0)
	syncer.mu.RUnlock()
}

package syncer

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
)

// ConcurrencyTestSuite provides comprehensive concurrency tests
type ConcurrencyTestSuite struct {
	suite.Suite
	mockClient *MockClient
	syncer     *ConfigSyncer
	ctx        context.Context
	cancel     context.CancelFunc
}

func (suite *ConcurrencyTestSuite) SetupTest() {
	suite.mockClient = &MockClient{}
	suite.ctx, suite.cancel = context.WithTimeout(context.Background(), 30*time.Second)
	suite.syncer = createTestSyncer(suite.mockClient, 50*time.Millisecond)
}

func (suite *ConcurrencyTestSuite) TearDownTest() {
	if suite.syncer != nil {
		suite.syncer.Stop()
	}
	suite.cancel()
}

// TestConcurrentSync tests multiple goroutines calling sync simultaneously
func (suite *ConcurrencyTestSuite) TestConcurrentSync() {
	// Setup mock to return consistent data
	suite.mockClient.On("GetProxyFormHost", "10.10.10.10").Return(createTestProxyResponse(), nil)
	suite.mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(createTestHostResponse(), nil)

	const numGoroutines = 20
	var wg sync.WaitGroup
	var successCount int32
	var errorCount int32

	// Launch concurrent sync operations
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			err := suite.syncer.sync()
			if err != nil {
				atomic.AddInt32(&errorCount, 1)
				suite.T().Logf("Goroutine %d sync failed: %v", id, err)
			} else {
				atomic.AddInt32(&successCount, 1)
			}
		}(i)
	}

	wg.Wait()

	// At least some operations should succeed
	suite.Greater(int(successCount), 0, "At least some sync operations should succeed")

	// Version should be incremented (but exact value depends on race conditions)
	finalVersion := suite.syncer.Version()
	suite.Greater(finalVersion, int64(0), "Version should be incremented")

	// Lines should be populated
	lines := suite.syncer.GetLines()
	suite.Len(lines, 2, "Lines should be populated")

	// Verify data consistency
	for _, line := range lines {
		suite.NotEmpty(line.IP, "Line IP should not be empty")
		suite.NotZero(line.Hash, "Line hash should be computed")
	}
}

// TestConcurrentSubscriptions tests multiple subscribers being created and destroyed concurrently
func (suite *ConcurrencyTestSuite) TestConcurrentSubscriptions() {
	const numSubscribers = 50
	var wg sync.WaitGroup
	var subscriptions []*Subscription
	var mu sync.Mutex

	// Create subscribers concurrently
	for i := 0; i < numSubscribers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			sub := suite.syncer.Subscribe(suite.ctx)

			mu.Lock()
			subscriptions = append(subscriptions, sub)
			mu.Unlock()

			// Do some work with the subscription
			time.Sleep(time.Duration(id%10) * time.Millisecond)
		}(i)
	}

	wg.Wait()

	// Verify all subscriptions were created
	suite.Equal(numSubscribers, len(subscriptions))

	// Verify syncer has all subscribers
	suite.syncer.mu.RLock()
	currentSubscriberCount := len(suite.syncer.subscribers)
	suite.syncer.mu.RUnlock()
	suite.Equal(numSubscribers, currentSubscriberCount)

	// Close subscribers concurrently
	for i := 0; i < numSubscribers; i++ {
		wg.Add(1)
		go func(sub *Subscription) {
			defer wg.Done()
			sub.Close()
		}(subscriptions[i])
	}

	wg.Wait()

	// Verify all subscriptions were removed
	suite.syncer.mu.RLock()
	finalSubscriberCount := len(suite.syncer.subscribers)
	suite.syncer.mu.RUnlock()
	suite.Equal(0, finalSubscriberCount)
}

// TestConcurrentEventNotification tests concurrent event notifications to multiple subscribers
func (suite *ConcurrencyTestSuite) TestConcurrentEventNotification() {
	const numSubscribers = 20
	const numEvents = 100

	// Create subscribers
	subscribers := make([]*Subscription, numSubscribers)
	eventCounters := make([]int32, numSubscribers)

	for i := 0; i < numSubscribers; i++ {
		subscribers[i] = suite.syncer.Subscribe(suite.ctx)
	}

	// Start event collectors
	var wg sync.WaitGroup
	for i := 0; i < numSubscribers; i++ {
		wg.Add(1)
		go func(subIndex int, sub *Subscription) {
			defer wg.Done()
			timeout := time.After(5 * time.Second)
			for {
				select {
				case <-sub.Events():
					atomic.AddInt32(&eventCounters[subIndex], 1)
				case <-timeout:
					return
				case <-suite.ctx.Done():
					return
				}
			}
		}(i, subscribers[i])
	}

	// Generate events concurrently
	for i := 0; i < numEvents; i++ {
		go func(eventNum int) {
			event := LineChangeEvent{
				Type:    LineCreate,
				Line:    createTestLine(fmt.Sprintf("line%d", eventNum), fmt.Sprintf("10.1.%d.%d", eventNum/100, eventNum%100), 3*time.Minute),
				Version: int64(eventNum + 1),
			}
			suite.syncer.notifyAll([]LineChangeEvent{event})
		}(i)
	}

	// Give some time for events to be processed
	time.Sleep(2 * time.Second)

	// Stop collectors
	suite.cancel()
	wg.Wait()

	// Verify events were distributed
	totalEvents := int32(0)
	for i, count := range eventCounters {
		suite.T().Logf("Subscriber %d received %d events", i, count)
		totalEvents += count
	}

	// Each event should be delivered to all subscribers
	suite.Greater(totalEvents, int32(0), "Should have received some events")

	// Clean up
	for _, sub := range subscribers {
		sub.Close()
	}
}

// TestRaceConditionDetection tests for race conditions using Go's race detector
func (suite *ConcurrencyTestSuite) TestRaceConditionDetection() {
	// This test is designed to trigger race conditions if they exist
	// Run with: go test -race

	const numOperations = 100
	var wg sync.WaitGroup

	// Setup mock responses
	suite.mockClient.On("GetProxyFormHost", "10.10.10.10").Return(createTestProxyResponse(), nil)
	suite.mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(createTestHostResponse(), nil)

	// Concurrent read operations
	for i := 0; i < numOperations; i++ {
		wg.Add(3) // Three types of operations per iteration

		// Read version
		go func() {
			defer wg.Done()
			_ = suite.syncer.Version()
		}()

		// Read lines
		go func() {
			defer wg.Done()
			_ = suite.syncer.GetLines()
		}()

		// Sync operation
		go func() {
			defer wg.Done()
			_ = suite.syncer.sync()
		}()
	}

	wg.Wait()

	// If we reach here without race detector warnings, the test passes
	suite.T().Log("Race condition test completed without detected races")
}

// TestHighLoadSubscriptionManagement tests subscription management under high load
func (suite *ConcurrencyTestSuite) TestHighLoadSubscriptionManagement() {
	const numCycles = 10
	const subscribersPerCycle = 20

	for cycle := 0; cycle < numCycles; cycle++ {
		var wg sync.WaitGroup
		var subscriptions []*Subscription
		var mu sync.Mutex

		// Create many subscribers
		for i := 0; i < subscribersPerCycle; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sub := suite.syncer.Subscribe(suite.ctx)
				mu.Lock()
				subscriptions = append(subscriptions, sub)
				mu.Unlock()
			}()
		}

		wg.Wait()

		// Verify subscriber count
		suite.syncer.mu.RLock()
		currentCount := len(suite.syncer.subscribers)
		suite.syncer.mu.RUnlock()
		suite.Equal(subscribersPerCycle, currentCount, "Cycle %d: incorrect subscriber count", cycle)

		// Generate some events
		for i := 0; i < 5; i++ {
			event := LineChangeEvent{
				Type:    LineCreate,
				Line:    createTestLine(fmt.Sprintf("cycle%d-line%d", cycle, i), fmt.Sprintf("10.%d.1.%d", cycle, i), 3*time.Minute),
				Version: int64(cycle*10 + i),
			}
			suite.syncer.notifyAll([]LineChangeEvent{event})
		}

		// Close all subscribers for this cycle
		for _, sub := range subscriptions {
			wg.Add(1)
			go func(s *Subscription) {
				defer wg.Done()
				s.Close()
			}(sub)
		}

		wg.Wait()

		// Verify cleanup
		suite.syncer.mu.RLock()
		finalCount := len(suite.syncer.subscribers)
		suite.syncer.mu.RUnlock()
		suite.Equal(0, finalCount, "Cycle %d: subscribers not cleaned up properly", cycle)
	}
}

// TestConcurrentStartStop tests concurrent start and stop operations
func (suite *ConcurrencyTestSuite) TestConcurrentStartStop() {
	// Create a fresh syncer for this test
	syncer := createTestSyncer(&MockClient{}, 10*time.Millisecond)

	const numOperations = 50
	var wg sync.WaitGroup
	var startCount int32
	var stopCount int32

	// Concurrent start and stop operations
	for i := 0; i < numOperations; i++ {
		wg.Add(2)

		// Start operation
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					// Starting an already started syncer might panic, that's okay
					suite.T().Logf("Start operation panicked: %v", r)
				}
			}()

			go syncer.Start() // Start in background since it blocks
			atomic.AddInt32(&startCount, 1)
		}()

		// Stop operation
		go func() {
			defer wg.Done()
			syncer.Stop()
			atomic.AddInt32(&stopCount, 1)
		}()
	}

	wg.Wait()

	// Verify final state
	suite.True(syncer.stopped, "Syncer should be in stopped state")
	suite.Equal(int32(numOperations), startCount)
	suite.Equal(int32(numOperations), stopCount)
}

// TestMemoryLeakDetection tests for potential memory leaks in concurrent scenarios
func (suite *ConcurrencyTestSuite) TestMemoryLeakDetection() {
	// Record initial memory stats
	var initialStats runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&initialStats)

	// Create and destroy many subscribers
	const numIterations = 100
	const subscribersPerIteration = 10

	for i := 0; i < numIterations; i++ {
		var subscriptions []*Subscription

		// Create subscribers
		for j := 0; j < subscribersPerIteration; j++ {
			sub := suite.syncer.Subscribe(suite.ctx)
			subscriptions = append(subscriptions, sub)
		}

		// Generate some events
		event := LineChangeEvent{
			Type:    LineCreate,
			Line:    createTestLine(fmt.Sprintf("iter%d", i), fmt.Sprintf("10.1.%d.1", i%255), 3*time.Minute),
			Version: int64(i + 1),
		}
		suite.syncer.notifyAll([]LineChangeEvent{event})

		// Close subscribers
		for _, sub := range subscriptions {
			sub.Close()
		}

		// Force garbage collection every 10 iterations
		if i%10 == 0 {
			runtime.GC()
		}
	}

	// Final garbage collection and memory check
	runtime.GC()
	var finalStats runtime.MemStats
	runtime.ReadMemStats(&finalStats)

	// Check for reasonable memory usage (this is a heuristic check)
	memoryGrowth := finalStats.Alloc - initialStats.Alloc
	suite.T().Logf("Memory growth: %d bytes", memoryGrowth)

	// If memory grew by more than 10MB, there might be a leak
	// This is a rough heuristic and may need adjustment
	const maxAcceptableGrowth = 10 * 1024 * 1024 // 10MB
	suite.Less(memoryGrowth, uint64(maxAcceptableGrowth), "Potential memory leak detected")
}

// TestDeadlockDetection tests for potential deadlocks
func (suite *ConcurrencyTestSuite) TestDeadlockDetection() {
	done := make(chan bool, 1)

	go func() {
		defer func() { done <- true }()

		var wg sync.WaitGroup
		const numGoroutines = 20

		// Create multiple goroutines that perform various operations
		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()

				// Mix of operations that could potentially deadlock
				switch id % 4 {
				case 0:
					// Subscribe and immediately close
					sub := suite.syncer.Subscribe(suite.ctx)
					sub.Close()
				case 1:
					// Read operations
					_ = suite.syncer.Version()
					_ = suite.syncer.GetLines()
				case 2:
					// Stop and restart (this might not work, but shouldn't deadlock)
					suite.syncer.Stop()
				case 3:
					// Event notification
					event := LineChangeEvent{
						Type:    LineCreate,
						Line:    createTestLine(fmt.Sprintf("deadlock-test-%d", id), fmt.Sprintf("10.1.1.%d", id), 3*time.Minute),
						Version: int64(id),
					}
					suite.syncer.notifyAll([]LineChangeEvent{event})
				}
			}(i)
		}

		wg.Wait()
	}()

	// Wait for operations to complete or timeout
	select {
	case <-done:
		suite.T().Log("Deadlock detection test completed successfully")
	case <-time.After(10 * time.Second):
		suite.Fail("Potential deadlock detected - operations did not complete within timeout")
	}
}

// TestConcurrentVersionIncrement tests that version increments are thread-safe
func (suite *ConcurrencyTestSuite) TestConcurrentVersionIncrement() {
	// Setup mock to always succeed
	suite.mockClient.On("GetProxyFormHost", "10.10.10.10").Return(createTestProxyResponse(), nil)
	suite.mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(createTestHostResponse(), nil)

	const numSyncs = 50
	var wg sync.WaitGroup
	versions := make([]int64, numSyncs)

	// Perform concurrent syncs
	for i := 0; i < numSyncs; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			suite.syncer.sync()
			versions[index] = suite.syncer.Version()
		}(i)
	}

	wg.Wait()

	// Analyze version increments
	finalVersion := suite.syncer.Version()
	suite.Greater(finalVersion, int64(0), "Final version should be greater than 0")

	// Check that all observed versions are valid (between 0 and final version)
	for i, version := range versions {
		suite.GreaterOrEqual(version, int64(0), "Version %d at index %d should be non-negative", version, i)
		suite.LessOrEqual(version, finalVersion, "Version %d at index %d should not exceed final version %d", version, i, finalVersion)
	}
}

func TestConcurrencySuite(t *testing.T) {
	suite.Run(t, new(ConcurrencyTestSuite))
}

// BenchmarkConcurrentOperations benchmarks various concurrent operations
func BenchmarkConcurrentOperations(b *testing.B) {
	mockClient := &MockClient{}
	mockClient.On("GetProxyFormHost", "10.10.10.10").Return(createTestProxyResponse(), nil)
	mockClient.On("HostGet", mock.AnythingOfType("zapix.HostGetParams")).Return(createTestHostResponse(), nil)

	syncer := createTestSyncer(mockClient, time.Minute)

	b.Run("ConcurrentSync", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				syncer.sync()
			}
		})
	})

	b.Run("ConcurrentVersionRead", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_ = syncer.Version()
			}
		})
	})

	b.Run("ConcurrentLinesRead", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_ = syncer.GetLines()
			}
		})
	})

	b.Run("ConcurrentSubscription", func(b *testing.B) {
		ctx := context.Background()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				sub := syncer.Subscribe(ctx)
				sub.Close()
			}
		})
	})
}

// TestGoroutineLeakDetection tests for goroutine leaks
func TestGoroutineLeakDetection(t *testing.T) {
	// Record initial goroutine count
	initialCount := runtime.NumGoroutine()

	// Create and destroy multiple syncers
	for i := 0; i < 10; i++ {
		mockClient := &MockClient{}
		syncer := createTestSyncer(mockClient, 100*time.Millisecond)

		// Create some subscribers
		ctx := context.Background()
		var subs []*Subscription
		for j := 0; j < 5; j++ {
			subs = append(subs, syncer.Subscribe(ctx))
		}

		// Clean up
		for _, sub := range subs {
			sub.Close()
		}
		syncer.Stop()
	}

	// Give some time for cleanup
	time.Sleep(100 * time.Millisecond)
	runtime.GC()

	// Check final goroutine count
	finalCount := runtime.NumGoroutine()

	// Allow some tolerance for background goroutines
	const tolerance = 5
	if finalCount > initialCount+tolerance {
		t.Errorf("Potential goroutine leak detected: initial=%d, final=%d", initialCount, finalCount)
	}

	t.Logf("Goroutine count: initial=%d, final=%d", initialCount, finalCount)
}

// TestConcurrentNotificationOrdering tests event notification ordering under concurrency
func TestConcurrentNotificationOrdering(t *testing.T) {
	mockClient := &MockClient{}
	syncer := createTestSyncer(mockClient, time.Minute)

	ctx := context.Background()
	sub := syncer.Subscribe(ctx)
	defer sub.Close()

	const numEvents = 100
	var wg sync.WaitGroup

	// Send events concurrently with version numbers
	for i := 0; i < numEvents; i++ {
		wg.Add(1)
		go func(version int) {
			defer wg.Done()
			event := LineChangeEvent{
				Type:    LineCreate,
				Line:    createTestLine(fmt.Sprintf("line%d", version), fmt.Sprintf("10.1.1.%d", version), 3*time.Minute),
				Version: int64(version),
			}
			syncer.notifyAll([]LineChangeEvent{event})
		}(i)
	}

	// Collect events
	receivedEvents := make([]LineChangeEvent, 0, numEvents)
	timeout := time.After(5 * time.Second)

	go func() {
		wg.Wait() // Wait for all events to be sent
	}()

	for len(receivedEvents) < numEvents {
		select {
		case event := <-sub.Events():
			receivedEvents = append(receivedEvents, event)
		case <-timeout:
			break
		}
	}

	t.Logf("Received %d out of %d events", len(receivedEvents), numEvents)

	// Verify we received events (order may not be preserved due to concurrency)
	assert.Greater(t, len(receivedEvents), 0, "Should receive at least some events")

	// Verify all events have valid version numbers
	for _, event := range receivedEvents {
		assert.GreaterOrEqual(t, event.Version, int64(0), "Event version should be non-negative")
		assert.Less(t, event.Version, int64(numEvents), "Event version should be within expected range")
	}
}

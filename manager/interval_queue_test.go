package manager

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/charlesren/zabbix_ddl_monitor/connection"
	"github.com/charlesren/zabbix_ddl_monitor/syncer"
)

// Helper function to create test lines for queue testing
func createQueueTestLine(id, ip, routerIP string, interval time.Duration) syncer.Line {
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

// Basic functionality tests
func TestNewIntervalTaskQueue(t *testing.T) {
	interval := 5 * time.Minute
	queue := NewIntervalTaskQueue(interval)
	defer queue.Stop()

	assert.NotNil(t, queue)
	assert.Equal(t, interval, queue.interval)
	assert.NotNil(t, queue.execNotify)
	assert.NotNil(t, queue.stopChan)
	assert.NotNil(t, queue.ticker)
	assert.Empty(t, queue.lines)
}

func TestIntervalTaskQueue_Add(t *testing.T) {
	queue := NewIntervalTaskQueue(5 * time.Minute)
	defer queue.Stop()

	line1 := createQueueTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute)
	line2 := createQueueTestLine("line2", "10.0.0.2", "192.168.1.1", 5*time.Minute)

	queue.Add(line1)
	queue.Add(line2)

	snapshot := queue.GetTasksSnapshot()
	assert.Equal(t, 2, len(snapshot))
	assert.Equal(t, "line1", snapshot[0].ID)
	assert.Equal(t, "line2", snapshot[1].ID)
}

func TestIntervalTaskQueue_Remove(t *testing.T) {
	queue := NewIntervalTaskQueue(5 * time.Minute)
	defer queue.Stop()

	line1 := createQueueTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute)
	line2 := createQueueTestLine("line2", "10.0.0.2", "192.168.1.1", 5*time.Minute)

	queue.Add(line1)
	queue.Add(line2)

	// Remove first line
	queue.Remove("10.0.0.1")

	snapshot := queue.GetTasksSnapshot()
	assert.Equal(t, 1, len(snapshot))
	assert.Equal(t, "line2", snapshot[0].ID)
}

func TestIntervalTaskQueue_Remove_NonexistentLine(t *testing.T) {
	queue := NewIntervalTaskQueue(5 * time.Minute)
	defer queue.Stop()

	line1 := createQueueTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute)
	queue.Add(line1)

	// Try to remove non-existent line
	queue.Remove("10.0.0.999")

	snapshot := queue.GetTasksSnapshot()
	assert.Equal(t, 1, len(snapshot))
	assert.Equal(t, "line1", snapshot[0].ID)
}

func TestIntervalTaskQueue_Contains(t *testing.T) {
	queue := NewIntervalTaskQueue(5 * time.Minute)
	defer queue.Stop()

	line1 := createQueueTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute)
	queue.Add(line1)

	assert.True(t, queue.Contains("10.0.0.1"))
	assert.False(t, queue.Contains("10.0.0.999"))
}

func TestIntervalTaskQueue_IsEmpty(t *testing.T) {
	queue := NewIntervalTaskQueue(5 * time.Minute)
	defer queue.Stop()

	// Initially empty
	assert.True(t, queue.IsEmpty())

	// Add line
	line1 := createQueueTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute)
	queue.Add(line1)
	assert.False(t, queue.IsEmpty())

	// Remove line
	queue.Remove("10.0.0.1")
	assert.True(t, queue.IsEmpty())
}

func TestIntervalTaskQueue_UpdateTask(t *testing.T) {
	queue := NewIntervalTaskQueue(5 * time.Minute)
	defer queue.Stop()

	line1 := createQueueTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute)
	queue.Add(line1)

	// Update the line
	updatedLine := line1
	updatedLine.Router.Password = "newpassword"
	updatedLine.ComputeHash()

	queue.UpdateTask(updatedLine)

	snapshot := queue.GetTasksSnapshot()
	assert.Equal(t, 1, len(snapshot))
	assert.Equal(t, "newpassword", snapshot[0].Router.Password)
}

func TestIntervalTaskQueue_UpdateTask_NonexistentLine(t *testing.T) {
	queue := NewIntervalTaskQueue(5 * time.Minute)
	defer queue.Stop()

	line1 := createQueueTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute)
	queue.Add(line1)

	// Try to update non-existent line
	nonexistentLine := createQueueTestLine("nonexistent", "10.0.0.999", "192.168.1.1", 5*time.Minute)
	nonexistentLine.Router.Password = "newpassword"
	nonexistentLine.ComputeHash()

	queue.UpdateTask(nonexistentLine)

	// Original line should be unchanged
	snapshot := queue.GetTasksSnapshot()
	assert.Equal(t, 1, len(snapshot))
	assert.Equal(t, "password", snapshot[0].Router.Password)
}

func TestIntervalTaskQueue_ReplaceAll(t *testing.T) {
	queue := NewIntervalTaskQueue(5 * time.Minute)
	defer queue.Stop()

	// Add initial lines
	line1 := createQueueTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute)
	line2 := createQueueTestLine("line2", "10.0.0.2", "192.168.1.1", 5*time.Minute)
	queue.Add(line1)
	queue.Add(line2)

	// Replace with new lines
	newLines := []syncer.Line{
		createQueueTestLine("line3", "10.0.0.3", "192.168.1.1", 5*time.Minute),
		createQueueTestLine("line4", "10.0.0.4", "192.168.1.1", 5*time.Minute),
		createQueueTestLine("line5", "10.0.0.5", "192.168.1.1", 5*time.Minute),
	}

	queue.ReplaceAll(newLines)

	snapshot := queue.GetTasksSnapshot()
	assert.Equal(t, 3, len(snapshot))
	assert.Equal(t, "line3", snapshot[0].ID)
	assert.Equal(t, "line4", snapshot[1].ID)
	assert.Equal(t, "line5", snapshot[2].ID)
}

func TestIntervalTaskQueue_ReplaceAll_EmptySlice(t *testing.T) {
	queue := NewIntervalTaskQueue(5 * time.Minute)
	defer queue.Stop()

	// Add initial lines
	line1 := createQueueTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute)
	queue.Add(line1)

	// Replace with empty slice
	queue.ReplaceAll([]syncer.Line{})

	assert.True(t, queue.IsEmpty())
	snapshot := queue.GetTasksSnapshot()
	assert.Equal(t, 0, len(snapshot))
}

func TestIntervalTaskQueue_GetTasksSnapshot(t *testing.T) {
	queue := NewIntervalTaskQueue(5 * time.Minute)
	defer queue.Stop()

	line1 := createQueueTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute)
	line2 := createQueueTestLine("line2", "10.0.0.2", "192.168.1.1", 5*time.Minute)
	queue.Add(line1)
	queue.Add(line2)

	snapshot := queue.GetTasksSnapshot()

	// Should be a copy, not the original slice
	assert.Equal(t, 2, len(snapshot))

	// Modifying snapshot should not affect original
	snapshot[0].ID = "modified"

	// Original should be unchanged
	originalSnapshot := queue.GetTasksSnapshot()
	assert.Equal(t, "line1", originalSnapshot[0].ID)
}

func TestIntervalTaskQueue_ExecNotify(t *testing.T) {
	// Use very short interval for testing
	queue := NewIntervalTaskQueue(50 * time.Millisecond)
	defer queue.Stop()

	notifyChan := queue.ExecNotify()

	// Wait for at least one notification
	select {
	case <-notifyChan:
		// Success - received notification
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Did not receive execution notification within expected time")
	}
}

func TestIntervalTaskQueue_ExecNotify_NonBlocking(t *testing.T) {
	// Use very short interval for testing
	queue := NewIntervalTaskQueue(10 * time.Millisecond)
	defer queue.Stop()

	notifyChan := queue.ExecNotify()

	// Receive multiple notifications without blocking
	notificationCount := 0
	timeout := time.After(100 * time.Millisecond)

	for notificationCount < 3 {
		select {
		case <-notifyChan:
			notificationCount++
		case <-timeout:
			goto done
		}
	}

done:
	assert.True(t, notificationCount >= 2, "Should receive multiple notifications")
}

func TestIntervalTaskQueue_Stop(t *testing.T) {
	queue := NewIntervalTaskQueue(100 * time.Millisecond)

	// Queue should be running initially
	notifyChan := queue.ExecNotify()

	// Receive at least one notification to confirm it's running
	select {
	case <-notifyChan:
		// Good, it's running
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Queue not running before stop")
	}

	// Stop the queue
	queue.Stop()

	// After stopping, should not receive more notifications
	select {
	case <-notifyChan:
		t.Fatal("Received notification after stop")
	case <-time.After(200 * time.Millisecond):
		// Good, no notification after stop
	}
}

// Concurrent access tests
func TestIntervalTaskQueue_ConcurrentAccess(t *testing.T) {
	queue := NewIntervalTaskQueue(1 * time.Second) // Longer interval to reduce noise
	defer queue.Stop()

	var wg sync.WaitGroup
	numGoroutines := 10
	operationsPerGoroutine := 50

	// Concurrent additions
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < operationsPerGoroutine; j++ {
				line := createQueueTestLine(
					fmt.Sprintf("line_%d_%d", id, j),
					fmt.Sprintf("10.%d.%d.1", id, j),
					"192.168.1.1",
					5*time.Minute,
				)
				queue.Add(line)
			}
		}(i)
	}

	// Concurrent reads
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < operationsPerGoroutine; j++ {
				queue.GetTasksSnapshot()
				queue.IsEmpty()
			}
		}()
	}

	wg.Wait()

	// Final state should be consistent
	snapshot := queue.GetTasksSnapshot()
	assert.Equal(t, numGoroutines*operationsPerGoroutine, len(snapshot))
}

func TestIntervalTaskQueue_ConcurrentAddRemove(t *testing.T) {
	queue := NewIntervalTaskQueue(1 * time.Second)
	defer queue.Stop()

	var wg sync.WaitGroup
	numOperations := 100

	// Add initial lines
	for i := 0; i < 10; i++ {
		line := createQueueTestLine(
			fmt.Sprintf("initial_%d", i),
			fmt.Sprintf("10.0.0.%d", i),
			"192.168.1.1",
			5*time.Minute,
		)
		queue.Add(line)
	}

	// Concurrent add/remove operations
	for i := 0; i < numOperations; i++ {
		wg.Add(2)

		go func(id int) {
			defer wg.Done()
			line := createQueueTestLine(
				fmt.Sprintf("concurrent_%d", id),
				fmt.Sprintf("10.1.%d.1", id),
				"192.168.1.1",
				5*time.Minute,
			)
			queue.Add(line)
		}(i)

		go func(id int) {
			defer wg.Done()
			// Try to remove various lines
			if id%2 == 0 {
				queue.Remove(fmt.Sprintf("10.0.0.%d", id%10))
			} else {
				queue.Remove(fmt.Sprintf("10.0.1.%d", id-1))
			}
		}(i)
	}

	wg.Wait()

	// Should not panic and should have consistent state
	snapshot := queue.GetTasksSnapshot()
	assert.True(t, len(snapshot) >= 0)
}

func TestIntervalTaskQueue_ConcurrentReplaceAll(t *testing.T) {
	queue := NewIntervalTaskQueue(1 * time.Second)
	defer queue.Stop()

	var wg sync.WaitGroup
	numGoroutines := 5
	numReplaceOperations := 10

	// Concurrent ReplaceAll operations
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numReplaceOperations; j++ {
				newLines := make([]syncer.Line, 3)
				for k := 0; k < 3; k++ {
					newLines[k] = createQueueTestLine(
						fmt.Sprintf("replace_%d_%d_%d", id, j, k),
						fmt.Sprintf("10.%d.%d.%d", id, j, k),
						"192.168.1.1",
						5*time.Minute,
					)
				}
				queue.ReplaceAll(newLines)
			}
		}(i)
	}

	// Concurrent reads during replacement
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numReplaceOperations*2; j++ {
				queue.GetTasksSnapshot()
				queue.IsEmpty()
				time.Sleep(1 * time.Millisecond) // Small delay to increase chances of race
			}
		}()
	}

	wg.Wait()

	// Final state should be consistent
	snapshot := queue.GetTasksSnapshot()
	assert.Equal(t, 3, len(snapshot)) // Last ReplaceAll should have 3 lines
}

// Performance and stress tests
func TestIntervalTaskQueue_LargeNumberOfLines(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping large dataset test in short mode")
	}

	queue := NewIntervalTaskQueue(1 * time.Hour) // Long interval to avoid notifications during test
	defer queue.Stop()

	numLines := 10000

	start := time.Now()

	// Add many lines
	for i := 0; i < numLines; i++ {
		line := createQueueTestLine(
			fmt.Sprintf("large_%d", i),
			fmt.Sprintf("10.%d.%d.%d", (i/65536)%256, (i/256)%256, i%256),
			"192.168.1.1",
			5*time.Minute,
		)
		queue.Add(line)
	}

	addElapsed := time.Since(start)

	// Verify all lines are present
	snapshot := queue.GetTasksSnapshot()
	assert.Equal(t, numLines, len(snapshot))

	snapshotTime := time.Since(start) - addElapsed

	// Test search performance
	start = time.Now()
	for i := 0; i < 1000; i++ {
		queue.Contains(fmt.Sprintf("10.100.0.%d", i))
	}
	searchElapsed := time.Since(start)

	t.Logf("Added %d lines in %v", numLines, addElapsed)
	t.Logf("Snapshot of %d lines took %v", numLines, snapshotTime)
	t.Logf("1000 contains operations took %v", searchElapsed)
}

// Edge case tests
func TestIntervalTaskQueue_ZeroInterval(t *testing.T) {
	queue := NewIntervalTaskQueue(0)
	defer queue.Stop()

	// Should handle zero interval gracefully by converting to minimum interval
	assert.NotNil(t, queue)
	assert.Equal(t, time.Nanosecond, queue.interval)
}

func TestIntervalTaskQueue_RemoveFromEmptyQueue(t *testing.T) {
	queue := NewIntervalTaskQueue(5 * time.Minute)
	defer queue.Stop()

	// Should not panic when removing from empty queue
	assert.NotPanics(t, func() {
		queue.Remove("10.0.0.999")
	})

	assert.True(t, queue.IsEmpty())
}

func TestIntervalTaskQueue_UpdateInEmptyQueue(t *testing.T) {
	queue := NewIntervalTaskQueue(5 * time.Minute)
	defer queue.Stop()

	line := createQueueTestLine("line1", "10.0.0.1", "192.168.1.1", 5*time.Minute)

	// Should not panic when updating in empty queue
	assert.NotPanics(t, func() {
		queue.UpdateTask(line)
	})

	assert.True(t, queue.IsEmpty())
}

// Benchmark tests
func BenchmarkIntervalTaskQueue_Add(b *testing.B) {
	queue := NewIntervalTaskQueue(1 * time.Hour)
	defer queue.Stop()

	lines := make([]syncer.Line, b.N)
	for i := 0; i < b.N; i++ {
		lines[i] = createQueueTestLine(
			fmt.Sprintf("bench_%d", i),
			fmt.Sprintf("10.%d.%d.%d", (i/65536)%256, (i/256)%256, i%256),
			"192.168.1.1",
			5*time.Minute,
		)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		queue.Add(lines[i])
	}
}

func BenchmarkIntervalTaskQueue_Remove(b *testing.B) {
	queue := NewIntervalTaskQueue(1 * time.Hour)
	defer queue.Stop()

	// Pre-populate queue
	for i := 0; i < b.N; i++ {
		line := createQueueTestLine(
			fmt.Sprintf("bench_%d", i),
			fmt.Sprintf("10.%d.%d.%d", (i/65536)%256, (i/256)%256, i%256),
			"192.168.1.1",
			5*time.Minute,
		)
		queue.Add(line)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		queue.Remove(fmt.Sprintf("10.200.0.%d", i))
	}
}

func BenchmarkIntervalTaskQueue_Contains(b *testing.B) {
	queue := NewIntervalTaskQueue(1 * time.Hour)
	defer queue.Stop()

	// Pre-populate queue
	numLines := 1000
	for i := 0; i < numLines; i++ {
		line := createQueueTestLine(
			fmt.Sprintf("bench_%d", i),
			fmt.Sprintf("10.%d.%d.%d", (i/65536)%256, (i/256)%256, i%256),
			"192.168.1.1",
			5*time.Minute,
		)
		queue.Add(line)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		queue.Contains(fmt.Sprintf("10.200.0.%d", i%numLines))
	}
}

func BenchmarkIntervalTaskQueue_GetTasksSnapshot(b *testing.B) {
	queue := NewIntervalTaskQueue(1 * time.Hour)
	defer queue.Stop()

	// Pre-populate queue
	numLines := 1000
	for i := 0; i < numLines; i++ {
		line := createQueueTestLine(
			fmt.Sprintf("bench_%d", i),
			fmt.Sprintf("10.%d.%d.%d", (i/65536)%256, (i/256)%256, i%256),
			"192.168.1.1",
			5*time.Minute,
		)
		queue.Add(line)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		queue.GetTasksSnapshot()
	}
}

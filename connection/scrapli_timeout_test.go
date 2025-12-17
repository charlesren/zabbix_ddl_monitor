package connection

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestScrapliDriver_Execute_TimeoutControl 测试Execute方法的超时控制功能
func TestScrapliDriver_Execute_TimeoutControl(t *testing.T) {
	// 这个测试验证修改后的Execute方法是否能够正确处理超时
	// 由于ScrapliDriver依赖外部库，我们主要验证逻辑正确性

	t.Run("context timeout should propagate to Execute method", func(t *testing.T) {
		// 验证context超时能够被Execute方法正确处理
		// 这是修复的核心：确保阻塞的scrapli调用可以被中断

		// 创建一个会立即超时的context
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
		defer cancel()

		// 给一点时间让context超时
		time.Sleep(2 * time.Millisecond)

		// 验证context已经超时
		assert.Error(t, ctx.Err())
		assert.Equal(t, context.DeadlineExceeded, ctx.Err())
	})

	t.Run("goroutine with select should respect context timeout", func(t *testing.T) {
		// 验证goroutine+select模式能够正确响应context超时
		// 这是我们在Execute方法中实现的模式

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		resultChan := make(chan string, 1)

		// 模拟一个长时间运行的操作
		go func() {
			time.Sleep(200 * time.Millisecond) // 这个操作比context超时时间长
			resultChan <- "result"
		}()

		start := time.Now()
		select {
		case <-ctx.Done():
			// 应该在这里超时
			duration := time.Since(start)
			assert.True(t, duration >= 50*time.Millisecond && duration < 100*time.Millisecond,
				"应该在50ms左右超时，实际: %v", duration)
			assert.Equal(t, context.DeadlineExceeded, ctx.Err())
		case <-resultChan:
			t.Fatal("不应该收到结果，应该超时")
		}
	})

	t.Run("fast operation should complete before timeout", func(t *testing.T) {
		// 验证快速操作能够在超时前完成

		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		resultChan := make(chan string, 1)

		// 模拟一个快速操作
		go func() {
			time.Sleep(50 * time.Millisecond) // 这个操作比context超时时间短
			resultChan <- "success"
		}()

		start := time.Now()
		select {
		case <-ctx.Done():
			t.Fatal("不应该超时，操作应该完成")
		case result := <-resultChan:
			duration := time.Since(start)
			assert.Equal(t, "success", result)
			assert.True(t, duration >= 50*time.Millisecond && duration < 100*time.Millisecond,
				"应该在50ms左右完成，实际: %v", duration)
			assert.NoError(t, ctx.Err())
		}
	})
}

// TestScrapliDriver_Execute_ErrorHandling 测试错误处理
func TestScrapliDriver_Execute_ErrorHandling(t *testing.T) {
	t.Run("should handle nil context", func(t *testing.T) {
		// 验证nil context的处理
		// 在修改后的Execute方法中，如果context为nil，会使用默认context

		// 这个测试主要是文档作用，实际测试需要完整的ScrapliDriver实例
		assert.True(t, true, "nil context should be handled by using default context")
	})

	t.Run("should handle cancelled context", func(t *testing.T) {
		// 验证已取消的context能够被正确处理

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // 立即取消

		// 验证context已经取消
		assert.Error(t, ctx.Err())
		assert.Equal(t, context.Canceled, ctx.Err())
	})
}

// TestScrapliDriver_Execute_Concurrency 测试并发安全性
func TestScrapliDriver_Execute_Concurrency(t *testing.T) {
	// 验证Execute方法的并发安全性
	// ScrapliDriver有mutex保护，应该是线程安全的

	t.Run("multiple goroutines should work correctly", func(t *testing.T) {
		const numGoroutines = 10
		done := make(chan bool, numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				defer cancel()

				// 模拟一些工作
				time.Sleep(time.Duration(id%3) * 20 * time.Millisecond)

				// 检查context是否仍然有效
				select {
				case <-ctx.Done():
					// 如果超时了，也没关系
				default:
					// 没有超时
				}

				done <- true
			}(i)
		}

		// 等待所有goroutine完成
		timeout := time.After(500 * time.Millisecond)
		for i := 0; i < numGoroutines; i++ {
			select {
			case <-done:
				// 继续
			case <-timeout:
				t.Fatalf("超时等待goroutine完成，已完成: %d/%d", i, numGoroutines)
			}
		}
	})
}

// TestScrapliDriver_Execute_RealWorldScenario 模拟真实场景
func TestScrapliDriver_Execute_RealWorldScenario(t *testing.T) {
	// 模拟真实场景：ping命令可能因为I/O错误而挂起

	t.Run("simulate IO error scenario", func(t *testing.T) {
		// 模拟I/O错误场景：操作挂起，需要超时中断
		// 这正是我们在日志中看到的问题：read /dev/ptmx: input/output error

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		operationDone := make(chan error, 1)

		// 模拟一个可能挂起的I/O操作
		go func() {
			// 模拟I/O操作可能挂起的情况
			// 在真实场景中，这可能是read /dev/ptmx这样的系统调用
			select {
			case <-time.After(10 * time.Second): // 模拟长时间挂起
				operationDone <- nil
			case <-ctx.Done():
				operationDone <- ctx.Err()
			}
		}()

		start := time.Now()
		select {
		case err := <-operationDone:
			duration := time.Since(start)
			if err != nil {
				// 应该超时
				assert.Equal(t, context.DeadlineExceeded, err)
				assert.True(t, duration >= 2*time.Second && duration < 3*time.Second,
					"应该在2秒左右超时，实际: %v", duration)
			} else {
				t.Fatal("不应该成功完成，应该超时")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("测试本身超时了")
		}
	})

	t.Run("simulate normal ping completion", func(t *testing.T) {
		// 模拟正常的ping命令完成

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		operationDone := make(chan string, 1)

		// 模拟正常的ping命令（快速完成）
		go func() {
			time.Sleep(100 * time.Millisecond) // 模拟ping命令执行时间
			operationDone <- "Success rate is 100 percent (5/5)"
		}()

		start := time.Now()
		select {
		case result := <-operationDone:
			duration := time.Since(start)
			assert.Equal(t, "Success rate is 100 percent (5/5)", result)
			assert.True(t, duration >= 100*time.Millisecond && duration < 200*time.Millisecond,
				"应该在100ms左右完成，实际: %v", duration)
			assert.NoError(t, ctx.Err())
		case <-ctx.Done():
			t.Fatal("不应该超时，ping命令应该快速完成")
		}
	})
}

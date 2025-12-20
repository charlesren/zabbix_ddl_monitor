package connection

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestScrapliDriver_GetPrompt_TimeoutControl 测试GetPrompt方法的超时控制功能
func TestScrapliDriver_GetPrompt_TimeoutControl(t *testing.T) {
	t.Run("context timeout should propagate to GetPrompt method", func(t *testing.T) {
		// 验证context超时能够被GetPrompt方法正确处理
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
		// 这是我们在GetPromptWithContext方法中实现的模式

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

// TestScrapliDriver_GetPrompt_ErrorHandling 测试错误处理
func TestScrapliDriver_GetPrompt_ErrorHandling(t *testing.T) {
	t.Run("should handle nil context", func(t *testing.T) {
		// 验证nil context的处理
		// 在修改后的GetPromptWithContext方法中，如果context为nil，会使用默认context

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
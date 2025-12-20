package connection

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestScrapliDriver_GetPromptWithContext_Integration 测试GetPromptWithContext方法与context的集成
func TestScrapliDriver_GetPromptWithContext_Integration(t *testing.T) {
	t.Run("should respect parent context cancellation", func(t *testing.T) {
		// 创建一个可取消的父context
		parentCtx, cancel := context.WithCancel(context.Background())
		
		// 创建一个独立的测试driver（不会真正连接到设备）
		driver := &ScrapliDriver{
			host:     "test-host",
			username: "test-user",
			password: "test-password",
			platform: "cisco_iosxe",
			timeout:  5 * time.Second,
			closed:   true, // 标记为已关闭，这样方法会立即返回错误
		}
		
		// 在调用前取消context
		cancel()
		
		// 调用GetPromptWithContext
		_, err := driver.GetPromptWithContext(parentCtx)
		
		// 验证返回了context取消错误
		assert.Error(t, err)
		assert.Equal(t, context.Canceled, parentCtx.Err())
	})
	
	t.Run("should handle timeout context", func(t *testing.T) {
		// 创建一个超时context
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
		defer cancel()
		
		// 创建一个独立的测试driver（不会真正连接到设备）
		driver := &ScrapliDriver{
			host:     "test-host",
			username: "test-user",
			password: "test-password",
			platform: "cisco_iosxe",
			timeout:  5 * time.Second,
			closed:   true, // 标记为已关闭，这样方法会立即返回错误
		}
		
		// 给一点时间让context超时
		time.Sleep(2 * time.Millisecond)
		
		// 调用GetPromptWithContext
		_, err := driver.GetPromptWithContext(ctx)
		
		// 验证返回了context超时错误
		assert.Error(t, err)
		assert.Equal(t, context.DeadlineExceeded, ctx.Err())
	})
}
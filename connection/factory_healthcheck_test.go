package connection

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestScrapliFactoryHealthCheck 测试ScrapliFactory的HealthCheck方法
func TestScrapliFactoryHealthCheck(t *testing.T) {
	t.Run("should return false for non-scrapli driver", func(t *testing.T) {
		factory := &ScrapliFactory{}
		
		// 创建一个mock driver（非ScrapliDriver类型）
		mockDriver := &MockProtocolDriver{
			ProtocolTypeFunc: func() Protocol {
				return ProtocolSSH
			},
		}
		
		// HealthCheck应该返回false
		result := factory.HealthCheck(mockDriver)
		assert.False(t, result)
	})
	
	t.Run("should handle nil driver", func(t *testing.T) {
		factory := &ScrapliFactory{}
		
		// HealthCheck应该处理nil情况
		result := factory.HealthCheck(nil)
		assert.False(t, result)
	})
}
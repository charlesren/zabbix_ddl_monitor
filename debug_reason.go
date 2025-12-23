package main

import (
	"fmt"
	"time"
	
	"github.com/charlesren/zabbix_ddl_monitor/connection"
)

func main() {
	// 创建配置
	config := &connection.EnhancedConnectionConfig{
		SmartRebuildEnabled:  true,
		RebuildMaxUsageCount: 200,
		RebuildMaxAge:        30 * time.Minute,
		RebuildMaxErrorRate:  0.2,
		RebuildMinInterval:   5 * time.Minute,
		RebuildStrategy:      "any",
	}
	
	fmt.Printf("配置: RebuildMaxUsageCount=%d\n", config.RebuildMaxUsageCount)
	
	// 创建rebuildManager
	rebuildManager := connection.NewRebuildManager(config, nil, nil)
	
	// 创建连接池
	pool := &connection.EnhancedConnectionPool{
		config: *config,
	}
	pool.RebuildManager = rebuildManager
	
	fmt.Printf("pool.rebuildManager: %v\n", pool.RebuildManager != nil)
	fmt.Printf("pool.config.RebuildMaxUsageCount: %d\n", pool.config.RebuildMaxUsageCount)
	
	// 测试getRebuildReason方法是否存在
	fmt.Println("测试完成")
}

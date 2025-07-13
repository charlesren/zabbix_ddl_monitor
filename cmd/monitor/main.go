package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/yourusername/zabbix_ddl_monitor/internal/aggregator"
	"github.com/yourusername/zabbix_ddl_monitor/internal/batch"
	"github.com/yourusername/zabbix_ddl_monitor/internal/config"
	"github.com/yourusername/zabbix_ddl_monitor/internal/router"
	"github.com/yourusername/zabbix_ddl_monitor/internal/task"
)

func main() {
	// 初始化组件
	taskReg := task.NewRegistry()
	taskReg.Register("ping", &task.PingTask{})

	connPool := router.NewConnectionPool()
	aggregator := aggregator.New()
	scheduler := batch.NewRouterGroupScheduler(taskReg, connPool, aggregator)

	// 模拟添加专线
	cfgMgr := config.NewConfigManager()
	for _, line := range cfgMgr.GetLines() {
		scheduler.AddLine(line)
	}

	// 优雅退出
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	scheduler.Stop()
	log.Println("服务已停止")
}

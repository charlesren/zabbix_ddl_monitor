package router_test

import (
	"testing"

	"github.com/yourusername/zabbix_ddl_monitor/internal/router"
)

func TestPool(t *testing.T) {
	pool := router.NewPool()
	_, err := pool.Get("192.168.1.1", "cisco_iosxe")
	if err != nil {
		t.Fatalf("Failed to get connection: %v", err)
	}
}

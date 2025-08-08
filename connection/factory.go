package connection

import "github.com/charlesren/zabbix_ddl_monitor/syncer"

type ProtocolFactory interface {
	Create(router *syncer.Router) (ProtocolDriver, error)
	HealthCheck(driver ProtocolDriver) bool
}

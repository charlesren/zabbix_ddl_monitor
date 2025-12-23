package main

import (
	"fmt"
	"reflect"
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
	
	// 创建rebuildManager
	rebuildManager := connection.NewRebuildManager(config, nil, nil)
	
	// 创建连接
	conn := &connection.EnhancedPooledConnection{}
	
	// 使用反射设置字段
	connValue := reflect.ValueOf(conn).Elem()
	
	// 设置createdAt
	createdAtField := connValue.FieldByName("createdAt")
	if createdAtField.IsValid() && createdAtField.CanSet() {
		createdAtField.Set(reflect.ValueOf(time.Now().Add(-40*time.Minute)))
		fmt.Println("设置createdAt成功")
	}
	
	// 设置usageCount
	usageCountField := connValue.FieldByName("usageCount")
	if usageCountField.IsValid() && usageCountField.CanSet() {
		usageCountField.SetInt(250)
		fmt.Println("设置usageCount成功")
	}
	
	// 设置totalRequests和totalErrors
	totalRequestsField := connValue.FieldByName("totalRequests")
	if totalRequestsField.IsValid() && totalRequestsField.CanSet() {
		totalRequestsField.SetInt(50)
		fmt.Println("设置totalRequests成功")
	}
	
	totalErrorsField := connValue.FieldByName("totalErrors")
	if totalErrorsField.IsValid() && totalErrorsField.CanSet() {
		totalErrorsField.SetInt(5)
		fmt.Println("设置totalErrors成功")
	}
	
	// 调用GetRebuildReason
	reason := rebuildManager.GetRebuildReason(conn)
	fmt.Printf("GetRebuildReason返回: %q\n", reason)
}

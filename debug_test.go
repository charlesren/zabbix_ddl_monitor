package main

import (
	"fmt"
	"reflect"
	"time"
	
	"github.com/charlesren/zabbix_ddl_monitor/connection"
)

func main() {
	now := time.Now()
	conn := &connection.EnhancedPooledConnection{}
	
	// 使用反射设置字段
	connValue := reflect.ValueOf(conn).Elem()
	
	// 设置createdAt字段
	createdAtField := connValue.FieldByName("createdAt")
	if createdAtField.IsValid() && createdAtField.CanSet() {
		createdAtField.Set(reflect.ValueOf(now.Add(-40*time.Minute)))
		fmt.Println("设置createdAt成功")
	}
	
	// 设置usageCount
	usageCountField := connValue.FieldByName("usageCount")
	if usageCountField.IsValid() && usageCountField.CanSet() {
		usageCountField.SetInt(250)
		fmt.Println("设置usageCount成功")
	}
	
	// 测试getCreatedAt
	createdAt := conn.GetCreatedAt()
	fmt.Printf("getCreatedAt返回: %v\n", createdAt)
	
	// 测试getUsageCount
	usageCount := conn.GetUsageCount()
	fmt.Printf("getUsageCount返回: %d\n", usageCount)
}

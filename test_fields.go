package main

import (
	"fmt"
	"reflect"
	
	"github.com/charlesren/zabbix_ddl_monitor/connection"
)

func main() {
	conn := &connection.EnhancedPooledConnection{}
	connValue := reflect.ValueOf(conn).Elem()
	connType := connValue.Type()
	
	fmt.Println("EnhancedPooledConnection字段:")
	for i := 0; i < connType.NumField(); i++ {
		field := connType.Field(i)
		fmt.Printf("  %d: %s (类型: %v, 可导出: %v)\n", 
			i, field.Name, field.Type, field.PkgPath == "")
	}
}

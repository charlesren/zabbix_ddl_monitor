# 系统设计文档

## 架构图
![架构图](architecture.png)

## 核心组件
1. ConfigManager - 从Zabbix获取专线配置
2. ConnectionPool - 路由器连接池
3. Task系统 - 可扩展的任务框架
4. Batch系统 - 批处理调度

## 扩展指南
1. 添加新任务：实现Task接口并注册
2. 支持新平台：在Task.Prepare中添加平台判断


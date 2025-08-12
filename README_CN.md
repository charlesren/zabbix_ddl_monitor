# Zabbix 专线监控系统

基于 Go 语言开发的专线连通性监控服务，通过 Zabbix API 集成实现专线配置的动态发现和管理，利用 `scrapligo` 和 SSH 协议与路由器交互。

## 项目概述

Zabbix 专线监控系统是一个全面的网络监控解决方案，通过 Zabbix 基础设施自动发现和监控专用线路（DDL）。系统通过在边缘路由器上执行 ping 任务并将结果报告给监控系统，提供实时的连通性监控。

## 核心特性

### 🔄 动态配置管理
- **Zabbix API 集成**：通过基于代理的过滤自动从 Zabbix 主机发现专线
- **实时同步**：监控配置变更并动态调整监控任务
- **标签过滤**：使用 Zabbix 主机标签（`TempType=ddl`）识别专线主机
- **宏驱动配置**：从 Zabbix 主机宏中提取专线和路由器详细信息

### 🚀 可扩展任务系统
- **插件架构**：支持多种监控类型的可扩展任务系统
- **平台无关**：支持多种路由器平台（思科 IOSXE、华为 VRP、H3C Comware 等）
- **协议灵活性**：双协议支持（SSH 和 Scrapli）适用于不同使用场景
- **批量处理**：高效的批量 ping 操作支持多个目标 IP

### 🔗 高级连接管理
- **连接池**：自动清理和健康检查的高效资源管理
- **多协议支持**：SSH 用于基本操作，Scrapli 用于高级交互功能
- **能力验证**：自动验证协议/平台/命令兼容性
- **弹性设计**：内置连接重试、超时处理和优雅降级

### 📊 健壮执行框架
- **中间件支持**：可配置的超时、重试、日志和指标收集
- **异步执行**：工作池的非阻塞任务执行
- **结果聚合**：全面的结果收集和报告
- **错误处理**：详细的错误跟踪和恢复机制

## 系统架构组件

### 核心模块

#### 配置同步器（ConfigSyncer）
- 从 Zabbix API 获取专线配置
- 通过订阅模型监控配置变更
- 管理配置版本和变更通知
- 处理基于代理的主机发现和过滤

#### 管理器（Manager）
- 中央编排组件
- 基于专线配置管理路由器调度器
- 处理完整和增量同步
- 协调监控组件的生命周期

#### 连接系统
- **连接池（ConnectionPool）**：管理带池化的协议驱动实例
- **协议驱动（ProtocolDriver）**：SSH 和 Scrapli 协议实现接口
- **工厂模式**：基于配置创建合适的驱动
- **能力系统**：验证平台和协议兼容性

#### 任务框架
- **任务接口（Task Interface）**：不同监控类型的插件系统
- **Ping 任务（PingTask）**：连通性监控的主要实现
- **任务注册表（TaskRegistry）**：可用任务的中央注册表
- **执行器（Executor）**：基于中间件的任务执行引擎

## 支持的平台和协议

### 路由器平台
- **思科 IOSXE**：完整支持，交互式和命令模式
- **思科 NXOS**：Scrapli 协议支持
- **华为 VRP**：SSH 和 Scrapli 协议支持
- **H3C Comware**：基础协议支持
- **思科 IOSXR**：扩展平台支持

### 协议支持
- **SSH**：基本命令执行，通用兼容性
- **Scrapli**：高级功能，包括交互式事件、自动补全

### 命令类型
- **Commands**：简单命令执行
- **Interactive Events**：复杂交互会话，支持提示符处理

## 配置说明

### Zabbix 集成
系统需要专线主机上的特定 Zabbix 主机宏配置：

```yaml
# 专线主机上必需的宏
{$LINE_ID}: "unique-line-identifier"      # 唯一专线标识符
{$LINE_CHECK_INTERVAL}: "180"             # 检查间隔（秒）
{$LINE_ROUTER_IP}: "192.168.1.1"         # 路由器 IP
{$LINE_ROUTER_USERNAME}: "admin"          # 路由器用户名
{$LINE_ROUTER_PASSWORD}: "password"       # 路由器密码
{$LINE_ROUTER_PLATFORM}: "cisco_iosxe"   # 路由器平台
{$LINE_ROUTER_PROTOCOL}: "scrapli"       # 协议类型
```

### 服务配置
```yaml
# conf/svr.yml
server:
  log:
    applog:
      loglevel: 1
  ip: 192.168.1.100

zabbix:
  username: "monitor_user"
  password: "password"
  serverip: "zabbix.example.com"
  serverport: "80"
```

## 使用说明

### 启动服务
```bash
# 使用默认配置
./cmd/monitor/main

# 使用自定义配置路径
./cmd/monitor/main -c /path/to/config.yml
```

### 任务示例

#### 单 IP Ping
```go
params := map[string]interface{}{
    "target_ip": "8.8.8.8",
    "repeat": 5,
    "timeout": 2 * time.Second,
}
```

#### 批量 IP Ping
```go
params := map[string]interface{}{
    "target_ips": []string{"8.8.8.8", "1.1.1.1", "208.67.222.222"},
    "repeat": 3,
    "timeout": 1 * time.Second,
}
```

## API 参考

### 任务接口
```go
type Task interface {
    Meta() TaskMeta
    ValidateParams(params map[string]interface{}) error
    BuildCommand(ctx TaskContext) (Command, error)
    ParseOutput(ctx TaskContext, raw interface{}) (Result, error)
}
```

### 协议驱动接口
```go
type ProtocolDriver interface {
    ProtocolType() Protocol
    Close() error
    Execute(req *ProtocolRequest) (*ProtocolResponse, error)
    GetCapability() ProtocolCapability
}
```

## 监控与指标

### 健康检查
- 连接池健康监控
- 协议驱动能力验证
- 任务执行成功率
- 配置同步状态

### 日志记录
- 可配置级别的结构化日志
- 任务执行跟踪
- 连接生命周期事件
- 错误跟踪和调试

### 性能指标
- 任务执行持续时间
- 连接池利用率
- 按平台划分的成功/失败率
- 配置同步频率

## 开发指南

### 构建项目
```bash
go mod download
go build -o ddl_monitor ./cmd/monitor
```

### 运行测试
```bash
# 单元测试
go test ./...

# 集成测试
go test ./connection -tags=integration
go test ./task -tags=integration
```

### 扩展系统

#### 添加新平台
1. 在任务适配器中实现平台特定的命令生成
2. 在 `connection/types.go` 中添加平台常量
3. 更新能力定义
4. 添加平台特定的输出解析

#### 添加新任务类型
1. 实现 `Task` 接口
2. 在 `TaskRegistry` 中注册任务
3. 添加平台特定实现
4. 更新能力映射

## 依赖关系

### 核心依赖
- **scrapligo**：高级网络设备自动化
- **zapix**：Zabbix API 客户端库
- **ylog**：结构化日志框架
- **viper**：配置管理

### 协议库
- **golang.org/x/crypto/ssh**：SSH 协议实现
- **scrapli/scrapligo**：网络设备自动化框架

## 平台特定实现

### 思科平台 ping 命令
```bash
# 需要 enable 密码时
enable
Password: <enable_password>
ping 8.8.8.8 repeat 5 timeout 2

# 用户模式
ping 8.8.8.8 repeat 5 timeout 2
```

### 华为平台 ping 命令
```bash
ping -c 5 -W 2 8.8.8.8
```

### 输出解析规则

#### 思科输出解析
- 成功率模式：`Success rate is 100 percent (5/5)`
- RTT 信息：`round-trip min/avg/max = 1/2/4 ms`

#### 华为输出解析
- 丢包率模式：`0% packet loss`
- RTT 信息：包含 `min/avg/max` 的行

## 错误处理与容错

### 容错机制
- **连接级别**：自动重连、健康检查、连接池恢复
- **任务级别**：重试机制、超时控制、降级处理
- **系统级别**：优雅关闭、资源清理、状态恢复

### 监控指标
- 任务执行成功率
- 连接池利用率
- 配置同步延迟
- 协议驱动健康状态

## 性能优化

### 连接池优化
- 连接复用减少创建开销
- 预热机制提高响应速度
- 自动清理避免资源泄漏
- 并发控制防止过载

### 批量处理优化
- 批量 ping 减少连接开销
- 异步执行提高并发能力
- 结果聚合减少通信次数

### 内存优化
- 配置缓存减少 API 调用
- 订阅模式避免轮询
- 增量同步减少数据传输

## 部署与运维

### 构建部署
```bash
# 构建
go mod download
go build -o ddl_monitor ./cmd/monitor

# 运行
./ddl_monitor -c conf/svr.yml
```

### 监控运维
- 服务健康检查端点
- 性能指标收集
- 日志聚合分析
- 告警规则配置

### 故障排查
- 详细的结构化日志
- 连接泄漏检测
- 任务执行追踪
- 配置同步状态监控

## 测试策略

### 单元测试
- 核心组件功能测试
- 错误场景模拟
- 边界条件验证

### 集成测试
- 端到端流程测试
- 协议驱动集成
- Zabbix API 集成

### 性能测试
- 连接池压力测试
- 批量任务性能
- 内存泄漏检测

## 许可证

本项目采用 MIT 许可证 - 请查看 LICENSE 文件了解详情。

## 贡献指南

1. Fork 仓库
2. 创建功能分支
3. 提交更改
4. 推送到分支
5. 创建 Pull Request

## 技术支持

如有问题和疑问：
- 在 GitHub 仓库中创建 issue
- 查看 `/docs` 目录中的文档
- 查看测试文件了解使用示例

## 系统工作流程

### 启动流程
```
1. 加载配置文件
2. 初始化日志系统
3. 创建 Zabbix 客户端
4. 初始化 ConfigSyncer
5. 创建 Manager 并注册 PingTask
6. 启动配置同步协程
7. 启动变更事件处理协程
8. 执行初始全量同步
```

### 配置同步流程
```
定时同步：
1. 通过代理 IP 查询代理 ID
2. 获取带有 ddl 标签的主机
3. 解析主机宏提取配置
4. 计算配置变更
5. 更新内部状态
6. 通知订阅者

变更处理：
1. 接收变更事件
2. 更新路由器专线映射
3. 创建/更新/删除调度器
4. 启动/停止调度任务
```

### 任务执行流程
```
1. 调度器触发任务
2. 从连接池获取协议驱动
3. 验证任务能力匹配
4. 构建执行上下文
5. 执行中间件链
6. 协议驱动执行命令
7. 解析输出生成结果
8. 释放连接回池
9. 聚合结果并上报
```

## 关键数据结构

### 专线配置
```go
type Line struct {
    ID       string        // 专线唯一标识
    IP       string        // 专线IP地址
    Interval time.Duration // 检查间隔
    Router   Router        // 关联路由器
    Hash     uint64        // 配置哈希值
}

type Router struct {
    IP       string              // 路由器IP
    Username string              // 登录用户名
    Password string              // 登录密码
    Platform connection.Platform // 平台类型
    Protocol connection.Protocol // 协议类型
}
```

### 变更事件
```go
type LineChangeEvent struct {
    Type    ChangeType // 变更类型
    Line    Line       // 专线数据
    Version int64      // 配置版本
}

const (
    LineCreate ChangeType = iota + 1
    LineUpdate
    LineDelete
)
```

### 任务结果
```go
type Result struct {
    Success bool                   `json:"success"`
    Data    map[string]interface{} `json:"data"`
    Error   string                 `json:"error,omitempty"`
}
```

通过本系统，您可以轻松实现企业级的专线连通性监控，确保网络基础设施的稳定性和可靠性。
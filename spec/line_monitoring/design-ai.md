# 专线监控系统设计文档

## 1. 概述

### 1.1 项目背景
专线监控系统是一个基于Go语言开发的网络专线连通性监控服务，通过Zabbix API集成实现专线配置的动态发现和管理，利用scrapligo和SSH协议与路由器交互，执行连通性检测任务。

### 1.2 系统目标
- 自动从Zabbix基础设施发现和管理专线配置
- 实时监控专线连通性状态
- 支持多平台路由器（Cisco、华为、H3C等）
- 提供可扩展的任务执行框架
- 高可用性和容错能力

## 2. 系统架构

### 2.1 整体架构图
```
┌─────────────────────────────────────────────────────────────────┐
│                        Zabbix DDL Monitor                       │
├─────────────────────────────────────────────────────────────────┤
│  ┌─────────────────┐    ┌─────────────────┐    ┌──────────────┐ │
│  │   ConfigSyncer  │───▶│     Manager     │───▶│  Scheduler   │ │
│  │                 │    │                 │    │              │ │
│  │ • Zabbix API    │    │ • 路由器管理    │    │ • 任务调度   │ │
│  │ • 配置同步      │    │ • 生命周期      │    │ • 队列管理   │ │
│  │ • 变更通知      │    │ • 事件处理      │    │ • 间隔控制   │ │
│  └─────────────────┘    └─────────────────┘    └──────────────┘ │
├─────────────────────────────────────────────────────────────────┤
│  ┌─────────────────┐    ┌─────────────────┐    ┌──────────────┐ │
│  │ ConnectionPool  │───▶│   TaskExecutor  │───▶│ TaskRegistry │ │
│  │                 │    │                 │    │              │ │
│  │ • 连接池管理    │    │ • 中间件支持    │    │ • 任务注册   │ │
│  │ • 协议驱动      │    │ • 异步执行      │    │ • 插件系统   │ │
│  │ • 健康检查      │    │ • 结果聚合      │    │ • 能力验证   │ │
│  └─────────────────┘    └─────────────────┘    └──────────────┘ │
├─────────────────────────────────────────────────────────────────┤
│  ┌─────────────────┐    ┌─────────────────┐    ┌──────────────┐ │
│  │  ProtocolDriver │    │   PingTask      │    │  Aggregator  │ │
│  │                 │    │                 │    │              │ │
│  │ • SSH Driver    │    │ • 单/批量Ping   │    │ • 结果收集   │ │
│  │ • Scrapli Driver│    │ • 平台适配      │    │ • 状态上报   │ │
│  │ • 能力系统      │    │ • 输出解析      │    │ • 指标统计   │ │
│  └─────────────────┘    └─────────────────┘    └──────────────┘ │
└─────────────────────────────────────────────────────────────────┘
```

### 2.2 核心组件详述

#### 2.2.1 配置同步模块（ConfigSyncer）
**职责：**
- 通过Zabbix API获取专线配置信息
- 监控配置变更并生成变更事件
- 管理配置版本和订阅者通知

**核心接口：**
```go
type ConfigSyncer struct {
    client       Client                   // Zabbix客户端接口
    lines        map[string]Line          // 当前专线配置
    version      int64                    // 配置版本号
    subscribers  []chan<- LineChangeEvent // 订阅者列表
    mu           sync.RWMutex             // 读写锁
    syncInterval time.Duration            // 同步间隔
    lastSyncTime time.Time               // 最后同步时间
    ctx          context.Context          // 上下文
    cancel       context.CancelFunc       // 取消函数
    stopOnce     sync.Once               // 停止控制
    stopped      bool                    // 停止状态
}
```

**配置获取流程：**
1. 通过代理IP查询Zabbix代理ID
2. 使用代理ID和标签过滤(`TempType=ddl`)获取主机列表
3. 从主机宏中提取专线和路由器配置信息
4. 生成Line对象并计算配置哈希

#### 2.2.2 管理器（Manager）
**职责：**
- 协调整个系统的运行
- 管理路由器调度器的生命周期
- 处理配置变更事件
- 任务注册和发现

**核心结构：**
```go
type Manager struct {
    configSyncer ConfigSyncerInterface   // 配置同步器接口
    schedulers   map[string]Scheduler    // 路由器调度器映射
    routerLines  map[string][]syncer.Line // 路由器专线映射
    registry     task.Registry          // 任务注册表
    mu           sync.Mutex             // 互斥锁
    stopChan     chan struct{}          // 停止信号
    wg           sync.WaitGroup         // 等待组
}
```

**事件处理机制：**
- `LineCreate`: 添加新专线，创建或更新调度器
- `LineUpdate`: 更新专线配置，通知调度器
- `LineDelete`: 删除专线，延迟清理空调度器

#### 2.2.3 连接池（ConnectionPool）
**设计原则：**
- 协议无关的连接管理
- 自动健康检查和连接清理
- 支持连接预热和容量管理
- 调试模式下的连接泄漏检测

**核心组件：**
```go
type ConnectionPool struct {
    config      ConnectionConfig                    // 连接配置
    factories   map[Protocol]ProtocolFactory       // 协议工厂映射
    pools       map[Protocol]*DriverPool           // 协议连接池映射
    mu          sync.RWMutex                       // 读写锁
    idleTimeout time.Duration                      // 空闲超时
    ctx         context.Context                    // 上下文
    cancel      context.CancelFunc                 // 取消函数
    debugMode   bool                               // 调试模式
    activeConns map[string]string                  // 活跃连接跟踪
}
```

**生命周期管理：**
- 后台协程进行连接健康检查（30秒间隔）
- 空闲连接清理（1分钟间隔，5分钟超时）
- 优雅关闭时的资源释放

#### 2.2.4 协议驱动系统
**协议抽象：**
```go
type ProtocolDriver interface {
    ProtocolType() Protocol
    Close() error
    Execute(req *ProtocolRequest) (*ProtocolResponse, error)
    GetCapability() ProtocolCapability
}
```

**支持的协议：**
- **SSH协议**：基础命令执行，通用兼容性
- **Scrapli协议**：高级功能，支持交互式事件

**能力系统：**
```go
type ProtocolCapability struct {
    APIVersion          string              // 能力版本
    Protocol            Protocol            // 协议类型
    PlatformSupport     []Platform         // 支持平台
    CommandTypesSupport []CommandType      // 支持命令类型
    ConfigModes         []ConfigModeCapability // 配置模式
    MaxConcurrent       int                // 最大并发数
    Timeout             time.Duration      // 超时时间
    SupportsAutoComplete bool              // 自动补全支持
    SupportsColorOutput  bool              // 彩色输出支持
}
```

#### 2.2.5 任务系统
**任务接口规范：**
```go
type Task interface {
    Meta() TaskMeta                                              // 元信息
    ValidateParams(params map[string]interface{}) error         // 参数验证
    BuildCommand(ctx TaskContext) (Command, error)              // 命令构建
    ParseOutput(ctx TaskContext, raw interface{}) (Result, error) // 输出解析
}
```

**PingTask实现：**
- 支持单IP和批量IP ping
- 平台特定的命令生成（Cisco、华为）
- 智能输出解析和结果聚合
- 参数验证和错误处理

**任务上下文：**
```go
type TaskContext struct {
    TaskType    TaskType                   // 任务类型
    Platform    Platform                   // 平台类型
    Protocol    Protocol                   // 协议类型
    CommandType CommandType               // 命令类型
    Params      map[string]interface{}    // 任务参数
    Ctx         context.Context           // 执行上下文
}
```

#### 2.2.6 执行器（Executor）
**中间件架构：**
```go
type ExecutorFunc func(Task, connection.ProtocolDriver, TaskContext) (Result, error)
type Middleware func(ExecutorFunc) ExecutorFunc
```

**内置中间件：**
- **WithTimeout**: 超时控制
- **WithRetry**: 重试机制
- **WithLogging**: 日志记录
- **WithMetrics**: 指标收集

**执行流程：**
1. 参数验证
2. 能力验证（协议/平台/命令类型）
3. 命令构建
4. 协议驱动执行
5. 结果解析和返回

## 3. 数据模型

### 3.1 专线配置模型
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

### 3.2 变更事件模型
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

### 3.3 任务结果模型
```go
type Result struct {
    Success bool                   `json:"success"`
    Data    map[string]interface{} `json:"data"`
    Error   string                 `json:"error,omitempty"`
}
```

## 4. 关键流程

### 4.1 系统启动流程
```
1. 加载配置文件
2. 初始化日志系统
3. 创建Zabbix客户端
4. 初始化ConfigSyncer
5. 创建Manager并注册PingTask
6. 启动配置同步协程
7. 启动变更事件处理协程
8. 执行初始全量同步
```

### 4.2 配置同步流程
```
定时同步:
1. 通过代理IP查询代理ID
2. 获取带有ddl标签的主机
3. 解析主机宏提取配置
4. 计算配置变更
5. 更新内部状态
6. 通知订阅者

变更处理:
1. 接收变更事件
2. 更新路由器专线映射
3. 创建/更新/删除调度器
4. 启动/停止调度任务
```

### 4.3 任务执行流程
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

## 5. 平台支持

### 5.1 支持的路由器平台
- **Cisco IOSXE**: 全功能支持，交互式和命令模式
- **Cisco NXOS**: Scrapli协议支持
- **Cisco IOSXR**: 基础协议支持
- **Huawei VRP**: SSH和Scrapli协议支持
- **H3C Comware**: 基础协议支持

### 5.2 平台特定实现
**Cisco平台ping命令：**
```bash
# 需要enable密码时
enable
Password: <enable_password>
ping 8.8.8.8 repeat 5 timeout 2

# 用户模式
ping 8.8.8.8 repeat 5 timeout 2
```

**华为平台ping命令：**
```bash
ping -c 5 -W 2 8.8.8.8
```

### 5.3 输出解析规则
**Cisco输出解析：**
- 成功率模式：`Success rate is 100 percent (5/5)`
- RTT信息：`round-trip min/avg/max = 1/2/4 ms`

**华为输出解析：**
- 丢包率模式：`0% packet loss`
- RTT信息：包含`min/avg/max`的行

## 6. 配置管理

### 6.1 Zabbix宏配置
```
{$LINE_ID}: "line-001"                    # 专线唯一标识
{$LINE_CHECK_INTERVAL}: "180"             # 检查间隔（秒）
{$LINE_ROUTER_IP}: "192.168.1.1"         # 路由器IP
{$LINE_ROUTER_USERNAME}: "admin"          # 路由器用户名
{$LINE_ROUTER_PASSWORD}: "password"       # 路由器密码
{$LINE_ROUTER_PLATFORM}: "cisco_iosxe"   # 路由器平台
{$LINE_ROUTER_PROTOCOL}: "scrapli"       # 协议类型
```

### 6.2 服务配置
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

## 7. 错误处理与容错

### 7.1 错误分类
```go
type TaskError struct {
    Code    string      // 错误代码
    Message string      // 错误信息
    Details interface{} // 错误详情
}
```

### 7.2 容错机制
- **连接级别**: 自动重连、健康检查、连接池恢复
- **任务级别**: 重试机制、超时控制、降级处理
- **系统级别**: 优雅关闭、资源清理、状态恢复

### 7.3 监控指标
- 任务执行成功率
- 连接池利用率
- 配置同步延迟
- 协议驱动健康状态

## 8. 性能优化

### 8.1 连接池优化
- 连接复用减少创建开销
- 预热机制提高响应速度
- 自动清理避免资源泄漏
- 并发控制防止过载

### 8.2 批量处理优化
- 批量ping减少连接开销
- 异步执行提高并发能力
- 结果聚合减少通信次数

### 8.3 内存优化
- 配置缓存减少API调用
- 订阅模式避免轮询
- 增量同步减少数据传输

## 9. 扩展设计

### 9.1 新增平台支持
1. 在`connection/types.go`添加平台常量
2. 实现平台特定的命令生成逻辑
3. 添加输出解析规则
4. 更新能力定义映射

### 9.2 新增任务类型
1. 实现`Task`接口
2. 注册到`TaskRegistry`
3. 添加平台特定适配器
4. 更新能力映射关系

### 9.3 新增协议支持
1. 实现`ProtocolDriver`接口
2. 创建协议工厂
3. 定义协议能力
4. 注册到连接池

## 10. 部署与运维

### 10.1 构建部署
```bash
# 构建
go mod download
go build -o ddl_monitor ./cmd/monitor

# 运行
./ddl_monitor -c conf/svr.yml
```

### 10.2 监控运维
- 服务健康检查端点
- 性能指标收集
- 日志聚合分析
- 告警规则配置

### 10.3 故障排查
- 详细的结构化日志
- 连接泄漏检测
- 任务执行追踪
- 配置同步状态监控

## 11. 测试策略

### 11.1 单元测试
- 核心组件功能测试
- 错误场景模拟
- 边界条件验证

### 11.2 集成测试
- 端到端流程测试
- 协议驱动集成
- Zabbix API集成

### 11.3 性能测试
- 连接池压力测试
- 批量任务性能
- 内存泄漏检测

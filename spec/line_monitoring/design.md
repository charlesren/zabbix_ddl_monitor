# 专线监控系统设计文档

## 1. 概述
本系统通过Zabbix实现网络专线通断监控，通过动态管理专线配置和路由器连接，实现多平台任务执行与批量结果上报的核心流程：
1. **配置同步**：从Zabbix API拉取专线列表。
2. **任务调度**：按专线间隔执行路由器Ping检测，复用路由器连接。
3. **结果上报**：批量汇总检测结果回传Zabbix。

## 2. 系统架构与关键流程
### 2.1 架构图
```mermaid
graph TD
    A[ConfigSyncer] -->|拉取配置| B[Zabbix]
    C[Manager] -->|Line列表| A
    A -->|推送变更通知| C
    C -->|创建/更新| D[RouterScheduler1]
    D -->|持有| F[IntervalQueue-30s]
    D --> J[Executor]
    J -->|管理| L[ConnPool:10.0.0.1]
    F -->|生成task| N[Registry]
    F -->|提交task| J
    J --> O[Aggregator]
    O -->|批量上报| P[Zabbix]
```

### 2.2 核心模块

#### 配置同步模块（ConfigSyncer）
- 周期性的从Zabbix API获取专线配置（IP/间隔/路由器信息）。
- 周期可自定义，默认5m
- 提供专线列表查询接口，用于查询当前所有专线列表。
- 提供专线变更通知接口，把专线的增删改通知给订阅者。

#### 调度中心（Manager）
- 周期性的从ConfigSyncer获取全量专线列表
- 订阅ConfigSyncer的专线变更通知
- 维护动态路由器专线关系表,描述路由器上绑定的专线,可计算出路由器上绑定的专线数量，同时隐含的知道了路由器信息，因为每条专线上都有（初始化时从ConfigSyncer获取全量、收到ConfigSyncer通知后更新，周期性的获取从ConfigSyncer获取全量纠正，放在异常，周期为1小时）
- 管理所有RouterScheduler创建(启动时、收到专线新增时按需创建，根据全量列表周期校准)
- 路由器上绑定的专线数量为零时延迟删除RouterScheduler(10分钟),如果在延迟期间有新的专线关联上，则取消延迟删除
- 专线变更信息通知到相应的RouterScheduler,RouterScheduler收到通知后更新自身信息
- 管理任务注册中心Registry

#### 路由器调度器（RouterScheduler）
- 更新绑定到自身Line信息
- 维护路由器级别的任务队列（`IntervalQueue`）。
- 收到任务信号分发至 `Executor`。
- 拥有connectPool
- 调度PingTask类型的任务，任务的触发时机由IntervalQueue决定
- 从ConnectionPool申请连接、用完释放
- 根据Router里Protocol信息，申请相应的协议的连接
- Router里自带platform信息，用于匹配task实现的平台类型
- 根据任务类型（PingTask）以及router的协议类型、平台类型,利用任务注册中心里PingTask的信息生成任务，确保task命令类型与驱动匹配.优先选择Iteractive_event，其次选择commands.

#### 路由器任务队列（IntervalQueue)
  周期性的提供line列表，便于生成task(作为task的一个参数)
- IntervalQueue根据检查间隔定时触发任务。
- 维护相同interval的line列表


#### 连接池 （ConnectionPool）
- 保存协议的类型：源头是Router,由用户指定，在创建RouterScheduler时传到ConnectionPool作为自身的一个参数
- 根据协议类型维护管理相应的连接
- 需支持多协议类型ssh/netconf/scrapli连接
- 负责连接的创建、关闭、健康检查
- 提供protocoldriver的实例化和缓存，返回增强驱动
- 通过工厂模式创建不同的ProtocolDriver，便于扩展其它协议类型

#### 协议驱动（ProtocolDriver）
- 实现协议原生操作（如ssh命令发送，scrapli交互）
- 包括同样的基本方法，如Close,返回driver的协议类型的方法


#### 任务（Task）
Task 仅定义参数规范，实际工作由适配器完成
- 每一类任务都能返回支持的协议类型如ssh/scrapli,以及协议下命令的类型如interactive_event/commands，此外还有命令类型下支持的平台如cisco_iosxe、huawei_vrp
- 使用者提供协议类型、平台类型，不关心命令类型
- 任务开发者知道命令类型，为协议和平台提供任务实现
- 任务的实现可能支持多平台，如cisco_iosxe、huawei_vrp
- 任务执行前进行Platform/Protocol/CommandType匹配性校验和参数规范性校验

##### 注册中心（Registry）
注册和发现适配器实例
- 提供注册接口注册实现的任务类型
- 有一个注册中心，存储所有实现的各任务类型。
- 提供任务发现接口。
- 提供平台适配器查询接口。


##### 任务实现
- 声明支持的协议类型（如ssh/netconf/scrapli）
- 实现任务逻辑，与协议驱动匹配
- TaskType>Platform>Protocol>CommandType>TaskImpl依次递进的层级结构
- 协议可支持多种命令类型，如interactive_event/commands
- 当前scrapli实现interactive_event类型
- 当前ssh实现commands类型

##### 监控任务实现 （ping_task）
- 支持多平台（`cisco_iosxe`、`cisco_iosxr`、`cisco_nxos`、`h3c_comware`、`huawei_vrp`)
- 支持scrapligo和channel
- 支持多条专线的IP合并为一个参数，一次性检查

##### 平台适配器（PlatformAdapter）
- 将通用任务参数转换为设备特定的协议指令
- 封装不同厂商设备的特殊处理逻辑



##### 执行器 （Executor）
负责执行已生成的任务实例，专注协议驱动交互与流程控制,依赖外部传入的连接和任务
获取适配器 → 生成命令 → 提交到protocolDriver
- 参数规范性校验
- 保障协议driver的类型与task的实现匹配
- 保障task的实现支持当前的platform
- 提交任务到ProtocolDriver，执行任务
- 控制任务超时（默认30秒）和重试（最多3次）#最好放到Executor外面,由调用者控制
- 提交任务结果到aggregator
- 任务结果解析 #暂不实施，返回rawOutput,用户自行解析


#### 异步执行器 （asyncExecutor）
- **职责**：非阻塞执行任务，通过通道提交和返回结果。
- **组合设计**：
  - 内部复用同步`Executor`逻辑。
  - 通过`workers`控制并发度。
  - 默认 `workers`: 10
  - 任务队列容量: 100
  - 单路由器最大并发连接数: 3（防止单个路由器过载）


#### 结果上报 （aggregator）
- 合并结果并上报
- 触发上报条件
  - 缓冲区达到 100 条结果，或
  - 最近一次上报后超过 5 秒。


### 2.3 关键时序流程
#### 配置同步时序
```mermaid
sequenceDiagram
    participant Zabbix
    participant ConfigSyncer
    participant Manager
    participant RouterScheduler
    participant IntervalQueue

    ConfigSyncer->>Zabbix: 定期拉取Line配置
    Manager->>ConfigSyncer: 订阅专线变更通知
    Manager->>ConfigSyncer: 拉取全量专线列表
    Manager->>Manager: 初始化、维护路由器专线关系表
    Manager->>RouterScheduler: 创建实例，绑定相关Line
    RouterScheduler->>RouterScheduler: 按Interval给专线分类
    RouterScheduler->>IntervalQueue: 按Interval创建不同的Queue
    ConfigSyncer-->>Manager: 专线变更时广播
    Manager->>ConfigSyncer: 周期获取全量专线列表
    Manager->>RouterScheduler: 更新绑定的Line
    RouterScheduler->>IntervalQueue: 变更关联的Line
```

#### 任务执行时序
```mermaid
sequenceDiagram
    participant Queue
    participant RouterScheduler
    participant Executor
    participant Registry
    participant Aggregator

    %% 任务分发与执行
    Queue->>Registry: 生成任务命令（PingTask）
    Registry->>Queue: 返回命令（"ping 10.0.0.1"）
    Queue->>Executor: 提交任务（Task）
    Queue-->>Executor: 提供合并后的任务列表
    Executor->>Executor: 申请连接（GetConn）

    Executor->>scrapligo.Channel: 执行命令
    scrapligo.Channel-->>Executor: 返回输出
    Executor->>Registry: 解析输出
    Registry-->>Executor: 返回Result
    Executor->>Aggregator: 提交结果
    Executor->>Executor: 释放连接（ReleaseConn）
```


#### 异步执行流程
```mermaid
sequenceDiagram
    participant Queue as IntervalTaskQueue
    participant Scheduler as RouterScheduler
    participant Executor
    participant Pool as ConnectionPool

    Note over Queue: 定时触发（如30秒）
    Queue->>Scheduler: 发送execNotify信号
    Scheduler->>Pool: 获取连接（GetWithRetry）
    Pool-->>Scheduler: 返回Connection
    Scheduler->>Executor: ExecuteWithConn(conn, tasks)
    Executor->>Pool: 使用连接执行命令
    Executor-->>Scheduler: 返回Result[]
    Scheduler->>Aggregator: 上报结果
    Scheduler->>Pool: 释放连接

#### Ping执行流程
```mermaid
sequenceDiagram
    participant T as TestCase
    participant G as TaskGenerator
    participant P as ConnectionPool
    participant D as ScrapliDriver
    participant TK as PingTask

    T->>G: Build("ping", params, "scrapli")
    G->>P: GetDriver("10.0.0.1", "scrapli")
    P-->>G: EnhancedDriver(protocol="scrapli")
    G->>TK: SupportsProtocol("scrapli")?
    TK-->>G: true
    G->>TK: GenerateInteractiveEvents(params)
    TK-->>G: events
    G->>D: Execute(events)
    D-->>G: result
    G-->>T: result

#### 典型工作流
sequenceDiagram
    participant User
    participant Generator as TaskGenerator
    participant Pool as ConnectionPool
    participant Driver as EnhancedDriver
    participant Task as PingTask

    User->>Generator: Build("ping", params)
    Generator->>Pool: GetDriver("10.0.0.1", "scrapli")
    Pool-->>Generator: EnhancedDriver(Scrapli)
    Generator->>Task: SupportsProtocol("scrapli")?
    Task-->>Generator: true
    Generator->>Task: GenerateInteractiveEvents(params)
    Task-->>Generator: events
    Generator->>Driver: Execute(events)
    Driver->>ScrapliDriver: SendInteractive(events)
    ScrapliDriver-->>Driver: result
    Driver-->>User: Result


###  任务合并规则
1. **可合并条件**：
   - 相同路由器平台（如 `cisco_iosxe`）。
   - 相同任务类型（如均为 `PingTask`）。
   - 参数兼容（如 `repeat` 和 `timeout` 值相同）。
2. **合并策略**：
   - 批量生成命令（如 `ping ip1 ip2 ip3`）。
   - 解析输出时按顺序拆分结果。
3. **Fallback**：若任务未实现 `BatchTask`，自动降级为单任务模式。


## 3. 核心数据结构
### 专线配置
```go
type Line struct {
    ID       string
    IP       string
    Interval time.Duration
    Router   Router
    	Hash     uint64 // Line信息的hash，用于比对是否有变化
}
var DefaultInterval time.Duration = 3 * time.Minute
```

### 路由器信息
```go
type Router struct {
	ID       string
    IP       string
    Username string
    Password string  // 加密存储
    Platform string  // 平台类型
    Protocol string  // "scrapli channel" "ssh" 或 "netconf"
}
```

### 管理器（Manager）
```go
type Manager struct {
	configSyncer *syncer.ConfigSyncer
	schedulers   map[string]Scheduler     // key: routerIP
	routerLines  map[string][]syncer.Line // key: routerIP
	registry     task.Registry
	mu           sync.Mutex
	stopChan     chan struct{}
	wg           sync.WaitGroup
}
```
### 配置同步器 (ConfigSyncer)
// 引用github.com/charlesren/zapix
```go
type ConfigSyncer struct {
    client       *zapix.ZabbixClient
    lines       map[string]Line  // 当前全量配置
    version     int64           // 单调递增版本号
    subscribers []chan<- LineChangeEvent    // 订阅者列表
    mu          sync.RWMutex    // 读写锁替代互斥锁
	syncInterval time.Duration
    lastSyncTime time.Time // 记录最后一次同步时间
    ctx context.Context
   	cancel       context.CancelFunc
	stopOnce     sync.Once
	stopped      bool
}
const (
    LineCreate ChangeType = iota + 1
    LineUpdate
    LineDelete
)
type LineChangeEvent struct {
    Type    ChangeType
    Line   Line
}
// Events 返回只读通道供用户使用
func (s *Subscription) Events() <-chan LineChangeEvent {
	return s.events
}
type Subscription struct {
	events chan LineChangeEvent // 内部使用双向通道
	cs     *ConfigSyncer
	cancel context.CancelFunc
	once   sync.Once
}
```

### 路由器调度器（RouterScheduler）
```go
type IntervalTaskQueue struct {
	interval   time.Duration
	lines      []syncer.Line // 改为值存储
	mu         sync.Mutex
	execNotify chan struct{} // 执行信号通道
	stopChan   chan struct{} // 新增停止通道
	ticker     *time.Ticker  // 内置调度器
}

type RouterScheduler struct {
	router     *syncer.Router
	lines      []syncer.Line
	connection *connection.ConnectionPool
	queues     map[time.Duration]*IntervalTaskQueue
	executor   *task.Executor
	stopChan   chan struct{}
	wg         sync.WaitGroup
	mu         sync.Mutex
}
type Scheduler interface {
	OnLineCreated(line syncer.Line)     // 专线创建
	OnLineUpdated(old, new syncer.Line) // 专线更新（提供新旧值）
	OnLineDeleted(line syncer.Line)     // 专线删除
	OnLineReset(lines []syncer.Line)    // 专线重置
	Stop()
	Start()
}
```
### 执行器
```go
type SyncExecutor struct {
	// 无连接池等状态字段
	taskTimeout time.Duration // 单任务超时时间
}
func (e *SyncExecutor) Execute(
	ctx context.Context,
	conn connection.ProtocolDriver,
	platform string,
	taskType string,
	params map[string]interface{},
) (Result, error) {
	return Result{}, nil
}
```
### 异步执行器
```go
type AsyncExecutor struct {
	taskChan chan asyncTask // 任务通道
	workers  int            // 并发worker数量
	wg       sync.WaitGroup // 任务组同步
	stopChan chan struct{}  // 停止信号
}
//work执行任务复用SyncExecutor执行逻辑
func (e *AsyncExecutor) worker() {
			result, err := NewSyncExecutor().Execute(
				task.ctx,
				task.conn,
				task.platform,
				task.taskType,
				task.params,
			)
}
```
### 连接池
```go
type ConnectionPool interface {
    Get(routerIP string, protocol string) (ProtocolDriver, error)
    Release(driver ProtocolDriver)
}
type ProtocolDriver interface {
    SendCommands(commands []string) (string, error)
    Close() error
}
type ScrapliDriver struct {
	channel *scrapligo.Channel
}


// 连接池获取示例（自动选择协议）
driver, err := pool.Get(ctx, router.IP, router.Protocol)
```


### 任务接口规范
```go
type ProtocolCapability struct {
	Protocol     string   // "ssh"或"scrapli"
	CommandTypes []string // ["commands", "interactive_event"]
}

type PlatformSupport struct {
	Platform string               // "cisco_iosxe"
	Params   map[string]ParamSpec // 平台特有参数规范
}

type TaskMeta struct {
	Type            string
	ProtocolSupport []ProtocolCapability
	Platforms       []PlatformSupport
}

type Task interface {
	// 元信息
	Meta() TaskMeta

	// 命令生成（动态适配协议和平台）
	Generate(protocolType string, commandType string, platform string, params map[string]interface{}) (interface{}, error)

	// 结果解析
	ParseOutput(protocolType string, commandType string, platform string, rawOutput interface{}) (Result, error)
}


//ParamSpec用于参数校验，与 `Registry` 交互
type ParamSpec struct {
    Name     string                 // 参数名（如 "target_ip"）
    Type     string                 // 类型（"string"/"int"/"duration"）
    Required bool                   // 是否必填
    Default  interface{}            // 默认值
    Validate func(interface{}) error // 校验函数
}

```

### 平台适配实现
```go
type PlatformHandler interface {
    GenerateCommand(params map[string]interface{}) (string, error)
    ParseOutput(output string) (Result, error)
}

var platformHandlers = map[string]PlatformHandler{
    "cisco_iosxe": &CiscoPingHandler{},
    "huawei_vrp":  &HuaweiPingHandler{},
}
type CiscoIOSXEHandler struct {
   DefaultEvents []*scrapligo.Event // 预定义交互事件
}

func (h *CiscoIOSXEHandler) GenerateCommand(params map[string]interface{}) ([]*scrapligo.Event, error) {
  return h.DefaultEvents, nil // 返回scrapligo事件序列
}



```
### 注册任务
- 通过 `init()` 函数在模块加载时注册任务：
```go
// Registry 定义任务注册与发现的接口
type Registry interface {
    Register(meta TaskMeta) error
    Discover(taskType, platform, protocol, commandType string) (Task, error)
    ListPlatforms(platform string) []string
}

// DefaultRegistry 默认实现（原Registry结构体重命名）
type DefaultRegistry struct {
    tasks map[string]TaskMeta
    mu    sync.RWMutex
}

```

### 任务结果
```go
type Result struct {
    Success bool
    Data    map[string]interface{}  // 指标数据（如延迟、丢包率）
    Error   *TaskError              // 错误详情（可选）
}

```


## 4.数据流与错误处理




### 错误处理
```go
type TaskError struct {
    Code    string                 // 错误码（如 "INVALID_PARAMS"）
    Message string                 // 用户友好描述
    Details map[string]interface{} // 上下文信息
}
```
| 错误类型         | 处理方式                 |
|------------------|--------------------------|
| 连接申请失败     | 标记任务失败，触发告警并重试（最多3次）。 |
| 参数校验失败     | 拒绝任务并记录日志       |
| 平台不支持       | 标记失败并触发告警       |
| 执行超时         | 终止任务并释放连接       |
| 上报失败         | 本地缓存结果，下次批次重试（最多3次） |


## 5. 测试方案
### 单元测试
- 参数校验逻辑。
- 命令生成器测试。
- 结果解析器测试。

### 集成测试
- 完整流程测试：
  1. 模拟Zabbix配置下发。
  2. 验证任务调度。
  3. 检查结果上报。

## 6. 扩展设计
### 新增平台支持
1. 实现平台Handler接口。
2. 注册到任务仓库。
3. 添加平台测试用例。

### 新增检测类型
1. 实现Task接口。
2. 定义参数规范。
3. 注册任务实现。

type ChangeType uint8

const (
	LineCreate ChangeType = iota + 1
	LineUpdate
	LineDelete
)

type LineChangeEvent struct {
	Type    ChangeType
	Line    Line  // 事件关联的专线数据
	Version int64 // 配置版本号
}
type Subscriber chan<- LineChangeEvent // 强类型的只写通道

// syncer/syncer.go
type ConfigSyncer struct {
	client      *zapix.Client
	lines       map[string]Line // 当前全量配置
	version     int64           // 单调递增版本号
	subscribers []Subscriber    // 订阅者列表
	mu          sync.RWMutex    // 读写锁替代互斥锁
}

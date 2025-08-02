package syncer

import (
	"context"
	"encoding/binary"
	"hash/fnv"
	"sync"
	"time"

	"github.com/charlesren/zapix"
)

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
	client       *zapix.ZabbixClient
	lines        map[string]Line // 当前全量配置
	version      int64           // 单调递增版本号
	subscribers  []Subscriber    // 订阅者列表
	mu           sync.RWMutex    // 读写锁替代互斥锁
	syncInterval time.Duration
	lastSyncTime time.Time // 记录最后一次同步时间
	ctx          context.Context
	cancel       context.CancelFunc
	stopOnce     sync.Once
	stopped      bool
}

type Line struct {
	ID       string
	IP       string
	Interval time.Duration
	Router   Router
	Hash     uint64 // Line信息的hash，用于比对是否有变化
}

func (l *Line) ComputeHash() {
	h := fnv.New64a()
	binary.Write(h, binary.LittleEndian, []byte(l.ID))
	binary.Write(h, binary.LittleEndian, []byte(l.IP))
	binary.Write(h, binary.LittleEndian, int64(l.Interval))
	binary.Write(h, binary.LittleEndian, []byte(l.Router.IP))
	binary.Write(h, binary.LittleEndian, []byte(l.Router.Username))
	binary.Write(h, binary.LittleEndian, []byte(l.Router.Password)) // [注] 实际生产环境应排除
	binary.Write(h, binary.LittleEndian, []byte(l.Router.Platform))
	l.Hash = h.Sum64()
}

type Router struct {
	IP       string
	Username string
	Password string
	Platform string
}

// 从Zabbix API获取路由器详细信息
func FetchRouterDetails(ip string) (*Router, error) {
	// 实际实现中调用Zabbix API的host.get接口
	return &Router{
		IP:       ip,
		Username: "admin", // 示例值
		Password: "password",
		Platform: "cisco_iosxe",
	}, nil
}

var ProxyIP string = "1.1.1.1"
var SelectTag string = "TempType"
var SelectValue string = "ddl"

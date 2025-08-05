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

// syncer/syncer.go
type ConfigSyncer struct {
	client       Client                   // 使用接口
	lines        map[string]Line          // 当前全量配置
	version      int64                    // 单调递增版本号
	subscribers  []chan<- LineChangeEvent // 订阅者列表
	mu           sync.RWMutex             // 读写锁替代互斥锁
	syncInterval time.Duration
	lastSyncTime time.Time // 记录最后一次同步时间
	ctx          context.Context
	cancel       context.CancelFunc
	stopOnce     sync.Once
	stopped      bool
}
type Client interface {
	GetProxyFormHost(ip string) ([]zapix.ProxyObject, error)
	HostGet(params zapix.HostGetParams) ([]zapix.HostObject, error)
	// 其他必要方法...
}

type Line struct {
	ID       string        //预留，为CMDB相应编号,需要从host的macros里获取，为{$LINE_ID}的值
	IP       string        //专线IP, 对应zabbix主机host.Host
	Interval time.Duration //检查间隔，需要从host的macros里获取，为{$LINE_CHECK_INTERVAL}的值
	Router   Router
	Hash     uint64 // Line信息的hash，用于比对是否有变化
}

var DefaultInterval time.Duration = 3 * time.Minute

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
	IP       string //eage router,需要从host的macros里获取，为{$LINE_ROUTER_IP}的值
	Username string //路由器用户名，需要从host的macros里获取，为{$LINE_ROUTER_USERNAME}的值
	Password string //路由器密码，需要从host的macros里获取，为{$LINE_ROUTER_PASSWORD}的值
	Platform string //路由器操作系统平台（`cisco_iosxe`、`cisco_iosxr`、`cisco_nxos`、`h3c_comware`、`huawei_vrp`)，需要从host的macros里获取，为{$LINE_ROUTER_PLATFORM}的值
	Protocol string //路由器driver协议 "scrapli-channel" "ssh" 或 "netconf",需要从host的macros里获取，为{$LINE_ROUTER_PROTOCOL}的值
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
var LineSelectTag string = "TempType"
var LineSelectValue string = "ddl"

// Subscription 封装订阅的通道和取消逻辑
type Subscription struct {
	events chan LineChangeEvent
	cs     *ConfigSyncer
	cancel context.CancelFunc
	once   sync.Once
}

package batch

import (
	"container/heap"
	"sync"
	"time"

	"github.com/yourusername/zabbix_ddl_monitor/internal/config"
	"github.com/yourusername/zabbix_ddl_monitor/internal/router"
	"github.com/yourusername/zabbix_ddl_monitor/internal/task"
)

const maxRetries = 3

type RouterQueue struct {
	lines      PriorityQueue
	mu         sync.Mutex
	connPool   *router.ConnectionPool
	taskReg    *task.Registry
	aggregator *aggregator.Aggregator
	compressor *aggregator.ResultCompressor
	closeChan  chan struct{}
}

type RouterGroupScheduler struct {
	routers    map[string]*RouterQueue
	globalMu   sync.Mutex
	connPool   *router.ConnectionPool
	taskReg    *task.Registry
	aggregator *aggregator.Aggregator
}

func NewRouterGroupScheduler(
	taskReg *task.Registry,
	connPool *router.ConnectionPool,
	aggregator *aggregator.Aggregator,
) *RouterGroupScheduler {
	return &RouterGroupScheduler{
		routers:    make(map[string]*RouterQueue),
		connPool:   connPool,
		taskReg:    taskReg,
		aggregator: aggregator,
	}
}

func (s *RouterGroupScheduler) AddLine(line *config.Line) {
	s.globalMu.Lock()
	defer s.globalMu.Unlock()

	routerIP := line.Router.IP
	if _, exists := s.routers[routerIP]; !exists {
		// 连接池预热
		if conn, err := s.connPool.GetConnection(line.Router); err == nil {
			s.connPool.ReleaseConnection(conn)
		}

		q := &RouterQueue{
			connPool:   s.connPool,
			taskReg:    s.taskReg,
			aggregator: s.aggregator,
			compressor: aggregator.NewCompressor(),
			closeChan:  make(chan struct{}),
		}
		s.routers[routerIP] = q
		go q.start()
	}

	s.routers[routerIP].mu.Lock()
	heap.Push(&s.routers[routerIP].lines, line)
	s.routers[routerIP].mu.Unlock()
}

func (q *RouterQueue) start() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			q.processReadyLines()
		case <-q.closeChan:
			return
		}
	}
}

func (q *RouterQueue) processReadyLines() {
	q.mu.Lock()
	defer q.mu.Unlock()

	now := time.Now()
	var batch []*config.Line

	for len(q.lines) > 0 && q.lines[0].NextRun.Before(now) {
		batch = append(batch, heap.Pop(&q.lines).(*config.Line))
	}

	if len(batch) > 0 {
		go q.executeBatch(batch)
	}
}

func (q *RouterQueue) executeBatch(lines []*config.Line) {
	router := lines[0].Router
	conn, err := q.connPool.GetConnection(router)
	if err != nil {
		q.retryLines(lines, true)
		return
	}
	defer q.connPool.ReleaseConnection(conn)

	// 批量执行
	var results []aggregator.Result
	for _, line := range lines {
		cmd := q.taskReg.Get("ping").Prepare(router.Platform, line.IP)
		output, err := conn.SendCommand(cmd)
		if err != nil {
			results = append(results, aggregator.Result{LineID: line.ID, Success: false})
			continue
		}
		parsed, _ := q.taskReg.Get("ping").Parse(output)
		results = append(results, aggregator.Result{
			LineID:  line.ID,
			Success: parsed.Success,
			Latency: parsed.Latency,
		})
	}

	// 压缩上报
	for _, res := range results {
		q.compressor.Add(router.IP, res)
	}
	q.compressor.Flush(q.aggregator.Report)
	q.retryLines(lines, false)
}

func (q *RouterQueue) retryLines(lines []*config.Line, isRetry bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for _, line := range lines {
		if isRetry {
			line.RetryCount++
			if line.RetryCount >= maxRetries {
				continue
			}
			line.NextRun = time.Now().Add(time.Second << line.RetryCount)
		} else {
			line.RetryCount = 0
			line.NextRun = time.Now().Add(line.Interval)
		}
		heap.Push(&q.lines, line)
	}
}

func (s *RouterGroupScheduler) Stop() {
	s.globalMu.Lock()
	defer s.globalMu.Unlock()

	for _, q := range s.routers {
		close(q.closeChan)
	}
}

type PriorityQueue []*config.Line

func (pq PriorityQueue) Len() int            { return len(pq) }
func (pq PriorityQueue) Less(i, j int) bool  { return pq[i].NextRun.Before(pq[j].NextRun) }
func (pq PriorityQueue) Swap(i, j int)       { pq[i], pq[j] = pq[j], pq[i] }
func (pq *PriorityQueue) Push(x interface{}) { *pq = append(*pq, x.(*config.Line)) }
func (pq *PriorityQueue) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	*pq = old[:n-1]
	return item
}

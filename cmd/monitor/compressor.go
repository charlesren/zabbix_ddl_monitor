package aggregator

import "sync"

type ResultCompressor struct {
	mu      sync.Mutex
	results map[string][]Result // Key: RouterIP
}

func NewCompressor() *ResultCompressor {
	return &ResultCompressor{
		results: make(map[string][]Result),
	}
}

func (c *ResultCompressor) Add(routerIP string, res Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.results[routerIP] = append(c.results[routerIP], res)
}

func (c *ResultCompressor) Flush(reportFunc func([]Result)) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for routerIP, results := range c.results {
		if len(results) > 0 {
			go reportFunc(results)
			delete(c.results, routerIP)
		}
	}
}

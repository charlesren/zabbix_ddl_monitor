package aggregator

import "log"

type Result struct {
	LineID  string
	Success bool
	Latency float64
}

type Aggregator struct {
	reportChan chan []Result
}

func New() *Aggregator {
	agg := &Aggregator{
		reportChan: make(chan []Result, 100),
	}
	go agg.start()
	return agg
}

func (a *Aggregator) Report(results []Result) {
	a.reportChan <- results
}

func (a *Aggregator) start() {
	for batch := range a.reportChan {
		// 实际上报逻辑（如调用Zabbix API）
		log.Printf("上报结果: %+v", batch)
	}
}

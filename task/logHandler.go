package task

import "github.com/charlesren/ylog"

// LogHandler 日志处理器
type LogHandler struct{}

func (h *LogHandler) HandleResult(events []ResultEvent) error {
	for _, event := range events {
		if event.Success {
			ylog.Infof("result", "✓ %s ping %s success (duration: %v)",
				event.RouterIP, event.IP, event.Duration)
		} else {
			ylog.Warnf("result", "✗ %s ping %s failed: %s (duration: %v)",
				event.RouterIP, event.IP, event.Error, event.Duration)
		}
	}
	return nil
}

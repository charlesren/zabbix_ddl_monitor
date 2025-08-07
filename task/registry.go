package task

import (
	"fmt"
	"sync"
)

type Registry interface {
	Register(meta TaskMeta) error
	Discover(taskType, platform, protocol, commandType string) (Task, error)
	ListPlatforms(platform string) []string
}

// DefaultRegistry 默认实现
type DefaultRegistry struct {
	tasks map[string]TaskMeta
	mu    sync.RWMutex
}

// 确保DefaultRegistry实现Registry接口
var _ Registry = (*DefaultRegistry)(nil)

func NewDefaultRegistry() Registry {
	return &DefaultRegistry{tasks: make(map[string]TaskMeta)}
}

// Register 注册任务（层级化）
func (r *DefaultRegistry) Register(meta TaskMeta) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tasks[meta.Type]; exists {
		return fmt.Errorf("task '%s' already registered", meta.Type)
	}
	r.tasks[meta.Type] = meta
	return nil
}

// Discover 发现任务实现（严格层级匹配）
func (r *DefaultRegistry) Discover(taskType, platform, protocol, commandType string) (Task, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	meta, exists := r.tasks[taskType]
	if !exists {
		return nil, fmt.Errorf("task type '%s' not found", taskType)
	}

	for _, p := range meta.Platforms {
		if p.Platform == platform {
			for _, proto := range p.Protocols {
				if proto.Protocol == protocol {
					for _, cmd := range proto.CommandTypes {
						if cmd.CommandType == commandType {
							return cmd.ImplFactory(), nil
						}
					}
				}
			}
		}
	}
	return nil, fmt.Errorf("no matching task for platform '%s', protocol '%s', command type '%s'", platform, protocol, commandType)
}

// ListPlatforms 查询平台支持的任务
func (r *DefaultRegistry) ListPlatforms(platform string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var supported []string
	for name, meta := range r.tasks {
		for _, p := range meta.Platforms {
			if p.Platform == platform {
				supported = append(supported, name)
				break
			}
		}
	}
	return supported
}

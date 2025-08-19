package task

import (
	"fmt"
	"sync"

	"github.com/charlesren/ylog"
)

type Registry interface {
	Register(meta TaskMeta) error
	Discover(taskType TaskType, platform Platform, protocol Protocol, commandType CommandType) (Task, error)
	ListPlatforms(platform Platform) []TaskType
}

type DefaultRegistry struct {
	tasks map[TaskType]TaskMeta
	mu    sync.RWMutex
}

var _ Registry = (*DefaultRegistry)(nil)

func NewDefaultRegistry() Registry {
	return &DefaultRegistry{
		tasks: make(map[TaskType]TaskMeta),
	}
}

func (r *DefaultRegistry) Register(meta TaskMeta) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tasks[meta.Type]; exists {
		ylog.Warnf("registry", "task %s already registered", meta.Type)
		return fmt.Errorf("task '%s' already registered", meta.Type)
	}
	r.tasks[meta.Type] = meta
	ylog.Infof("registry", "registered new task: %s", meta.Type)
	return nil
}

func (r *DefaultRegistry) Discover(
	taskType TaskType,
	platform Platform,
	protocol Protocol,
	commandType CommandType,
) (Task, error) {
	ylog.Debugf("registry", "discovering task: type=%s, platform=%s, protocol=%s, cmdType=%s",
		taskType, platform, protocol, commandType)

	r.mu.RLock()
	defer r.mu.RUnlock()

	meta, exists := r.tasks[taskType]
	if !exists {
		ylog.Errorf("registry", "task type not found: %s", taskType)
		return nil, fmt.Errorf("task type '%s' not found", taskType)
	}

	for _, p := range meta.Platforms {
		ylog.Debugf("registry", "registered platform: %s (type: %T)", p.Platform, p.Platform)
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
	ylog.Debugf("registry", "registered platforms for %s: %v", taskType, meta.Platforms)
	return nil, fmt.Errorf("no matching task for platform '%s', protocol '%s', command type '%s'", platform, protocol, commandType)
}

func (r *DefaultRegistry) ListPlatforms(platform Platform) []TaskType {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var supported []TaskType
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

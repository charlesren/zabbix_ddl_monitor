package task

import (
	"fmt"
	"sync"
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
		return fmt.Errorf("task '%s' already registered", meta.Type)
	}
	r.tasks[meta.Type] = meta
	return nil
}

func (r *DefaultRegistry) Discover(
	taskType TaskType,
	platform Platform,
	protocol Protocol,
	commandType CommandType,
) (Task, error) {
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

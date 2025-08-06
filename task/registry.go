package task

import (
	"sync"
)

type Registry struct {
	tasks sync.Map // map[string]Task
}

var globalRegistry = &Registry{}

func GlobalRegistry() *Registry {
	return globalRegistry
}

// 注册任务（需实现Task接口）
func (r *Registry) Register(task Task) error {
	meta := task.Meta()
	if _, loaded := r.tasks.LoadOrStore(meta.Name, task); loaded {
		return ErrTaskExists
	}
	return nil
}

// 发现任务支持的能力
func (r *Registry) GetTaskCapabilities(name string) (TaskMeta, error) {
	if task, ok := r.tasks.Load(name); ok {
		return task.(Task).Meta(), nil
	}
	return TaskMeta{}, ErrTaskNotFound
}

// 根据条件筛选任务
func (r *Registry) FindTasks(filter func(TaskMeta) bool) []string {
	var matches []string
	r.tasks.Range(func(key, value interface{}) bool {
		if filter(value.(Task).Meta()) {
			matches = append(matches, key.(string))
		}
		return true
	})
	return matches
}

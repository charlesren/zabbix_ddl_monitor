package task

import (
	"fmt"
	"sync"
)

// Registry manages task implementations and their metadata
type Registry struct {
	mu    sync.RWMutex
	tasks map[string]Task
	specs map[string]map[string]string // Task name -> parameter specifications
}

// NewRegistry creates a new task registry
func NewRegistry() *Registry {
	return &Registry{
		tasks: make(map[string]Task),
		specs: make(map[string]map[string]string),
	}
}

// Register adds a new task implementation to the registry
func (r *Registry) Register(name string, t Task) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tasks[name]; exists {
		return fmt.Errorf("task already registered: %s", name)
	}

	r.tasks[name] = t
	r.specs[name] = t.ParamsSpec()
	return nil
}

// Get retrieves a task implementation by name
func (r *Registry) Get(name string) (Task, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	task, exists := r.tasks[name]
	if !exists {
		return nil, fmt.Errorf("task not found: %s", name)
	}
	return task, nil
}

// GetParamsSpec returns parameter specifications for a task
func (r *Registry) GetParamsSpec(name string) (map[string]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	spec, exists := r.specs[name]
	if !exists {
		return nil, fmt.Errorf("task not found: %s", name)
	}
	return spec, nil
}

// ValidateParams checks if provided parameters match task requirements
func (r *Registry) ValidateParams(name string, params map[string]interface{}) error {
	spec, err := r.GetParamsSpec(name)
	if err != nil {
		return err
	}

	for param := range params {
		if _, ok := spec[param]; !ok {
			return fmt.Errorf("unexpected parameter: %s", param)
		}
	}
	return nil
}

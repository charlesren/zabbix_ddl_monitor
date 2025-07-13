package task

type Task interface {
    Prepare(platform string) string
    Parse(output string) (string, error)
}

type Registry struct {
    tasks map[string]Task
}

func NewRegistry() *Registry {
    return &Registry{
        tasks: make(map[string]Task),
    }
}

func (r *Registry) Register(name string, t Task) {
    r.tasks[name] = t
}

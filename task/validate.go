package task

import "fmt"

func validateParams(params map[string]interface{}, meta TaskMeta, platform string) error {
	var specs map[string]ParamSpec
	for _, p := range meta.Platforms {
		if p.Platform == platform {
			specs = p.Params
			break
		}
	}

	for name, spec := range specs {
		value, exists := params[name]
		if !exists {
			if spec.Required {
				return fmt.Errorf("param '%s' is required", name)
			}
			continue
		}

		if spec.Validator != nil {
			if err := spec.Validator(value); err != nil {
				return fmt.Errorf("invalid param '%s': %v", name, err)
			}
		}
	}
	return nil
}

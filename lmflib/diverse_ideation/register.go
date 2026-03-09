package diverse_ideation

import (
	"fmt"

	"zoa/runtime"
)

func RegisterFunctions(registry *runtime.Registry) error {
	if registry == nil {
		return fmt.Errorf("registry is nil")
	}
	return registry.Register(diverseIdeationFunction())
}

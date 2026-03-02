package diverse_ideation

import (
	"fmt"

	lmfrt "zoa/lmfrt"
)

func RegisterFunctions(registry *lmfrt.Registry) error {
	if registry == nil {
		return fmt.Errorf("registry is nil")
	}
	return registry.Register(diverseIdeationFunction())
}

package intrinsic

import (
	"fmt"

	"zoa/runtime"
)

func RegisterMixins(registry *runtime.Registry) error {
	if registry == nil {
		return fmt.Errorf("registry is nil")
	}
	return registry.RegisterMixin(zoaSystemMixin())
}

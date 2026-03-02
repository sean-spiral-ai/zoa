package intrinsic

import "fmt"

import lmfrt "zoa/lmfrt"

func RegisterFunctions(registry *lmfrt.Registry) error {
	if registry == nil {
		return fmt.Errorf("registry is nil")
	}
	return registry.Register(intrinsicModifyCodebase())
}

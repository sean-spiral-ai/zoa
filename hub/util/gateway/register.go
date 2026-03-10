package gateway

import (
	"fmt"

	"zoa/runtime"
)

func RegisterFunctions(registry *runtime.Registry) error {
	if registry == nil {
		return fmt.Errorf("registry is nil")
	}
	if err := registry.Register(initFunction()); err != nil {
		return err
	}
	if err := registry.Register(recvFunction()); err != nil {
		return err
	}
	if err := registry.Register(pumpFunction()); err != nil {
		return err
	}
	if err := registry.Register(outboxSinceFunction()); err != nil {
		return err
	}
	if err := registry.Register(outboxMaxIDFunction()); err != nil {
		return err
	}
	return nil
}

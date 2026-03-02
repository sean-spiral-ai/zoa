package gateway

import (
	"fmt"

	lmfrt "zoa/lmfrt"
)

func RegisterFunctions(registry *lmfrt.Registry) error {
	if registry == nil {
		return fmt.Errorf("registry is nil")
	}
	if err := registry.Register(initFunction()); err != nil {
		return err
	}
	if err := registry.Register(recvFunction()); err != nil {
		return err
	}
	if err := registry.Register(outboxSinceFunction()); err != nil {
		return err
	}
	return nil
}

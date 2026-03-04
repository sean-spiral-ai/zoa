package md_to_pdf

import (
	"fmt"
	"path/filepath"
	"runtime"

	lmfrt "zoa/lmfrt"
)

func RegisterFunctions(registry *lmfrt.Registry) error {
	if registry == nil {
		return fmt.Errorf("registry is nil")
	}
	return registry.Register(mdToPDFFunction(assetsDir()))
}

func assetsDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "assets")
}

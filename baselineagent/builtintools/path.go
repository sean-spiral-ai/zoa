package builtintools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type PathResolver struct {
	root string
}

func NewPathResolver(root string) (*PathResolver, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	return &PathResolver{root: filepath.Clean(abs)}, nil
}

func (p *PathResolver) Root() string {
	return p.root
}

func (p *PathResolver) Resolve(path string) (string, error) {
	expanded := expandPath(path)
	if strings.TrimSpace(expanded) == "" {
		return "", fmt.Errorf("path is required")
	}
	var abs string
	if filepath.IsAbs(expanded) {
		abs = filepath.Clean(expanded)
	} else {
		abs = filepath.Clean(filepath.Join(p.root, expanded))
	}

	rel, err := filepath.Rel(p.root, abs)
	if err != nil {
		return "", fmt.Errorf("compute relative path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside workspace root %q", path, p.root)
	}
	return abs, nil
}

func expandPath(path string) string {
	if path == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	return path
}

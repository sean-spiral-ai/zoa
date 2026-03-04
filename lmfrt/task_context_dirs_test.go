package lmfrt_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	lmfrt "zoa/lmfrt"
)

func TestTaskContextGetStateDirCreatesPersistentNamespacePath(t *testing.T) {
	root := t.TempDir()
	sqlitePath := filepath.Join(root, "state.db")

	tc, err := lmfrt.NewTaskContext(context.Background(), lmfrt.TaskContextOptions{
		CWD:        root,
		SQLitePath: sqlitePath,
		Namespace:  "md_to_pdf",
	})
	if err != nil {
		t.Fatalf("create task context: %v", err)
	}

	stateDir, err := tc.GetStateDir()
	if err != nil {
		t.Fatalf("get state dir: %v", err)
	}
	want := filepath.Join(root, "namespace_state", "md_to_pdf")
	if stateDir != want {
		t.Fatalf("state dir mismatch: got %q want %q", stateDir, want)
	}
	info, err := os.Stat(stateDir)
	if err != nil {
		t.Fatalf("stat state dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("state dir is not a directory: %q", stateDir)
	}

	again, err := tc.GetStateDir()
	if err != nil {
		t.Fatalf("get state dir second call: %v", err)
	}
	if again != stateDir {
		t.Fatalf("state dir changed across calls: first=%q second=%q", stateDir, again)
	}

	marker := filepath.Join(stateDir, "marker.txt")
	if err := os.WriteFile(marker, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if err := tc.Close(); err != nil {
		t.Fatalf("close task context: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("expected state marker to persist after close: %v", err)
	}
}

func TestTaskContextGetStateDirRequiresNamespace(t *testing.T) {
	tc, err := lmfrt.NewTaskContext(context.Background(), lmfrt.TaskContextOptions{
		CWD:        t.TempDir(),
		SQLitePath: filepath.Join(t.TempDir(), "state.db"),
	})
	if err != nil {
		t.Fatalf("create task context: %v", err)
	}
	defer func() { _ = tc.Close() }()

	_, err = tc.GetStateDir()
	if err == nil {
		t.Fatalf("expected error when namespace is unset")
	}
	if !strings.Contains(err.Error(), "namespace is not set") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTaskContextGetAssetsDir(t *testing.T) {
	t.Run("returns configured assets path", func(t *testing.T) {
		root := t.TempDir()
		assetsDir := filepath.Join(root, "assets")
		if err := os.MkdirAll(assetsDir, 0o755); err != nil {
			t.Fatalf("create assets dir: %v", err)
		}

		tc, err := lmfrt.NewTaskContext(context.Background(), lmfrt.TaskContextOptions{
			CWD:        root,
			SQLitePath: filepath.Join(root, "state.db"),
			Namespace:  "md_to_pdf",
			AssetsDir:  assetsDir,
		})
		if err != nil {
			t.Fatalf("create task context: %v", err)
		}
		defer func() { _ = tc.Close() }()

		got, err := tc.GetAssetsDir()
		if err != nil {
			t.Fatalf("get assets dir: %v", err)
		}
		if got != assetsDir {
			t.Fatalf("assets dir mismatch: got %q want %q", got, assetsDir)
		}
	})

	t.Run("errors when assets path is not configured", func(t *testing.T) {
		tc, err := lmfrt.NewTaskContext(context.Background(), lmfrt.TaskContextOptions{
			CWD:        t.TempDir(),
			SQLitePath: filepath.Join(t.TempDir(), "state.db"),
			Namespace:  "md_to_pdf",
		})
		if err != nil {
			t.Fatalf("create task context: %v", err)
		}
		defer func() { _ = tc.Close() }()

		_, err = tc.GetAssetsDir()
		if err == nil {
			t.Fatalf("expected error when assets dir is unset")
		}
		if !strings.Contains(err.Error(), "assets dir is not configured") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestTaskContextGetTmpDirCreatesUniqueDirsAndCleansOnClose(t *testing.T) {
	tc, err := lmfrt.NewTaskContext(context.Background(), lmfrt.TaskContextOptions{
		CWD:        t.TempDir(),
		SQLitePath: filepath.Join(t.TempDir(), "state.db"),
		Namespace:  "md_to_pdf",
	})
	if err != nil {
		t.Fatalf("create task context: %v", err)
	}

	tmp1, err := tc.GetTmpDir()
	if err != nil {
		t.Fatalf("get tmp dir first call: %v", err)
	}
	tmp2, err := tc.GetTmpDir()
	if err != nil {
		t.Fatalf("get tmp dir second call: %v", err)
	}
	if tmp1 == tmp2 {
		t.Fatalf("expected unique tmp dirs, both were %q", tmp1)
	}
	for _, dir := range []string{tmp1, tmp2} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("stat tmp dir %q: %v", dir, err)
		}
		if !strings.HasPrefix(filepath.Base(dir), "lmfrt-md_to_pdf-") {
			t.Fatalf("tmp dir %q does not use namespace prefix", dir)
		}
		if err := os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write scratch file in %q: %v", dir, err)
		}
	}

	if err := tc.Close(); err != nil {
		t.Fatalf("close task context: %v", err)
	}

	for _, dir := range []string{tmp1, tmp2} {
		_, err := os.Stat(dir)
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected tmp dir %q to be removed on close, stat err=%v", dir, err)
		}
	}
}

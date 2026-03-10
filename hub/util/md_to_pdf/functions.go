package md_to_pdf

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"zoa/runtime"
)

func mdToPDFFunction(assets string) *runtime.Function {
	return &runtime.Function{
		ID:        "md_to_pdf.md_to_pdf",
		WhenToUse: "Convert a Markdown file to a styled PDF document.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"markdown_path": map[string]any{
					"type":        "string",
					"description": "Absolute path to the input .md file.",
				},
				"pdf_path": map[string]any{
					"type":        "string",
					"description": "Absolute path for the output .pdf file.",
				},
			},
			"required": []string{"markdown_path", "pdf_path"},
		},
		AssetsDir: assets,
		Exec:     execMdToPDF,
	}
}

func execMdToPDF(tc *runtime.TaskContext, input map[string]any) (map[string]any, error) {
	mdPath, _ := input["markdown_path"].(string)
	mdPath = strings.TrimSpace(mdPath)
	if mdPath == "" {
		return nil, fmt.Errorf("markdown_path is required")
	}
	pdfPath, _ := input["pdf_path"].(string)
	pdfPath = strings.TrimSpace(pdfPath)
	if pdfPath == "" {
		return nil, fmt.Errorf("pdf_path is required")
	}

	if err := ensureStateDirReady(tc); err != nil {
		return nil, fmt.Errorf("prepare state dir: %w", err)
	}

	stateDir, err := tc.GetStateDir()
	if err != nil {
		return nil, err
	}

	stylePath := filepath.Join(stateDir, "style.css")
	pythonBin := filepath.Join(stateDir, "venv", "bin", "python")
	scriptPath := filepath.Join(stateDir, "md_to_pdf.py")

	cmd := exec.CommandContext(tc.Context(), pythonBin, scriptPath, mdPath, pdfPath, "--style", stylePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("md_to_pdf.py: %w\n%s", err, string(out))
	}

	resultPath := strings.TrimSpace(string(out))
	if resultPath == "" {
		resultPath = pdfPath
	}

	return map[string]any{
		"pdf_path": resultPath,
	}, nil
}

func ensureStateDirReady(tc *runtime.TaskContext) error {
	stateDir, err := tc.GetStateDir()
	if err != nil {
		return err
	}
	assetsDir, err := tc.GetAssetsDir()
	if err != nil {
		return err
	}

	// Copy script and stylesheet if missing or stale.
	for _, name := range []string{"md_to_pdf.py", "style.css"} {
		src := filepath.Join(assetsDir, name)
		dst := filepath.Join(stateDir, name)
		if err := copyFileIfNewer(src, dst); err != nil {
			return fmt.Errorf("copy %s: %w", name, err)
		}
	}

	// Create venv if not present.
	venvDir := filepath.Join(stateDir, "venv")
	pythonBin := filepath.Join(venvDir, "bin", "python")
	if _, err := os.Stat(pythonBin); err != nil {
		cmd := exec.CommandContext(tc.Context(), "python3", "-m", "venv", venvDir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("create venv: %w\n%s", err, string(out))
		}
	}

	// Install deps if marker missing.
	marker := filepath.Join(stateDir, ".deps_installed")
	if _, err := os.Stat(marker); err != nil {
		pipBin := filepath.Join(venvDir, "bin", "pip")
		cmd := exec.CommandContext(tc.Context(), pipBin, "install", "markdown", "weasyprint")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("install deps: %w\n%s", err, string(out))
		}
		_ = os.WriteFile(marker, []byte("ok"), 0o644)
	}

	return nil
}

func copyFileIfNewer(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	dstInfo, err := os.Stat(dst)
	if err == nil && !srcInfo.ModTime().After(dstInfo.ModTime()) {
		return nil // dst is up to date
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

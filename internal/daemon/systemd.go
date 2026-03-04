package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/template"
)

const serviceName = "zoa"

// InstallConfig holds the parameters baked into the launcher script.
type InstallConfig struct {
	SourceDir         string  // Go source tree (default: os.Getwd())
	CWD               string  // --cwd for zoa slack
	SessionDir        string  // --session-dir
	Model             string  // --model
	MaxTurns          int     // --max-turns
	Temperature       float64 // --temperature
	TimeoutSec        int     // --timeout
	PollMs            int     // --poll-ms
	LogLevel          string  // --log-level
	DebugLogComponent string  // --debug-log-component
	TraceHTTPAddr     string  // --trace-http-addr for runtime trace control

	// Defaults used to suppress flags that match.
	DefaultModel             string
	DefaultMaxTurns          int
	DefaultTemperature       float64
	DefaultTimeoutSec        int
	DefaultPollMs            int
	DefaultLogLevel          string
	DefaultDebugLogComponent string
	DefaultCWD               string
	DefaultSessionDir        string
	DefaultTraceHTTPAddr     string
}

// Action represents a systemctl lifecycle verb.
type Action string

const (
	ActionStart   Action = "start"
	ActionStop    Action = "stop"
	ActionRestart Action = "restart"
)

// StatusInfo holds parsed status fields from systemctl show.
type StatusInfo struct {
	ActiveState    string
	SubState       string
	MainPID        int
	ExecMainStatus int
	ExecMainCode   string
}

func (s *StatusInfo) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "ActiveState:    %s\n", s.ActiveState)
	fmt.Fprintf(&b, "SubState:       %s\n", s.SubState)
	fmt.Fprintf(&b, "MainPID:        %d\n", s.MainPID)
	fmt.Fprintf(&b, "ExecMainCode:   %s\n", s.ExecMainCode)
	fmt.Fprintf(&b, "ExecMainStatus: %d\n", s.ExecMainStatus)
	return b.String()
}

// Install writes the launcher script and systemd unit, then enables and starts the service.
func Install(cfg InstallConfig) error {
	if cfg.SourceDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve source dir: %w", err)
		}
		cfg.SourceDir = wd
	}

	if err := os.MkdirAll(zoaBinDir(), 0755); err != nil {
		return fmt.Errorf("create bin dir: %w", err)
	}

	slackFlags := buildSlackFlags(cfg)
	launcherData := launcherTemplateData{
		SourceDir:  cfg.SourceDir,
		BinaryPath: filepath.Join(zoaBinDir(), "zoa"),
		SlackFlags: slackFlags,
	}
	if err := writeTemplate(launcherPath(), launcherTmpl, launcherData, 0755); err != nil {
		return fmt.Errorf("write launcher: %w", err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", launcherPath())

	unitData := unitTemplateData{
		WorkingDirectory: cfg.SourceDir,
		LauncherPath:     launcherPath(),
		PATH:             os.Getenv("PATH"),
	}
	unitDir := filepath.Dir(unitFilePath())
	if err := os.MkdirAll(unitDir, 0755); err != nil {
		return fmt.Errorf("create unit dir: %w", err)
	}
	if err := writeTemplate(unitFilePath(), unitTmpl, unitData, 0644); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", unitFilePath())

	if err := ensureLinger(); err != nil {
		return err
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", serviceName); err != nil {
		return err
	}
	if err := systemctl("restart", serviceName); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "service installed and started")
	return nil
}

// Uninstall stops and disables the service, removes generated files, and reloads systemd.
func Uninstall() error {
	// disable --now ignores errors if the service doesn't exist
	_ = systemctl("disable", "--now", serviceName)

	removed := 0
	for _, p := range []string{unitFilePath(), launcherPath()} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", p, err)
		} else if err == nil {
			fmt.Fprintf(os.Stderr, "removed %s\n", p)
			removed++
		}
	}

	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if removed == 0 {
		fmt.Fprintln(os.Stderr, "nothing to uninstall")
	} else {
		fmt.Fprintln(os.Stderr, "service uninstalled")
	}
	return nil
}

// RunAction executes a systemctl lifecycle action (start, stop, restart).
func RunAction(action Action) error {
	return systemctl(string(action), serviceName)
}

// Status returns parsed service status from systemctl show.
func Status() (*StatusInfo, error) {
	out, err := systemctlOutput("show", serviceName,
		"--property=ActiveState,SubState,MainPID,ExecMainStatus,ExecMainCode")
	if err != nil {
		return nil, err
	}
	info := &StatusInfo{}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "ActiveState":
			info.ActiveState = v
		case "SubState":
			info.SubState = v
		case "MainPID":
			info.MainPID, _ = strconv.Atoi(v)
		case "ExecMainStatus":
			info.ExecMainStatus, _ = strconv.Atoi(v)
		case "ExecMainCode":
			info.ExecMainCode = v
		}
	}
	return info, nil
}

// Logs execs into journalctl so the user gets direct TTY streaming.
// extraArgs are passed through (e.g. -f, -n 100).
func Logs(extraArgs []string) error {
	args := []string{"--user", "-u", serviceName}
	args = append(args, extraArgs...)
	bin, err := exec.LookPath("journalctl")
	if err != nil {
		return fmt.Errorf("journalctl not found: %w", err)
	}
	return syscall.Exec(bin, append([]string{"journalctl"}, args...), os.Environ())
}

// --- helpers ---

func zoaDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".zoa")
}

func zoaBinDir() string {
	return filepath.Join(zoaDir(), "bin")
}

func launcherPath() string {
	return filepath.Join(zoaDir(), "zoa-launcher.sh")
}

func unitFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", serviceName+".service")
}

func systemctl(args ...string) error {
	full := append([]string{"--user"}, args...)
	cmd := exec.Command("systemctl", full...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func systemctlOutput(args ...string) (string, error) {
	full := append([]string{"--user"}, args...)
	out, err := exec.Command("systemctl", full...).Output()
	if err != nil {
		return "", fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func ensureLinger() error {
	user := os.Getenv("USER")
	if user == "" {
		user = "root"
	}
	// Check if linger is already enabled.
	lingerFile := filepath.Join("/var/lib/systemd/linger", user)
	if _, err := os.Stat(lingerFile); err == nil {
		return nil
	}
	cmd := exec.Command("loginctl", "enable-linger", user)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("enable linger: %w", err)
	}
	fmt.Fprintln(os.Stderr, "enabled loginctl linger")
	return nil
}

func writeTemplate(path string, tmplText string, data any, perm os.FileMode) error {
	t, err := template.New("").Parse(tmplText)
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer f.Close()
	return t.Execute(f, data)
}

func buildSlackFlags(cfg InstallConfig) string {
	var parts []string
	add := func(flag, value, def string) {
		if value != "" && value != def {
			parts = append(parts, flag+" "+value)
		}
	}
	addInt := func(flag string, value, def int) {
		if value != def && value != 0 {
			parts = append(parts, flag+" "+strconv.Itoa(value))
		}
	}
	addFloat := func(flag string, value, def float64) {
		if value != def {
			parts = append(parts, flag+" "+strconv.FormatFloat(value, 'f', -1, 64))
		}
	}

	add("--log-level", cfg.LogLevel, cfg.DefaultLogLevel)
	add("--debug-log-component", cfg.DebugLogComponent, cfg.DefaultDebugLogComponent)
	add("--cwd", cfg.CWD, cfg.DefaultCWD)
	add("--session-dir", cfg.SessionDir, cfg.DefaultSessionDir)
	add("--model", cfg.Model, cfg.DefaultModel)
	add("--trace-http-addr", cfg.TraceHTTPAddr, cfg.DefaultTraceHTTPAddr)
	addInt("--max-turns", cfg.MaxTurns, cfg.DefaultMaxTurns)
	addFloat("--temperature", cfg.Temperature, cfg.DefaultTemperature)
	addInt("--timeout", cfg.TimeoutSec, cfg.DefaultTimeoutSec)
	addInt("--poll-ms", cfg.PollMs, cfg.DefaultPollMs)

	return strings.Join(parts, " ")
}

// --- templates ---

type launcherTemplateData struct {
	SourceDir  string
	BinaryPath string
	SlackFlags string
}

var launcherTmpl = `#!/usr/bin/env bash
set -euo pipefail
cd "{{.SourceDir}}"
BINARY="{{.BinaryPath}}"
echo "zoa-launcher: building from {{.SourceDir}}" >&2
go build -o "$BINARY" ./cmd/zoa
echo "zoa-launcher: starting zoa slack" >&2
exec "$BINARY" slack{{if .SlackFlags}} {{.SlackFlags}}{{end}}
`

type unitTemplateData struct {
	WorkingDirectory string
	LauncherPath     string
	PATH             string
}

var unitTmpl = `[Unit]
Description=Zoa Slack Bridge
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory={{.WorkingDirectory}}
ExecStart={{.LauncherPath}}
Restart=always
RestartSec=5
SuccessExitStatus=42
KillMode=process
Environment=PATH={{.PATH}}

[Install]
WantedBy=default.target
`

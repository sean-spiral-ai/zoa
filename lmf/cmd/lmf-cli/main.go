package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"codexagentbase/lmf/functions"
	lmf "codexagentbase/lmf/runtime"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	registry := functions.NewRegistry()
	ctx := context.Background()

	subcmd := os.Args[1]
	switch subcmd {
	case "list":
		runList(registry)
	case "run":
		if err := runOne(ctx, registry, os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	case "run-all-tests":
		if err := runAllTests(ctx, registry, os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Println(`lmf-cli

Commands:
  list
  run <function-id> [--input-json '{"k":"v"}']
  run-all-tests [--intrinsic-cwd <path>] [--intrinsic-instruction <text>]`)
}

func runList(registry *lmf.Registry) {
	items := registry.List()
	for _, fn := range items {
		fmt.Printf("%s\t%s\n", fn.ID, fn.Description)
	}
}

func runOne(ctx context.Context, registry *lmf.Registry, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: run <function-id> [--input-json ...]")
	}
	functionID := args[0]
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	inputJSON := fs.String("input-json", "{}", "JSON object input")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	input, err := parseInputJSON(*inputJSON)
	if err != nil {
		return err
	}

	fn, ok := registry.Get(functionID)
	if !ok {
		return fmt.Errorf("unknown function: %s", functionID)
	}

	result, runErr := lmf.Run(ctx, fn, input)
	printJSON(result)
	if runErr != nil {
		return runErr
	}
	return nil
}

func runAllTests(ctx context.Context, registry *lmf.Registry, args []string) error {
	fs := flag.NewFlagSet("run-all-tests", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	intrinsicCWD := fs.String("intrinsic-cwd", "", "cwd used for intrinsic.modify_codebase")
	intrinsicInstruction := fs.String("intrinsic-instruction", "", "instruction for intrinsic.modify_codebase")
	if err := fs.Parse(args); err != nil {
		return err
	}

	type tc struct {
		name       string
		functionID string
		input      map[string]any
		expectErr  bool
		allowSkip  bool
		skipReason string
	}

	tests := []tc{
		{
			name:       "programmatic guard fails",
			functionID: "test.programmatic_guard",
			input:      map[string]any{"value": 0},
			expectErr:  true,
		},
		{
			name:       "programmatic guard passes",
			functionID: "test.programmatic_guard",
			input:      map[string]any{"value": 5},
			expectErr:  false,
		},
		{
			name:       "nlcondition funny fails",
			functionID: "test.nlcondition_funny",
			input:      map[string]any{"text": "Install dependencies and run go test."},
			expectErr:  true,
		},
		{
			name:       "typed nlexec echo passes",
			functionID: "test.nlexec_typed_echo",
			input:      map[string]any{"name": "sean"},
			expectErr:  false,
		},
		{
			name:       "nlexec context memory passes",
			functionID: "test.nlexec_context_memory",
			input:      map[string]any{},
			expectErr:  false,
		},
		{
			name:       "nlcondition isolation passes",
			functionID: "test.nlcondition_isolation",
			input:      map[string]any{},
			expectErr:  false,
		},
	}

	if strings.TrimSpace(*intrinsicCWD) != "" && strings.TrimSpace(*intrinsicInstruction) != "" {
		tests = append(tests, tc{
			name:       "intrinsic modify codebase",
			functionID: "intrinsic.modify_codebase",
			input: map[string]any{
				"cwd":         *intrinsicCWD,
				"instruction": *intrinsicInstruction,
			},
			expectErr: false,
		})
	} else {
		tests = append(tests, tc{
			name:       "intrinsic modify codebase",
			functionID: "intrinsic.modify_codebase",
			allowSkip:  true,
			skipReason: "pass --intrinsic-cwd and --intrinsic-instruction to run",
		})
	}

	type summaryRow struct {
		Name    string `json:"name"`
		Status  string `json:"status"`
		Details string `json:"details,omitempty"`
	}

	summaries := []summaryRow{}
	failures := 0

	for _, test := range tests {
		if test.allowSkip {
			summaries = append(summaries, summaryRow{Name: test.name, Status: "skipped", Details: test.skipReason})
			continue
		}

		fn, ok := registry.Get(test.functionID)
		if !ok {
			failures++
			summaries = append(summaries, summaryRow{Name: test.name, Status: "failed", Details: "function not found"})
			continue
		}

		_, err := lmf.Run(ctx, fn, test.input)
		if (err != nil) != test.expectErr {
			failures++
			summaries = append(summaries, summaryRow{
				Name:    test.name,
				Status:  "failed",
				Details: fmt.Sprintf("unexpected error expectation mismatch (err=%v)", err != nil),
			})
			continue
		}

		status := "ok"
		details := ""
		if err != nil {
			status = "ok(expected failure)"
			details = err.Error()
		}
		summaries = append(summaries, summaryRow{Name: test.name, Status: status, Details: details})
	}

	sort.Slice(summaries, func(i, j int) bool { return summaries[i].Name < summaries[j].Name })
	printJSON(map[string]any{
		"failures": failures,
		"results":  summaries,
	})
	if failures > 0 {
		return errors.New("one or more tests failed")
	}
	return nil
}

func parseInputJSON(input string) (map[string]any, error) {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(input), &parsed); err != nil {
		return nil, fmt.Errorf("invalid input-json: %w", err)
	}
	if parsed == nil {
		parsed = map[string]any{}
	}
	return parsed, nil
}

func printJSON(v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Printf("{\"error\":%q}\n", err.Error())
		return
	}
	fmt.Println(string(b))
}

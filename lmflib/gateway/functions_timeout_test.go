package gateway

import (
	"strings"
	"testing"
)

func TestInboundPumpInputFromRecvInputPreservesExplicitZeroTimeout(t *testing.T) {
	out, err := inboundPumpInputFromRecvInput(map[string]any{
		"timeout_sec": 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	raw, ok := out["timeout_sec"]
	if !ok {
		t.Fatalf("expected timeout_sec to be preserved")
	}
	val, ok := raw.(int)
	if !ok || val != 0 {
		t.Fatalf("unexpected timeout_sec value: %#v", raw)
	}
}

func TestInboundPumpInputFromRecvInputRejectsNegativeTimeout(t *testing.T) {
	_, err := inboundPumpInputFromRecvInput(map[string]any{
		"timeout_sec": -1,
	})
	if err == nil {
		t.Fatalf("expected error for negative timeout")
	}
}

func TestRecvSchemaDocumentsTimeoutDefaultAndUnlimited(t *testing.T) {
	fn := recvFunction()
	properties, ok := fn.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("missing input properties")
	}
	timeoutProp, ok := properties["timeout_sec"].(map[string]any)
	if !ok {
		t.Fatalf("missing timeout_sec property")
	}
	desc, _ := timeoutProp["description"].(string)
	if !strings.Contains(desc, "3600") {
		t.Fatalf("timeout description should mention default 3600: %q", desc)
	}
	if !strings.Contains(strings.ToLower(desc), "0") || !strings.Contains(strings.ToLower(desc), "disable") {
		t.Fatalf("timeout description should mention 0 disables timeout: %q", desc)
	}
}

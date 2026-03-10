package llm

import (
	"strings"
	"testing"

	"zoa/internal/keys"
)

func requireLiveProviderToken(t *testing.T, envVar string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping live provider smoke test in -short mode")
	}
	if !liveAPITestsEnabled {
		t.Skip("live provider smoke tests are disabled; re-run with -tags liveapi")
	}
	value := strings.TrimSpace(keys.ResolveWithNearestDotEnv("", envVar))
	if value == "" {
		t.Skipf("%s is not set", envVar)
	}
	return value
}

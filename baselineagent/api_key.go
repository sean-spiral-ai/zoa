package baselineagent

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

const GeminiAPIKeyEnvVar = "GEMINI_API_KEY"

// ResolveAPIKey returns an API key from explicit input, environment, or nearest .env.
func ResolveAPIKey(explicit string) (string, bool) {
	if key := strings.TrimSpace(explicit); key != "" {
		return key, true
	}

	if key := strings.TrimSpace(os.Getenv(GeminiAPIKeyEnvVar)); key != "" {
		return key, true
	}

	if key, ok := loadKeyFromNearestDotEnv(GeminiAPIKeyEnvVar); ok {
		return key, true
	}
	return "", false
}

func loadKeyFromNearestDotEnv(key string) (string, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}

	for {
		candidate := filepath.Join(dir, ".env")
		if value, ok := readDotEnvValue(candidate, key); ok {
			return value, true
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}

func readDotEnvValue(path string, key string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.TrimSpace(k) != key {
			continue
		}
		value := strings.TrimSpace(v)
		value = strings.Trim(value, `"'`)
		if value == "" {
			return "", false
		}
		return value, true
	}
	return "", false
}

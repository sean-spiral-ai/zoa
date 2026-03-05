package keys

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LoadDotEnv loads a dotenv file into process environment variables without
// overriding keys that are already set in the environment.
func LoadDotEnv(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			return fmt.Errorf("%s:%d: expected KEY=VALUE", path, lineNo)
		}

		key := strings.TrimSpace(line[:eq])
		if key == "" {
			return fmt.Errorf("%s:%d: empty key", path, lineNo)
		}
		rawValue := strings.TrimSpace(line[eq+1:])
		value, err := parseDotEnvValue(rawValue)
		if err != nil {
			return fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("%s:%d: set %s: %w", path, lineNo, key, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan %s: %w", path, err)
	}
	return nil
}

func Resolve(explicit string, envVars ...string) string {
	if val := strings.TrimSpace(explicit); val != "" {
		return val
	}
	for _, name := range envVars {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if val := strings.TrimSpace(os.Getenv(name)); val != "" {
			return val
		}
	}
	return ""
}

// ResolveWithNearestDotEnv resolves credentials in this order:
// explicit value, process environment, then nearest .env walking upward from cwd.
func ResolveWithNearestDotEnv(explicit string, envVars ...string) string {
	if val := Resolve(explicit, envVars...); val != "" {
		return val
	}
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(dir, ".env")
		for _, envVar := range envVars {
			envVar = strings.TrimSpace(envVar)
			if envVar == "" {
				continue
			}
			if value, ok := readDotEnvKey(candidate, envVar); ok {
				return value
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func readDotEnvKey(path string, key string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		name := strings.TrimSpace(line[:eq])
		if name != key {
			continue
		}
		rawValue := strings.TrimSpace(line[eq+1:])
		value, err := parseDotEnvValue(rawValue)
		if err != nil {
			return "", false
		}
		if strings.TrimSpace(value) == "" {
			return "", false
		}
		return strings.TrimSpace(value), true
	}
	return "", false
}

func parseDotEnvValue(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if strings.HasPrefix(raw, `"`) {
		unquoted, err := strconv.Unquote(raw)
		if err != nil {
			return "", fmt.Errorf("invalid quoted value: %w", err)
		}
		return unquoted, nil
	}
	if strings.HasPrefix(raw, `'`) {
		if len(raw) < 2 || !strings.HasSuffix(raw, `'`) {
			return "", fmt.Errorf("invalid single-quoted value")
		}
		return raw[1 : len(raw)-1], nil
	}
	if idx := strings.Index(raw, " #"); idx >= 0 {
		raw = strings.TrimSpace(raw[:idx])
	}
	return raw, nil
}

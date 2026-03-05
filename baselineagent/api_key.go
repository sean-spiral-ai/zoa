package baselineagent

import (
	"strings"

	"zoa/internal/keys"
)

const GeminiAPIKeyEnvVar = "GEMINI_API_KEY"
const AnthropicOAuthTokenEnvVar = "ANTHROPIC_OAUTH_TOKEN"

// ResolveAPIKey returns default-model credentials from explicit input, environment, or nearest .env.
func ResolveAPIKey(explicit string) (string, bool) {
	return ResolveCredential(explicit, DefaultModel)
}

// ResolveCredential returns model credentials from explicit input, environment, or nearest .env.
func ResolveCredential(explicit string, model string) (string, bool) {
	envVar := RequiredCredentialEnvVarForModel(model)
	if envVar == "" {
		return "", false
	}
	if key := strings.TrimSpace(keys.ResolveWithNearestDotEnv(explicit, envVar)); key != "" {
		return key, true
	}
	return "", false
}

func RequiredCredentialEnvVarForModel(model string) string {
	switch InferProviderFromModel(model) {
	case ProviderAnthropic:
		return AnthropicOAuthTokenEnvVar
	case ProviderGemini:
		return GeminiAPIKeyEnvVar
	default:
		return ""
	}
}

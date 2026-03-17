package model

import (
	"strings"

	"zoa/internal/keys"
)

const GeminiAPIKeyEnvVar = "GEMINI_API_KEY"
const AnthropicAPIKeyEnvVar = "ANTHROPIC_API_KEY"
const AnthropicSetupTokenEnvVar = "ANTHROPIC_SETUP_TOKEN"

// ResolveAPIKey returns default-model credentials from explicit input, environment, or nearest .env.
func ResolveAPIKey(explicit string) (string, bool) {
	return ResolveCredential(explicit, DefaultModel)
}

// ResolveCredential returns model credentials from explicit input, environment, or nearest .env.
func ResolveCredential(explicit string, model string) (string, bool) {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit), true
	}
	switch InferProviderFromModel(model) {
	case ProviderAnthropic:
		if key := strings.TrimSpace(keys.ResolveWithNearestDotEnv("", AnthropicAPIKeyEnvVar)); key != "" {
			return key, true
		}
		if token := strings.TrimSpace(keys.ResolveWithNearestDotEnv("", AnthropicSetupTokenEnvVar)); token != "" {
			return token, true
		}
		return "", false
	case ProviderGemini:
		if key := strings.TrimSpace(keys.ResolveWithNearestDotEnv("", GeminiAPIKeyEnvVar)); key != "" {
			return key, true
		}
		return "", false
	default:
		return "", false
	}
}

func RequiredCredentialEnvVarForModel(model string) string {
	switch InferProviderFromModel(model) {
	case ProviderAnthropic:
		return AnthropicAPIKeyEnvVar
	case ProviderGemini:
		return GeminiAPIKeyEnvVar
	default:
		return ""
	}
}

func MissingCredentialMessageForModel(model string) string {
	if InferProviderFromModel(model) == ProviderAnthropic {
		return AnthropicAPIKeyEnvVar + " or " + AnthropicSetupTokenEnvVar + " is required"
	}
	envVar := RequiredCredentialEnvVarForModel(model)
	if envVar == "" {
		return "credential is required"
	}
	return envVar + " is required"
}

package baselineagent

import topmodel "zoa/model"

const GeminiAPIKeyEnvVar = topmodel.GeminiAPIKeyEnvVar
const AnthropicOAuthTokenEnvVar = topmodel.AnthropicOAuthTokenEnvVar

func ResolveAPIKey(explicit string) (string, bool) {
	return topmodel.ResolveAPIKey(explicit)
}

func ResolveCredential(explicit string, model string) (string, bool) {
	return topmodel.ResolveCredential(explicit, model)
}

func RequiredCredentialEnvVarForModel(model string) string {
	return topmodel.RequiredCredentialEnvVarForModel(model)
}

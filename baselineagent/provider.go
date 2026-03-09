package baselineagent

import topmodel "zoa/model"

type Provider = topmodel.Provider

const (
	ProviderGemini    = topmodel.ProviderGemini
	ProviderAnthropic = topmodel.ProviderAnthropic
)

const (
	ModelClaudeSonnet46      = topmodel.ModelClaudeSonnet46
	ModelClaudeOpus46        = topmodel.ModelClaudeOpus46
	ModelGemini31ProPreview  = topmodel.ModelGemini31ProPreview
	ModelGemini3FlashPreview = topmodel.ModelGemini3FlashPreview
)

func SupportedModelNames() []string {
	return topmodel.SupportedModelNames()
}

func IsSupportedModel(model string) bool {
	return topmodel.IsSupportedModel(model)
}

func InferProviderFromModel(model string) Provider {
	return topmodel.InferProviderFromModel(model)
}

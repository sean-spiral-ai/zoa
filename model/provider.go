package model

import (
	"slices"
	"strings"
)

type Provider string

const (
	ProviderGemini    Provider = "gemini"
	ProviderAnthropic Provider = "anthropic"
)

const (
	ModelClaudeSonnet46      = "claude-sonnet-4-6"
	ModelClaudeOpus46        = "claude-opus-4-6"
	ModelGemini31ProPreview  = "gemini-3.1-pro-preview"
	ModelGemini3FlashPreview = "gemini-3-flash-preview"
)

var supportedModelNames = []string{
	ModelClaudeSonnet46,
	ModelClaudeOpus46,
	ModelGemini31ProPreview,
	ModelGemini3FlashPreview,
}

func (p Provider) Valid() bool {
	return p == ProviderGemini || p == ProviderAnthropic
}

func SupportedModelNames() []string {
	return slices.Clone(supportedModelNames)
}

func IsSupportedModel(model string) bool {
	normalized := strings.TrimSpace(model)
	for _, supported := range supportedModelNames {
		if normalized == supported {
			return true
		}
	}
	return false
}

func InferProviderFromModel(model string) Provider {
	switch strings.TrimSpace(model) {
	case ModelClaudeSonnet46, ModelClaudeOpus46:
		return ProviderAnthropic
	case ModelGemini31ProPreview, ModelGemini3FlashPreview:
		return ProviderGemini
	default:
		return Provider("")
	}
}

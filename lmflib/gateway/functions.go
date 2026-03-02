package gateway

import (
	"fmt"
	"strings"

	gatewayservice "zoa/gateway"
	lmfrt "zoa/lmfrt"
)

func RegisterFunctions(registry *lmfrt.Registry, service *gatewayservice.Service) error {
	if registry == nil {
		return fmt.Errorf("registry is nil")
	}
	if service == nil {
		return fmt.Errorf("service is nil")
	}
	if err := registry.Register(RecvFunction(service)); err != nil {
		return err
	}
	return nil
}

func RecvFunction(service *gatewayservice.Service) *lmfrt.Function {
	return &lmfrt.Function{
		ID:        "gateway.recv",
		WhenToUse: "Use at channel ingress to hand off any incoming user message into the persistent session queue (including slash command handling).",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"channel": map[string]any{"type": "string", "description": "Source channel identifier (defaults to tui)"},
				"message": map[string]any{"type": "string", "description": "Raw user message text"},
			},
			"required": []string{"message"},
		},
		Exec: func(_ *lmfrt.TaskContext, input map[string]any) (map[string]any, error) {
			channel, err := getString(input, "channel", false)
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(channel) == "" {
				channel = "tui"
			}
			message, err := getString(input, "message", true)
			if err != nil {
				return nil, err
			}
			result, err := service.Receive(channel, message)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"accepted":   result.Accepted,
				"message_id": result.MessageID,
				"decision":   result.Decision,
				"queue_len":  result.QueueLen,
			}, nil
		},
	}
}

func getString(in map[string]any, key string, required bool) (string, error) {
	raw, ok := in[key]
	if !ok || raw == nil {
		if required {
			return "", fmt.Errorf("missing required field: %s", key)
		}
		return "", nil
	}
	val, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	if required && strings.TrimSpace(val) == "" {
		return "", fmt.Errorf("%s cannot be empty", key)
	}
	return val, nil
}

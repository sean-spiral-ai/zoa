package gatewaychannel

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	Scheme         = "gatewaychannel"
	TransportTUI   = "tui"
	TransportSlack = "slack"

	QueryChannelID = "channel_id"
	TUIURI         = Scheme + "://" + TransportTUI
)

type Target struct {
	Raw            string
	Transport      string
	SlackChannelID string
}

func SlackURI(channelID string) string {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return Scheme + "://" + TransportSlack
	}
	values := url.Values{}
	values.Set(QueryChannelID, channelID)
	return Scheme + "://" + TransportSlack + "?" + values.Encode()
}

func Parse(raw string) (Target, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Target{}, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return Target{}, fmt.Errorf("parse gateway channel uri: %w", err)
	}
	if parsed.Scheme != Scheme {
		return Target{}, fmt.Errorf("unsupported gateway channel scheme %q", parsed.Scheme)
	}
	transport := strings.TrimSpace(parsed.Host)
	if transport == "" {
		return Target{}, fmt.Errorf("gateway channel transport is required")
	}
	target := Target{
		Raw:       raw,
		Transport: transport,
	}
	if transport == TransportSlack {
		target.SlackChannelID = strings.TrimSpace(parsed.Query().Get(QueryChannelID))
	}
	return target, nil
}

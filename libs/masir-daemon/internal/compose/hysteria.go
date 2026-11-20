package compose

import (
	"encoding/json"
	"strings"
)

// 3x-ui can copy its Hysteria server's HTTP masquerade into a client export.
// Xray uses it only on the listener. Remove it when composing a subscription
// outbound so cached configs and their IDs can be kept.
func stripHysteriaServerMasquerade(outbound map[string]json.RawMessage) {
	var protocol string
	for key, raw := range outbound {
		if strings.EqualFold(key, "protocol") {
			if json.Unmarshal(raw, &protocol) != nil || !strings.EqualFold(protocol, "hysteria") {
				return
			}
		}
	}
	if protocol == "" {
		return
	}

	for key, raw := range outbound {
		if !strings.EqualFold(key, "streamSettings") {
			continue
		}
		var stream map[string]json.RawMessage
		if json.Unmarshal(raw, &stream) != nil {
			continue
		}
		streamChanged := false
		for transport, raw := range stream {
			if !strings.EqualFold(transport, "hysteriaSettings") {
				continue
			}
			var settings map[string]json.RawMessage
			if json.Unmarshal(raw, &settings) != nil {
				continue
			}
			changed := false
			for option := range settings {
				if strings.EqualFold(option, "masquerade") {
					delete(settings, option)
					changed = true
				}
			}
			if changed {
				stream[transport], _ = json.Marshal(settings)
				streamChanged = true
			}
		}
		if streamChanged {
			outbound[key], _ = json.Marshal(stream)
		}
	}
}

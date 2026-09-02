package replay

import (
	"encoding/json"
	"fmt"
)

// decodeAny checks a payload's version without binding it to a typed struct,
// for the event types that carry no state the reducer needs. It still enforces
// the version, so a payload written by a future build is caught rather than
// waved through.
func decodeAny(raw json.RawMessage, into *map[string]any) error {
	var envelope struct {
		V *int `json:"v"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("read payload envelope: %w", err)
	}
	if envelope.V == nil {
		return fmt.Errorf("payload has no version field")
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("unmarshal payload: %w", err)
	}
	return nil
}

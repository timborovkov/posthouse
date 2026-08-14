package pagination

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const version = 1

type envelope struct {
	Version  int             `json:"v"`
	Kind     string          `json:"kind"`
	Scope    string          `json:"scope"`
	Position json.RawMessage `json:"position"`
}

func Encode(kind string, scope any, position any) (string, error) {
	fingerprint, err := Fingerprint(scope)
	if err != nil {
		return "", err
	}
	encodedPosition, err := json.Marshal(position)
	if err != nil {
		return "", fmt.Errorf("encode cursor position: %w", err)
	}
	data, err := json.Marshal(envelope{Version: version, Kind: kind, Scope: fingerprint, Position: encodedPosition})
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func Decode(token string, kind string, scope any, position any) error {
	if token == "" {
		return nil
	}
	if len(token) > 64<<10 {
		return fmt.Errorf("cursor is too large")
	}
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return fmt.Errorf("invalid cursor encoding")
	}
	var decoded envelope
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("invalid cursor payload")
	}
	fingerprint, err := Fingerprint(scope)
	if err != nil {
		return err
	}
	if decoded.Version != version || decoded.Kind != kind || decoded.Scope != fingerprint {
		return fmt.Errorf("cursor does not belong to this query; restart pagination")
	}
	if err := json.Unmarshal(decoded.Position, position); err != nil {
		return fmt.Errorf("invalid cursor position")
	}
	return nil
}

func Fingerprint(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode cursor scope: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func PageSize(value, fallback, maximum int) (int, error) {
	if value == 0 {
		return fallback, nil
	}
	if value < 1 || value > maximum {
		return 0, fmt.Errorf("page_size must be between 1 and %d", maximum)
	}
	return value, nil
}

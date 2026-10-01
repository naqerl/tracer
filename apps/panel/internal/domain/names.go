package domain

import (
	"fmt"
	"strings"
)

const (
	maxCollectorNameLength = 128
	maxAppNameLength       = 128
	maxNotifierNameLength  = 64
	maxAPIKeyNameLength    = 64
)

func ValidateCollectorName(value string) error {
	return validateIdentifier("collector name", value, maxCollectorNameLength)
}

func ValidateAppName(value string) error {
	return validateIdentifier("app name", value, maxAppNameLength)
}

func ValidateNotifierName(value string) error {
	return validateName("name", value, maxNotifierNameLength)
}

func ValidateAPIKeyName(value string) error {
	return validateName("name", value, maxAPIKeyNameLength)
}

// ValidateCollectorAPIKeyName holds a collector key name to the collector
// identifier rules: the name becomes the collector segment of the install-script
// URL the panel hands out, and that endpoint validates the segment as a
// collector name.
func ValidateCollectorAPIKeyName(value string) error {
	return validateIdentifier("collector key name", value, maxAPIKeyNameLength)
}

func ValidateUserID(value int64) error {
	if value <= 0 {
		return fmt.Errorf("user ID is required")
	}
	return nil
}

func validateIdentifier(field, value string, maxLength int) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("%s is required", field)
	}
	if trimmed != value {
		return fmt.Errorf("%s must not have leading or trailing spaces", field)
	}
	if len(trimmed) > maxLength {
		return fmt.Errorf("%s must be <= %d characters", field, maxLength)
	}
	for _, r := range trimmed {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' {
			continue
		}
		return fmt.Errorf("%s contains unsupported character %q", field, r)
	}
	return nil
}

func validateName(field, value string, maxLength int) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("%s is required", field)
	}
	if trimmed != value {
		return fmt.Errorf("%s must not have leading or trailing spaces", field)
	}
	if len(trimmed) > maxLength {
		return fmt.Errorf("%s must be <= %d characters", field, maxLength)
	}
	return nil
}

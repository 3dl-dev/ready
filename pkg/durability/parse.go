// Package durability implements the Campfire Durability Convention v0.1.
// It parses and validates durability:max-ttl and durability:lifecycle beacon tags.
// Reference: https://github.com/campfire-net/agentic-internet/blob/main/docs/conventions/campfire-durability.md
package durability

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

// DurabilityResult holds the parsed and validated result of durability tags.
type DurabilityResult struct {
	Valid          bool
	MaxTTL         string   // normalized: "0" or "<N><unit>", empty if absent
	LifecycleType  string   // "persistent", "ephemeral", "bounded", or empty
	LifecycleValue string   // timeout or ISO 8601 date for ephemeral/bounded; empty otherwise
	Warnings       []string // non-fatal conformance warnings
	Error          string   // first fatal conformance error; non-empty means Valid=false
}

// ParseTagsAt parses a beacon tags array and returns the durability conformance
// result, using now as the reference time. It follows the conformance checker
// specification from §7 of the convention.
func ParseTagsAt(tags []string, now time.Time) (*DurabilityResult, error) {
	r := &DurabilityResult{Valid: true}

	var maxTTLTags []string
	var lifecycleTags []string
	var unknownDurabilityTags []string

	for _, tag := range tags {
		if !strings.HasPrefix(tag, "durability:") {
			continue
		}
		rest := tag[len("durability:"):]
		switch {
		case strings.HasPrefix(rest, "max-ttl:"):
			maxTTLTags = append(maxTTLTags, tag)
		case strings.HasPrefix(rest, "lifecycle:"):
			lifecycleTags = append(lifecycleTags, tag)
		default:
			unknownDurabilityTags = append(unknownDurabilityTags, tag)
		}
	}

	// §7 check 1: max-ttl cardinality
	if len(maxTTLTags) > 1 {
		r.Valid = false
		r.Error = "multiple durability:max-ttl tags — at most one permitted"
		return r, nil
	}

	// §7 check 2: max-ttl format
	if len(maxTTLTags) == 1 {
		value := maxTTLTags[0][len("durability:max-ttl:"):]
		normalized, warn, err := parseMaxTTLValue(value)
		if err != nil {
			r.Valid = false
			r.Error = err.Error()
			return r, nil
		}
		r.MaxTTL = normalized
		if warn != "" {
			r.Warnings = append(r.Warnings, warn)
		}
	}

	// §7 check 3: lifecycle cardinality
	if len(lifecycleTags) > 1 {
		r.Valid = false
		r.Error = "multiple durability:lifecycle tags — at most one permitted"
		return r, nil
	}

	// §7 check 4: lifecycle type
	if len(lifecycleTags) == 1 {
		value := lifecycleTags[0][len("durability:lifecycle:"):]
		lcType, lcValue, warn, err := parseLifecycleValue(value, now)
		if err != nil {
			r.Valid = false
			r.Error = err.Error()
			return r, nil
		}
		r.LifecycleType = lcType
		r.LifecycleValue = lcValue
		if warn != "" {
			r.Warnings = append(r.Warnings, warn)
		}
	}

	// §7 check 5: unknown durability namespace tags
	for _, tag := range unknownDurabilityTags {
		r.Warnings = append(r.Warnings, fmt.Sprintf("unknown tag in reserved durability: namespace: %s", tag))
	}

	return r, nil
}

// parseMaxTTLValue validates and normalizes a max-ttl duration string.
// Returns normalized value, optional warning, and error.
func parseMaxTTLValue(s string) (normalized string, warning string, err error) {
	if s == "0" {
		return "0", "", nil
	}

	// Check for negative sign
	if strings.HasPrefix(s, "-") {
		return "", "", fmt.Errorf("durability:max-ttl: negative or non-numeric value")
	}

	// Must end with a unit character
	if len(s) == 0 {
		return "", "", fmt.Errorf("durability:max-ttl: empty duration value")
	}

	unit := s[len(s)-1]
	validUnits := map[byte]bool{'s': true, 'm': true, 'h': true, 'd': true}
	if !validUnits[unit] {
		// Check if last char is a digit (missing unit)
		if unicode.IsDigit(rune(unit)) {
			return "", "", fmt.Errorf("durability:max-ttl: unknown unit '' — must be s, m, h, or d")
		}
		return "", "", fmt.Errorf("durability:max-ttl: unknown unit '%c' — must be s, m, h, or d", unit)
	}

	nStr := s[:len(s)-1]
	if len(nStr) == 0 {
		return "", "", fmt.Errorf("durability:max-ttl: missing duration value before unit")
	}

	// Reject leading zeros
	if len(nStr) > 1 && nStr[0] == '0' {
		return "", "", fmt.Errorf("durability:max-ttl: leading zero in duration value")
	}
	// Reject the standalone "0" with a unit (e.g. "0d")
	if nStr == "0" {
		return "", "", fmt.Errorf("durability:max-ttl: '0%c' is invalid — use '0' for keep-forever, or a positive integer with unit", unit)
	}

	// Must be all digits
	for _, c := range nStr {
		if !unicode.IsDigit(c) {
			return "", "", fmt.Errorf("durability:max-ttl: negative or non-numeric value")
		}
	}

	// Max 6 digits
	if len(nStr) > 6 {
		return "", "", fmt.Errorf("durability:max-ttl: duration value exceeds 6-digit maximum")
	}

	// Parse integer value to check 100-year threshold
	n := 0
	for _, c := range nStr {
		n = n*10 + int(c-'0')
	}

	// Convert to days for threshold check
	var days int
	switch unit {
	case 's':
		days = n / 86400
	case 'm':
		days = n / 1440
	case 'h':
		days = n / 24
	case 'd':
		days = n
	}

	if days > 36500 {
		warn := fmt.Sprintf("durability:max-ttl: %s exceeds 100 years — treated as keep-forever (0)", s)
		return "0", warn, nil
	}

	return s, "", nil
}

// parseLifecycleValue validates a lifecycle type string.
// Returns type, value, optional warning, and error.
func parseLifecycleValue(s string, now time.Time) (lcType, lcValue, warning string, err error) {
	switch {
	case s == "persistent":
		return "persistent", "", "", nil

	case strings.HasPrefix(s, "ephemeral:"):
		timeout := s[len("ephemeral:"):]
		if timeout == "" {
			return "", "", "", fmt.Errorf("durability:lifecycle: ephemeral timeout is empty — must be <N><unit>")
		}
		// ephemeral:0 is invalid
		if timeout == "0" {
			return "", "", "", fmt.Errorf("durability:lifecycle: ephemeral timeout must be a positive integer with unit — use lifecycle:persistent for no timeout")
		}
		// Parse the timeout as a duration
		_, _, parseErr := parseDurationForLifecycle(timeout)
		if parseErr != nil {
			return "", "", "", fmt.Errorf("durability:lifecycle: ephemeral timeout '%s' is invalid — %s", timeout, parseErr.Error())
		}
		return "ephemeral", timeout, "", nil

	case strings.HasPrefix(s, "bounded:"):
		dateStr := s[len("bounded:"):]
		t, parseErr := time.Parse(time.RFC3339, dateStr)
		if parseErr != nil {
			return "", "", "", fmt.Errorf("durability:lifecycle: bounded date '%s' is not valid ISO 8601 UTC", dateStr)
		}
		var warn string
		if t.Before(now) {
			warn = "durability:lifecycle:bounded date is in the past — campfire lifecycle has elapsed"
		}
		return "bounded", dateStr, warn, nil

	default:
		// Extract the type for error message
		typePart := s
		if idx := strings.Index(s, ":"); idx >= 0 {
			typePart = s[:idx]
		}
		return "", "", "", fmt.Errorf("durability:lifecycle: unknown type '%s' — must be persistent, ephemeral:<duration>, or bounded:<iso8601>", typePart)
	}
}

// parseDurationForLifecycle parses a duration string of the form <N><unit> for lifecycle values.
// It returns (n, unit, error). This is used for ephemeral timeout validation.
func parseDurationForLifecycle(s string) (n int, unit byte, err error) {
	if len(s) == 0 {
		return 0, 0, fmt.Errorf("empty duration")
	}

	if strings.HasPrefix(s, "-") {
		return 0, 0, fmt.Errorf("negative or non-numeric value")
	}

	u := s[len(s)-1]
	validUnits := map[byte]bool{'s': true, 'm': true, 'h': true, 'd': true}
	if !validUnits[u] {
		return 0, 0, fmt.Errorf("unknown unit '%c'", u)
	}

	nStr := s[:len(s)-1]
	if len(nStr) == 0 {
		return 0, 0, fmt.Errorf("missing value before unit")
	}
	if nStr == "0" {
		return 0, 0, fmt.Errorf("zero timeout")
	}
	if len(nStr) > 1 && nStr[0] == '0' {
		return 0, 0, fmt.Errorf("leading zero")
	}
	if len(nStr) > 6 {
		return 0, 0, fmt.Errorf("duration value exceeds 6-digit maximum")
	}
	for _, c := range nStr {
		if !unicode.IsDigit(c) {
			return 0, 0, fmt.Errorf("non-numeric character '%c'", c)
		}
		n = n*10 + int(c-'0')
	}

	return n, u, nil
}

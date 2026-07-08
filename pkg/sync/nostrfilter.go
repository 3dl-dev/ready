// NIP-01 filter matching (ready-797).
//
// matchesFilter evaluates a nostr event against the subset of NIP-01 filter
// fields rd's negentropy sync uses: kinds, authors, ids, and "#<tag>" single-
// letter tag filters. It mirrors how the relay selects its record set for a
// NEG-OPEN filter, so the LOCAL log is reduced to the same universe the relay
// reconciles over — otherwise the have/need diff would be skewed. A filter field
// that is absent matches everything; within a field the values are ORed; across
// fields they are ANDed (NIP-01 semantics).
package sync

import "github.com/campfire-net/ready/pkg/nostr"

func matchesFilter(e *nostr.Event, filter map[string]any) bool {
	for key, raw := range filter {
		switch key {
		case "kinds":
			if !kindMatches(e.Kind, raw) {
				return false
			}
		case "authors":
			if !stringInField(e.PubKey, raw) {
				return false
			}
		case "ids":
			if !stringInField(e.ID, raw) {
				return false
			}
		default:
			if len(key) == 2 && key[0] == '#' {
				if !tagMatches(e, key[1], raw) {
					return false
				}
			}
			// Unknown keys (e.g. since/until/limit) are ignored for local matching.
		}
	}
	return true
}

func kindMatches(kind int, raw any) bool {
	switch v := raw.(type) {
	case []int:
		for _, k := range v {
			if k == kind {
				return true
			}
		}
	case []any:
		for _, e := range v {
			switch n := e.(type) {
			case int:
				if n == kind {
					return true
				}
			case float64:
				if int(n) == kind {
					return true
				}
			}
		}
	}
	return false
}

func stringInField(s string, raw any) bool {
	switch v := raw.(type) {
	case []string:
		for _, x := range v {
			if x == s {
				return true
			}
		}
	case []any:
		for _, e := range v {
			if x, ok := e.(string); ok && x == s {
				return true
			}
		}
	}
	return false
}

// tagMatches reports whether the event has a tag whose name is the single letter
// `letter` and whose value is among the filter's values.
func tagMatches(e *nostr.Event, letter byte, raw any) bool {
	name := string(letter)
	for _, tag := range e.Tags {
		if len(tag) >= 2 && tag[0] == name {
			if stringInField(tag[1], raw) {
				return true
			}
		}
	}
	return false
}

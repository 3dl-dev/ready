package durability

import (
	"testing"
	"time"
)

// referenceTime is used for tests involving bounded date checks (§8.11).
// Set to 2026-03-28T00:00:00Z as specified in the convention.
var referenceTime = time.Date(2026, 3, 28, 0, 0, 0, 0, time.UTC)

func TestParseTagsVectors(t *testing.T) {
	tests := []struct {
		name           string
		tags           []string
		wantValid      bool
		wantMaxTTL     string
		wantLCType     string
		wantLCValue    string
		wantWarnings   []string
		wantErrContain string // substring expected in Error field
	}{
		// §8.1 Valid — Persistent campfire, keep forever
		{
			name: "8.1 persistent + keep forever",
			tags: []string{
				"category:infrastructure",
				"durability:max-ttl:0",
				"durability:lifecycle:persistent",
			},
			wantValid:   true,
			wantMaxTTL:  "0",
			wantLCType:  "persistent",
			wantLCValue: "",
		},
		// §8.2 Valid — Ephemeral swarm campfire
		{
			name: "8.2 ephemeral swarm",
			tags: []string{
				"category:infrastructure",
				"durability:max-ttl:4h",
				"durability:lifecycle:ephemeral:30m",
			},
			wantValid:   true,
			wantMaxTTL:  "4h",
			wantLCType:  "ephemeral",
			wantLCValue: "30m",
		},
		// §8.3 Valid — Time-bounded campfire
		{
			name: "8.3 time-bounded",
			tags: []string{
				"category:social",
				"durability:max-ttl:90d",
				"durability:lifecycle:bounded:2026-06-01T00:00:00Z",
			},
			wantValid:   true,
			wantMaxTTL:  "90d",
			wantLCType:  "bounded",
			wantLCValue: "2026-06-01T00:00:00Z",
		},
		// §8.4 Valid — No durability tags
		{
			name: "8.4 no durability tags",
			tags: []string{
				"category:social",
				"member_count:5",
			},
			wantValid:   true,
			wantMaxTTL:  "",
			wantLCType:  "",
			wantLCValue: "",
		},
		// §8.5 Valid — max-ttl only
		{
			name: "8.5 max-ttl only",
			tags: []string{
				"category:social",
				"durability:max-ttl:30d",
			},
			wantValid:   true,
			wantMaxTTL:  "30d",
			wantLCType:  "",
			wantLCValue: "",
		},
		// §8.6 Invalid — Multiple max-ttl tags
		{
			name: "8.6 multiple max-ttl",
			tags: []string{
				"durability:max-ttl:30d",
				"durability:max-ttl:90d",
			},
			wantValid:      false,
			wantErrContain: "multiple durability:max-ttl tags",
		},
		// §8.7 Invalid — max-ttl with unknown unit
		{
			name:           "8.7 unknown unit",
			tags:           []string{"durability:max-ttl:30w"},
			wantValid:      false,
			wantErrContain: "unknown unit 'w'",
		},
		// §8.8 Invalid — max-ttl with zero N (0d)
		{
			name:           "8.8 zero N with unit (0d)",
			tags:           []string{"durability:max-ttl:0d"},
			wantValid:      false,
			wantErrContain: "'0d' is invalid",
		},
		// §8.9 Invalid — lifecycle with unknown type
		{
			name:           "8.9 unknown lifecycle type",
			tags:           []string{"durability:lifecycle:temporary"},
			wantValid:      false,
			wantErrContain: "unknown type 'temporary'",
		},
		// §8.10 Invalid — bounded with malformed date
		{
			name:           "8.10 malformed bounded date",
			tags:           []string{"durability:lifecycle:bounded:June-2026"},
			wantValid:      false,
			wantErrContain: "not valid ISO 8601 UTC",
		},
		// §8.11 Warning — bounded date in the past (referenceTime = 2026-03-28)
		{
			name:        "8.11 past bounded date",
			tags:        []string{"durability:lifecycle:bounded:2025-01-01T00:00:00Z"},
			wantValid:   true,
			wantLCType:  "bounded",
			wantLCValue: "2025-01-01T00:00:00Z",
			wantWarnings: []string{
				"durability:lifecycle:bounded date is in the past — campfire lifecycle has elapsed",
			},
		},
		// §8.12 Warning — max-ttl exceeds 100 years
		{
			name:       "8.12 max-ttl exceeds 100 years",
			tags:       []string{"durability:max-ttl:50000d"},
			wantValid:  true,
			wantMaxTTL: "0",
			wantWarnings: []string{
				"durability:max-ttl: 50000d exceeds 100 years — treated as keep-forever (0)",
			},
		},
		// §8.13 Invalid — ephemeral with no timeout
		{
			name:           "8.13 ephemeral no timeout",
			tags:           []string{"durability:lifecycle:ephemeral:"},
			wantValid:      false,
			wantErrContain: "ephemeral timeout is empty",
		},
		// §8.14 Invalid — multiple lifecycle tags
		{
			name: "8.14 multiple lifecycle tags",
			tags: []string{
				"durability:lifecycle:persistent",
				"durability:lifecycle:ephemeral:10m",
			},
			wantValid:      false,
			wantErrContain: "multiple durability:lifecycle tags",
		},
		// §8.15 Invalid — negative duration
		{
			name:           "8.15 negative duration",
			tags:           []string{"durability:max-ttl:-5d"},
			wantValid:      false,
			wantErrContain: "negative or non-numeric value",
		},
		// §8.16 Invalid — leading zero in N
		{
			name:           "8.16 leading zero",
			tags:           []string{"durability:max-ttl:030d"},
			wantValid:      false,
			wantErrContain: "leading zero",
		},
		// §8.17 Invalid — ephemeral:0
		{
			name:           "8.17 ephemeral:0",
			tags:           []string{"durability:lifecycle:ephemeral:0"},
			wantValid:      false,
			wantErrContain: "ephemeral timeout must be a positive integer with unit",
		},
		// §8.18 Warning — unknown durability namespace tag
		{
			name: "8.18 unknown durability namespace",
			tags: []string{
				"durability:max-ttl:30d",
				"durability:custom:foo",
			},
			wantValid:  true,
			wantMaxTTL: "30d",
			wantWarnings: []string{
				"unknown tag in reserved durability: namespace: durability:custom:foo",
			},
		},
		// §8.19 Invalid — N exceeds 6 digits
		{
			name:           "8.19 N exceeds 6 digits",
			tags:           []string{"durability:max-ttl:1234567d"},
			wantValid:      false,
			wantErrContain: "duration value exceeds 6-digit maximum",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := ParseTagsAt(tt.tags, referenceTime)
			if err != nil {
				t.Fatalf("ParseTagsAt returned unexpected error: %v", err)
			}

			if r.Valid != tt.wantValid {
				t.Errorf("Valid = %v, want %v (Error: %q)", r.Valid, tt.wantValid, r.Error)
			}

			if tt.wantErrContain != "" {
				if !contains(r.Error, tt.wantErrContain) {
					t.Errorf("Error = %q, want it to contain %q", r.Error, tt.wantErrContain)
				}
			} else if r.Error != "" && tt.wantValid {
				t.Errorf("unexpected Error: %q", r.Error)
			}

			if r.MaxTTL != tt.wantMaxTTL {
				t.Errorf("MaxTTL = %q, want %q", r.MaxTTL, tt.wantMaxTTL)
			}
			if r.LifecycleType != tt.wantLCType {
				t.Errorf("LifecycleType = %q, want %q", r.LifecycleType, tt.wantLCType)
			}
			if r.LifecycleValue != tt.wantLCValue {
				t.Errorf("LifecycleValue = %q, want %q", r.LifecycleValue, tt.wantLCValue)
			}

			for _, warnSubstr := range tt.wantWarnings {
				found := false
				for _, w := range r.Warnings {
					if contains(w, warnSubstr) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected warning containing %q, got %v", warnSubstr, r.Warnings)
				}
			}
		})
	}
}

// Additional edge case tests.

func TestParseTagsExactlyAtBoundary(t *testing.T) {
	// 36500d is exactly 100 years — should NOT warn.
	r, err := ParseTagsAt([]string{"durability:max-ttl:36500d"}, referenceTime)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Valid {
		t.Errorf("expected valid, got error: %s", r.Error)
	}
	if r.MaxTTL != "36500d" {
		t.Errorf("MaxTTL = %q, want 36500d", r.MaxTTL)
	}
	if len(r.Warnings) != 0 {
		t.Errorf("expected no warnings, got: %v", r.Warnings)
	}

	// 36501d exceeds 100 years — should warn and normalize to "0".
	r2, err := ParseTagsAt([]string{"durability:max-ttl:36501d"}, referenceTime)
	if err != nil {
		t.Fatal(err)
	}
	if !r2.Valid {
		t.Errorf("expected valid for 36501d, got error: %s", r2.Error)
	}
	if r2.MaxTTL != "0" {
		t.Errorf("MaxTTL = %q, want 0 for 36501d", r2.MaxTTL)
	}
	if len(r2.Warnings) == 0 {
		t.Errorf("expected warning for 36501d")
	}
}

func TestParseTagsMaxSixDigits(t *testing.T) {
	// Exactly 6 digits (999999d) should be valid.
	r, err := ParseTagsAt([]string{"durability:max-ttl:999999d"}, referenceTime)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Valid {
		t.Errorf("expected valid for 999999d")
	}

	// 7 digits (1000000d) should be invalid.
	r2, err := ParseTagsAt([]string{"durability:max-ttl:1000000d"}, referenceTime)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Valid {
		t.Errorf("expected invalid for 1000000d")
	}
}

func TestParseTagsBoundedFuture(t *testing.T) {
	// Bounded date in the future — valid, no warning.
	r, err := ParseTagsAt([]string{"durability:lifecycle:bounded:2030-01-01T00:00:00Z"}, referenceTime)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Valid {
		t.Errorf("expected valid, got error: %s", r.Error)
	}
	if len(r.Warnings) != 0 {
		t.Errorf("expected no warnings for future date, got: %v", r.Warnings)
	}
}

func TestParseTagsAllUnits(t *testing.T) {
	units := []string{"s", "m", "h", "d"}
	for _, u := range units {
		tag := "durability:max-ttl:100" + u
		r, err := ParseTagsAt([]string{tag}, referenceTime)
		if err != nil {
			t.Fatalf("unit %s: unexpected error: %v", u, err)
		}
		if !r.Valid {
			t.Errorf("unit %s: expected valid, got error: %s", u, r.Error)
		}
		if r.MaxTTL != "100"+u {
			t.Errorf("unit %s: MaxTTL = %q, want %q", u, r.MaxTTL, "100"+u)
		}
	}
}

func TestParseTagsNonDurabilityTagsIgnored(t *testing.T) {
	r, err := ParseTagsAt([]string{
		"category:infrastructure",
		"topic:coordination",
		"member_count:12",
		"published_at:2026-03-28T00:00:00Z",
	}, referenceTime)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Valid {
		t.Errorf("expected valid for non-durability tags")
	}
	if r.MaxTTL != "" || r.LifecycleType != "" || r.LifecycleValue != "" {
		t.Errorf("expected empty durability fields for non-durability tags")
	}
}

func contains(s, substr string) bool {
	return len(substr) == 0 || (len(s) >= len(substr) && indexOfSubstring(s, substr) >= 0)
}

func indexOfSubstring(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

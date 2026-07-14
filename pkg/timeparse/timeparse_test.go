package timeparse_test

import (
	"strings"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/timeparse"
)

// fixedNow is a fixed reference time for deterministic tests.
// 2026-03-25T12:00:00Z — a Wednesday.
var fixedNow = time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)

func TestParse(t *testing.T) {
	tests := []struct {
		name        string
		expr        string
		wantErr     bool
		wantContain string // substring the result must contain (for RFC3339 passthrough)
		wantExact   string // exact result
	}{
		// Relative hours
		{
			name:      "2h",
			expr:      "2h",
			wantExact: "2026-03-25T14:00:00Z",
		},
		{
			name:      "0h",
			expr:      "0h",
			wantExact: "2026-03-25T12:00:00Z",
		},
		{
			name:      "1H uppercase",
			expr:      "1H",
			wantExact: "2026-03-25T13:00:00Z",
		},
		{
			name:      "24h",
			expr:      "24h",
			wantExact: "2026-03-26T12:00:00Z",
		},
		// Relative days
		{
			name:      "3d",
			expr:      "3d",
			wantExact: "2026-03-28T12:00:00Z",
		},
		{
			name:      "0d",
			expr:      "0d",
			wantExact: "2026-03-25T12:00:00Z",
		},
		{
			name:      "999d",
			expr:      "999d",
			wantExact: "2028-12-18T12:00:00Z",
		},
		{
			name:      "1D uppercase",
			expr:      "1D",
			wantExact: "2026-03-26T12:00:00Z",
		},
		// Named
		{
			name:      "tomorrow",
			expr:      "tomorrow",
			wantExact: "2026-03-26T09:00:00Z",
		},
		{
			name:      "Tomorrow uppercase",
			expr:      "Tomorrow",
			wantExact: "2026-03-26T09:00:00Z",
		},
		{
			name:      "next week",
			expr:      "next week",
			// 2026-03-25 is Wednesday; next Monday = 2026-03-30
			wantExact: "2026-03-30T09:00:00Z",
		},
		{
			name:      "Next Week",
			expr:      "Next Week",
			wantExact: "2026-03-30T09:00:00Z",
		},
		// RFC3339 passthrough
		{
			name:        "RFC3339 passthrough",
			expr:        "2026-04-01T12:00:00Z",
			wantContain: "2026-04-01T12:00:00Z",
		},
		{
			name:        "RFC3339 with offset",
			expr:        "2026-04-01T12:00:00+05:00",
			wantContain: "2026-04-01T07:00:00Z",
		},
		// YYYY-MM-DD
		{
			name:      "YYYY-MM-DD",
			expr:      "2026-04-15",
			wantExact: "2026-04-15T09:00:00Z",
		},
		// Errors
		{
			name:    "invalid format",
			expr:    "foo",
			wantErr: true,
		},
		{
			name:    "empty",
			expr:    "",
			wantErr: true,
		},
		{
			name:    "negative hours",
			expr:    "-2h",
			wantErr: true,
		},
		{
			name:    "negative days",
			expr:    "-3d",
			wantErr: true,
		},
		{
			name:    "letters in hours",
			expr:    "abch",
			wantErr: true,
		},
		{
			name:    "letters in days",
			expr:    "abcd",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := timeparse.Parse(tc.expr, fixedNow)
			if tc.wantErr {
				if err == nil {
					t.Errorf("Parse(%q) = %q, want error", tc.expr, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q) unexpected error: %v", tc.expr, err)
			}
			if tc.wantExact != "" && got != tc.wantExact {
				t.Errorf("Parse(%q) = %q, want %q", tc.expr, got, tc.wantExact)
			}
			if tc.wantContain != "" && !strings.Contains(got, tc.wantContain) {
				t.Errorf("Parse(%q) = %q, want it to contain %q", tc.expr, got, tc.wantContain)
			}
		})
	}
}

// TestParseNextWeekOnMonday verifies "next week" when called on a Monday
// returns the following Monday (7 days later).
func TestParseNextWeekOnMonday(t *testing.T) {
	// 2026-03-23 is a Monday.
	monday := time.Date(2026, 3, 23, 10, 0, 0, 0, time.UTC)
	got, err := timeparse.Parse("next week", monday)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "2026-03-30T09:00:00Z"
	if got != want {
		t.Errorf("next week from Monday = %q, want %q", got, want)
	}
}

// TestParseNextWeekOnSunday verifies "next week" from Sunday returns the next Monday.
func TestParseNextWeekOnSunday(t *testing.T) {
	// 2026-03-29 is a Sunday.
	sunday := time.Date(2026, 3, 29, 10, 0, 0, 0, time.UTC)
	got, err := timeparse.Parse("next week", sunday)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "2026-03-30T09:00:00Z"
	if got != want {
		t.Errorf("next week from Sunday = %q, want %q", got, want)
	}
}

package main

import (
	"testing"
)

// TestTruncateID_SafelyHandlesShortStrings verifies that truncateID does not panic
// on strings shorter than maxLen. This is a regression test for ready-6ef, where
// code was using direct slicing like s[:12] without bounds checking.
func TestTruncateID_SafelyHandlesShortStrings(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
		desc   string
	}{
		// Typical case: 64-char hex ID truncated to 12 chars (9 chars + "...")
		{"abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz01", 12, "abcdefghi...", "64-char truncated to 12"},
		// Exactly at max length — no truncation
		{"abcdefghijkl", 12, "abcdefghijkl", "exactly at max length"},
		// One char under max — no truncation
		{"abcdefghijk", 12, "abcdefghijk", "one char under max"},
		// Short ID (regression test for ready-6ef) — no panic
		{"abc", 12, "abc", "3-char string with 12-char max"},
		{"", 12, "", "empty string with 12-char max"},
		// Very short max length
		{"abcdefghij", 3, "abc", "truncate to 3 chars"},
		{"abcdefghij", 4, "a...", "truncate to 4 chars"},
		// Max length = 1 edge case
		{"abcdefghij", 1, "a", "max length 1"},
		// Max length = 2 edge case
		{"abcdefghij", 2, "ab", "max length 2"},
		// Edge case: string = 4 chars, maxLen = 3
		{"abcd", 3, "abc", "4-char string to 3-char max"},
		// Edge case: string = 5 chars, maxLen = 4 (should show "...")
		{"abcde", 4, "a...", "5-char string to 4-char max"},
	}
	for _, tt := range tests {
		got := truncateID(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncateID(%q, %d) [%s] = %q, want %q", tt.input, tt.maxLen, tt.desc, got, tt.want)
		}
	}
}

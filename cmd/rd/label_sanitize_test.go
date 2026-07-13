package main

import (
	"strings"
	"testing"
)

// TestSanitizeLabelText_EscapesTerminalInjection is the coverage-sweep security
// unit for label.go:sanitizeLabelText — the trusted-vocabulary READ contract. Label
// descriptions are free-form and the write-side control-char gate can be bypassed by
// a raw campfire flush (ready-1c2), so the read path must neutralize ANSI/control
// payloads before they reach a terminal (or an agent reading `rd label list`). Every
// non-printable rune must be replaced with an escaped \xNN/\uNNNN form; no raw ESC,
// bell, or C0 control byte may survive.
func TestSanitizeLabelText_EscapesTerminalInjection(t *testing.T) {
	cases := []struct {
		name string
		in   string
		// mustNotContain lists raw control bytes that must be gone from the output.
		mustNotContain []string
		// mustContain lists the escaped forms that must appear.
		mustContain []string
	}{
		{
			name:           "ANSI color escape",
			in:             "\x1b[31mHACKED\x1b[0m",
			mustNotContain: []string{"\x1b"},
			mustContain:    []string{"\\x1b", "HACKED"},
		},
		{
			name:           "bell + carriage return + newline",
			in:             "line1\r\n\x07beep",
			mustNotContain: []string{"\x07", "\r", "\n"},
			mustContain:    []string{"\\x07", "\\x0d", "\\x0a", "line1", "beep"},
		},
		{
			name:           "cursor-move ANSI injecting a fake prompt",
			in:             "ok\x1b[2K\x1b[1Grm -rf /",
			mustNotContain: []string{"\x1b"},
			mustContain:    []string{"\\x1b", "rm -rf /"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeLabelText(tc.in)
			for _, bad := range tc.mustNotContain {
				if strings.Contains(got, bad) {
					t.Errorf("sanitizeLabelText(%q) = %q still contains raw control byte %q", tc.in, got, bad)
				}
			}
			for _, want := range tc.mustContain {
				if !strings.Contains(got, want) {
					t.Errorf("sanitizeLabelText(%q) = %q missing expected %q", tc.in, got, want)
				}
			}
		})
	}
}

// TestSanitizeLabelText_PrintablePassthrough proves a fully-printable description
// (including non-ASCII printable Unicode) is returned byte-for-byte unchanged — the
// sanitizer neutralizes only non-printable runes and never mangles legitimate text.
func TestSanitizeLabelText_PrintablePassthrough(t *testing.T) {
	for _, s := range []string{
		"a plain description",
		"unicode ok: café — naïve — 日本語 — ✓",
		"symbols: <>&{}[]()!@#$%^*",
		"",
	} {
		if got := sanitizeLabelText(s); got != s {
			t.Errorf("sanitizeLabelText(%q) = %q, want unchanged", s, got)
		}
	}
}

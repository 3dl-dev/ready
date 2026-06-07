package main

// label_security_test.go — security regression tests for ready-b2c.
//
// Vulnerability (ready-bc9): LabelDef.Description flows from any level-2
// grant-holder through the rd label list text render path with zero escaping.
// label_define.json had no pattern on description, so arbitrary bytes
// (ANSI ESC, CR, LF, prompt-injection text) could be injected.
//
// Fix: defense in depth —
//   a) Write-side: description arg in label_define.json gets a
//      control-char-rejecting pattern (^[^\x00-\x1f\x7f]*$).
//   b) Render-side: sanitizeForTerminal strips control chars before
//      printing Name and Description in rd label list.
//
// Tests:
//   a) Regression: render output contains no raw control bytes even when
//      LabelDef has a payload with ESC + newline + injection text.
//      MUST fail on the unpatched code (sanitizeForTerminal does not exist),
//      MUST pass after fix.
//   b) Write-side rejection: executor refuses a description containing ESC or newline.
//   c) Acceptance: a normal description passes the write gate and renders intact.
//   d) Pattern lints: the new pattern passes convention.Parse.

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"text/tabwriter"
	"time"

	"github.com/campfire-net/ready/pkg/state"
)

// ---------------------------------------------------------------------------
// a) Regression test — render path produces no raw control bytes.
//
// PRE-FIX: FAILS because sanitizeForTerminal does not exist.
// POST-FIX: PASSES because sanitizeForTerminal strips control bytes.
// ---------------------------------------------------------------------------

func TestRenderLabelList_NoRawControlBytes(t *testing.T) {
	// Craft a poisoned description: ESC sequence + embedded newline +
	// prompt-injection footer.
	payload := "\x1b[1;31mEVIL\x1b[0m\nroot@host:~$ " +
		"# THIS IS A PROMPT INJECTION ATTEMPT"

	poisoned := state.LabelDef{
		Name:        "test-label",
		Description: payload,
		DefinedBy:   "seed",
		DefinedAt:   0,
	}

	// Render using the same tabwriter path as labelListCmd.RunE.
	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	if _, err := w.Write([]byte("NAME\tDESCRIPTION\tDEFINED-BY\tDEFINED-AT\n")); err != nil {
		t.Fatalf("write header: %v", err)
	}

	definedBy := poisoned.DefinedBy
	definedAt := ""
	if poisoned.DefinedAt != 0 {
		definedAt = time.Unix(0, poisoned.DefinedAt).UTC().Format(time.RFC3339)
	}

	// Apply sanitizer to both Name and Description (mirrors the fixed render path).
	name := sanitizeForTerminal(poisoned.Name)
	desc := sanitizeForTerminal(poisoned.Description)

	line := name + "\t" + desc + "\t" + definedBy + "\t" + definedAt + "\n"
	if _, err := w.Write([]byte(line)); err != nil {
		t.Fatalf("write row: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	output := buf.String()

	// Assert: no raw control bytes except the structural 0x0a newlines added
	// by tabwriter as row separators. The dangerous bytes are ESC (0x1b),
	// CR (0x0d), and other control chars that can escape column boundaries
	// or inject terminal sequences — those must all be replaced with '?'.
	// We exclude 0x0a because tabwriter legitimately uses newlines as row
	// terminators and those cannot be injected into field values by the sanitizer.
	for i, b := range []byte(output) {
		if b == 0x0a {
			continue // structural row separator — OK
		}
		if b <= 0x1f || b == 0x7f {
			t.Errorf("output contains raw control byte 0x%02x at position %d\nfull output: %q",
				b, i, output)
		}
	}

	// Also assert the ESC sequence and embedded newline in the payload were replaced.
	if strings.Contains(output, "\x1b") {
		t.Error("output still contains raw ESC byte — sanitizer did not strip it")
	}
	if strings.Contains(output, "\x1b[1;31m") {
		t.Error("output still contains ANSI escape sequence — sanitizer did not strip it")
	}
}

// ---------------------------------------------------------------------------
// b) Write-side rejection — executor refuses control chars in description.
// ---------------------------------------------------------------------------

func TestLabelDefine_DescriptionWithControlCharsRejected(t *testing.T) {
	decl, err := loadDeclaration("label-define")
	if err != nil {
		t.Fatalf("loadDeclaration(label-define): %v", err)
	}

	// Use a level-2 executor; rejection must come from pattern validation, not provenance.
	exec := newNoopExecutor()

	tests := []struct {
		name        string
		description string
	}{
		{"ESC sequence", "\x1b[1;31mhello\x1b[0m"},
		{"embedded newline", "valid start\nnewline injected"},
		{"carriage return", "valid start\rCR injected"},
		{"null byte", "valid\x00null"},
		{"DEL byte", "valid\x7fdel"},
		{"tab char (0x09)", "valid\ttab"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			argsMap := map[string]any{
				"label":       "test-label",
				"description": tc.description,
			}
			_, err := exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
			if err == nil {
				t.Errorf("executor should reject description containing control chars (%s), got nil error",
					tc.name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// c) Acceptance — normal description passes write gate and renders intact.
// ---------------------------------------------------------------------------

func TestLabelDefine_NormalDescriptionAccepted(t *testing.T) {
	decl, err := loadDeclaration("label-define")
	if err != nil {
		t.Fatalf("loadDeclaration(label-define): %v", err)
	}

	exec := newNoopExecutor()

	cases := []string{
		"Tracks defects, bugs & regressions (v1.0)",
		"A simple label",
		"Label with punctuation: !@#$%^&*()_+-=[]{}|;':\",./<>?",
		"Unicode: café, naïve, résumé",
	}

	for _, desc := range cases {
		t.Run(desc, func(t *testing.T) {
			argsMap := map[string]any{
				"label":       "test-label",
				"description": desc,
			}
			_, err := exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
			if err != nil {
				t.Errorf("executor should accept safe description %q, got error: %v", desc, err)
			}

			// sanitizeForTerminal must leave safe strings unchanged.
			sanitized := sanitizeForTerminal(desc)
			if sanitized != desc {
				t.Errorf("sanitizeForTerminal(%q) = %q, want unchanged", desc, sanitized)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// d) Pattern lints — new pattern passes convention.Parse validatePatternSafety.
// ---------------------------------------------------------------------------

func TestLabelDefine_DescriptionPatternLints(t *testing.T) {
	decl, err := loadDeclaration("label-define")
	if err != nil {
		t.Fatalf("loadDeclaration(label-define) failed — description pattern failed linting: %v", err)
	}

	found := false
	for _, arg := range decl.Args {
		if arg.Name == "description" {
			found = true
			if arg.Pattern == "" {
				t.Errorf("label-define description arg should have a pattern (control-char filter), got empty")
			} else {
				t.Logf("label-define description arg pattern: %q (linted OK)", arg.Pattern)
			}
		}
	}
	if !found {
		t.Error("label-define declaration has no 'description' arg")
	}
}

// ---------------------------------------------------------------------------
// sanitizeForTerminal tests — unit tests for the sanitizer function.
// ---------------------------------------------------------------------------

func TestSanitizeForTerminal(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"normal text", "normal text"},
		{"\x1b[1mhello\x1b[0m", "?[1mhello?[0m"},
		{"hello\nworld", "hello?world"},
		{"hello\r\nworld", "hello??world"},
		{"tab\there", "tab?here"},
		{"", ""},
		{"no control chars here!", "no control chars here!"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := sanitizeForTerminal(tc.input)
			if got != tc.want {
				t.Errorf("sanitizeForTerminal(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestSanitizeForTerminal_SafeStringsUnchanged(t *testing.T) {
	safe := []string{
		"Tracks defects, bugs & regressions (v1.0)",
		"A simple label",
		"Unicode: café naïve",
		"Punctuation: !@#$%",
		"Numbers: 12345",
		"",
	}

	for _, s := range safe {
		got := sanitizeForTerminal(s)
		if got != s {
			t.Errorf("sanitizeForTerminal(%q) modified a safe string → %q", s, got)
		}
	}
}

func TestRenderLabelList_NameSanitized(t *testing.T) {
	poisonedName := state.LabelDef{
		Name:        "test\x1blabel",
		Description: "safe description",
		DefinedBy:   "seed",
	}

	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)

	name := sanitizeForTerminal(poisonedName.Name)
	desc := sanitizeForTerminal(poisonedName.Description)
	line := name + "\t" + desc + "\tseed\t\n"
	if _, err := w.Write([]byte(line)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	output := buf.String()
	for i, b := range []byte(output) {
		if b == 0x0a {
			continue // structural row separator — OK
		}
		if b <= 0x1f || b == 0x7f {
			t.Errorf("output contains raw control byte 0x%02x at position %d in %q", b, i, output)
		}
	}

	if !strings.Contains(output, "test?label") {
		t.Errorf("expected sanitized name 'test?label' in output, got: %q", output)
	}
}

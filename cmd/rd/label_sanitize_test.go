package main

// label_sanitize_test.go — regression + gate tests for ready-b2c.
//
// VULN (from opus security review ready-bc9): the label DESCRIPTION field is
// free-form producer-controlled text. It was rendered to the terminal by
// `rd label list` with zero escaping, and the write-side declaration had a
// max_length but no pattern — so arbitrary bytes (ANSI/ESC, CR/LF, BEL, NUL)
// could reach a human or agent terminal, enabling terminal hijack and agent
// prompt-injection. The label trust contract promises rendered atoms are safe;
// description broke that promise.
//
// Defense in depth (the ready-a92 ruling): BOTH layers must hold.
//   1. Write-side: label_define.json description arg rejects control characters.
//   2. Render-side: cmd/rd/label.go escapes control characters before printing —
//      because the write gate can be bypassed by a raw campfire flush
//      (ready-1c2 / buildLabelRegistry admits any work:label-define unchecked).

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/cf-conventions/cf-convention"

	"github.com/campfire-net/ready/pkg/storetest"
)

// TestLabelList_DescriptionControlBytesEscaped is the backported PoC, promoted to
// a regression test. It injects a control-byte description via the RAW flush path
// (storetest.LabelDefine writes work:label-define straight to the store, bypassing
// the executor write gate exactly as a malicious raw sender would), derives the
// real registry, then drives the real render path and asserts no control byte
// survives to the terminal.
//
// RED against pre-fix code (renderLabelTable printed e.def.Description raw);
// GREEN after sanitizeLabelText escapes non-printable runes.
func TestLabelList_DescriptionControlBytesEscaped(t *testing.T) {
	h := storetest.New(t)

	// ESC-based ANSI color, BEL, CR/LF, NUL, DEL — the terminal-hijack vectors.
	payload := "danger\x1b[31mRED\x1b[0m\r\nLINE2\x07\x00\x7f"
	h.LabelDefine("evil", payload)

	registry := h.DeriveAll().LabelRegistry()

	// Vuln precondition: derive admits the raw define with bytes intact. If this
	// ever stops holding, the render-side defense is no longer load-bearing and
	// this test's premise must be revisited.
	if got := registry["evil"].Description; got != payload {
		t.Fatalf("precondition: derived description = %q, want raw payload %q", got, payload)
	}

	var buf bytes.Buffer
	if err := renderLabelTable(&buf, sortedLabelEntries(registry)); err != nil {
		t.Fatalf("renderLabelTable: %v", err)
	}
	out := buf.String()

	// No raw control byte may reach the terminal. (Row-terminating '\n' added by
	// the table writer itself is fine — we check the injection vectors only.)
	for _, b := range []byte{0x1b, 0x07, 0x00, 0x7f, '\r'} {
		if strings.IndexByte(out, b) >= 0 {
			t.Errorf("rendered output contains raw control byte 0x%02x — terminal injection NOT neutralized", b)
		}
	}

	// The label name and the printable text survive; the ESC is shown escaped.
	if !strings.Contains(out, "evil") {
		t.Errorf("rendered output dropped label name 'evil':\n%q", out)
	}
	if !strings.Contains(out, "\\x1b") {
		t.Errorf("expected ESC rendered as escaped \\x1b, output:\n%q", out)
	}
}

// TestLabelDefine_DescriptionControlBytesRejected is the write-side gate proof:
// the executor must refuse a label-define whose description carries control
// characters (the description arg now has a control-char-rejecting pattern).
func TestLabelDefine_DescriptionControlBytesRejected(t *testing.T) {
	decl, err := loadDeclaration("label-define")
	if err != nil {
		t.Fatalf("loadDeclaration(label-define): %v", err)
	}

	// Level-2 caller so any rejection comes from the description pattern, not provenance.
	const callerKey = "test-key-desc-reject"
	exec := convention.NewExecutorForTest(&noopBackend{}, callerKey).
		WithProvenance(&staticProvenanceChecker{levels: map[string]int{callerKey: 2}})

	tests := []struct {
		name string
		desc string
	}{
		{"esc-ansi", "hello\x1b[31mworld"},
		{"newline", "line1\nline2"},
		{"carriage-return", "a\rb"},
		{"tab", "col1\tcol2"},
		{"bell", "ding\x07"},
		{"nul", "x\x00y"},
		{"del", "x\x7fy"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			argsMap := map[string]any{"label": "okname", "description": tc.desc}
			_, err := exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
			if err == nil {
				t.Errorf("executor should reject description with %s control char(s) %q, but accepted it", tc.name, tc.desc)
			}
		})
	}
}

// TestLabelDefine_DescriptionPrintableAccepted is the acceptance counterpart:
// ordinary descriptions — spaces, punctuation, Unicode, at max_length — pass the
// write gate. A rejection here would mean the pattern is too strict.
func TestLabelDefine_DescriptionPrintableAccepted(t *testing.T) {
	decl, err := loadDeclaration("label-define")
	if err != nil {
		t.Fatalf("loadDeclaration(label-define): %v", err)
	}

	const callerKey = "test-key-desc-accept"
	exec := convention.NewExecutorForTest(&noopBackend{}, callerKey).
		WithProvenance(&staticProvenanceChecker{levels: map[string]int{callerKey: 2}})

	valid := []string{
		"A normal description.",
		"Bugs, features & questions — all OK!",
		"unicode: café résumé naïve 日本語 😀",
		"punctuation: (parens) [brackets] {braces} /slash/ \"quotes\" 'apostrophe' #100%",
		strings.Repeat("x", 256), // exactly at max_length
	}

	for i, desc := range valid {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			argsMap := map[string]any{"label": "okname", "description": desc}
			_, err := exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
			if err != nil {
				t.Errorf("executor should accept printable description %q, got error: %v", desc, err)
			}
		})
	}
}

// TestSanitizeLabelText_UnitBehavior pins the escaping contract directly.
func TestSanitizeLabelText_UnitBehavior(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello world", "hello world"},
		{"unicode-printable-untouched", "café 日本語 😀", "café 日本語 😀"},
		{"esc", "a\x1bb", "a\\x1bb"},
		{"newline", "a\nb", "a\\x0ab"},
		{"tab", "a\tb", "a\\x09b"},
		{"del", "a\x7fb", "a\\x7fb"},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeLabelText(tc.in); got != tc.want {
				t.Errorf("sanitizeLabelText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

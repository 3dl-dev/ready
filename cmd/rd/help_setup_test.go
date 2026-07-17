package main

// help_setup_test.go — ready-e8a: `rd --help` must show all THREE real
// onboarding paths (init / follow / invite) in plain words, `rd join --help`
// must not claim an invite token is the only join path, and every dead-end
// error on the join/follow/link/confidential-write/legacy-config paths must
// name its own remedy command. commandHelpText is shared from
// help_campfire_test.go (same package).

import (
	"strings"
	"testing"
)

// TestRootHelp_ShowsThreeSetupPaths is the done-condition test for (1): the
// SETUP section of `rd --help` must show all three onboarding paths in plain
// words — a person on a new machine must be able to find the correct path
// from the tool alone, including the previously-missing "join boards from
// another of your machines" path (`rd follow`).
func TestRootHelp_ShowsThreeSetupPaths(t *testing.T) {
	help := commandHelpText(rootCmd)

	if !strings.Contains(help, "rd init") {
		t.Errorf("`rd --help` does not mention 'rd init' (start a new project)")
	}
	if !strings.Contains(help, "rd invite") {
		t.Errorf("`rd --help` does not mention 'rd invite' (invite a teammate)")
	}
	// The done condition names this line explicitly: a follow-from-another-
	// machine path must be visible, not just 'rd follow' in isolation — the
	// SETUP section must explain WHEN to use it (another of your machines).
	if !strings.Contains(help, "rd follow") {
		t.Errorf("`rd --help` does not mention 'rd follow' at all — the join-from-another-machine path is missing")
	}
	lower := strings.ToLower(help)
	if !strings.Contains(lower, "another") || !strings.Contains(lower, "machine") {
		t.Errorf("`rd --help` mentions 'rd follow' but not the another-machine framing that explains when to use it:\n%s", help)
	}
}

// TestJoinHelp_DoesNotClaimInviteIsOnlyPath is the done-condition test for
// (2): `rd join --help` must no longer assert that an invite token is the
// only join path (false — `rd follow` is a second, real path for a person's
// own additional machines), and it must point casual multi-machine users at
// `rd follow`.
func TestJoinHelp_DoesNotClaimInviteIsOnlyPath(t *testing.T) {
	help := commandHelpText(joinCmd)
	lower := strings.ToLower(help)

	if strings.Contains(lower, "only join path") {
		t.Errorf("`rd join --help` still claims an invite token is the only join path:\n%s", help)
	}
	if !strings.Contains(help, "rd follow") {
		t.Errorf("`rd join --help` does not point casual multi-machine users at 'rd follow':\n%s", help)
	}
}

// TestFollowHelp_ShortHonestCopyPasteableExample is the done-condition test
// for (3): `rd follow --help` must carry a real, copy-pasteable example —
// 'rd follow baron@3dl.dev'.
func TestFollowHelp_ShortHonestCopyPasteableExample(t *testing.T) {
	help := commandHelpText(followCmd)
	if !strings.Contains(help, "rd follow baron@3dl.dev") {
		t.Errorf("`rd follow --help` does not carry the real copy-pasteable example 'rd follow baron@3dl.dev':\n%s", help)
	}
}

// TestDeadEndErrors_NameTheirRemedy is the snapshot test for (4): every
// dead-end error on the join/follow/link/confidential-write/legacy-config
// paths must name its OWN exact remedy command — not just any `rd <cmd>`
// token. A loose `\brd [a-z][a-z-]*` regex matches stale 'rd pin-board' as
// readily as the current 'rd link', so it does not guard the pin-board ->
// link rename; each case below pins the specific post-rename command that
// path's error must contain. Each case drives the real error-producing code
// path (not a copy of the string), so a future edit that drops or reverts
// the remedy fails this test.
func TestDeadEndErrors_NameTheirRemedy(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantRemedy string // exact command token the error message must contain
	}{
		{
			name:       "join: non-token argument",
			err:        joinCmd.RunE(joinCmd, []string{"not-a-token"}),
			wantRemedy: "rd follow",
		},
		{
			name: "resolveBoardAuthorD: unresolvable board d",
			err: func() error {
				_, _, err := resolveBoardAuthorD("/x", "deadbeef")
				return err
			}(),
			wantRemedy: "rd link",
		},
		{
			name: "rd grant: not a nostr-native project",
			err: func() error {
				dir := isolateTempDir(t)
				_ = dir
				return grantCmd.RunE(grantCmd, []string{strings.Repeat("ab", 32), "contributor"})
			}(),
			wantRemedy: "rd link",
		},
		{
			name: "rd sessions: no pinned board",
			err: func() error {
				dir := isolateTempDir(t)
				return runSessionsNostr(dir, false)
			}(),
			wantRemedy: "rd link",
		},
		{
			name: "rd invite: not a nostr-native project",
			err: func() error {
				isolateTempDir(t)
				_, err := runNostrInvite(0)
				return err
			}(),
			wantRemedy: "rd link",
		},
		{
			name: "rd link: no .ready project directory",
			err: func() error {
				isolateTempDir(t)
				return runLinkOrPinBoard(nostrLinkCmd, nil)
			}(),
			wantRemedy: "rd init",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil {
				t.Fatalf("%s: expected an error, got nil", tc.name)
			}
			msg := tc.err.Error()
			if !strings.Contains(msg, tc.wantRemedy) {
				t.Errorf("%s: error does not name the expected remedy %q: %q", tc.name, tc.wantRemedy, msg)
			}
			if strings.Contains(msg, "pin-board") {
				t.Errorf("%s: error still names the stale 'pin-board' command: %q", tc.name, msg)
			}
		})
	}
}

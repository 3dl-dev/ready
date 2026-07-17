package main

// help_setup_test.go — ready-e8a: `rd --help` must show all THREE real
// onboarding paths (init / follow / invite) in plain words, `rd join --help`
// must not claim an invite token is the only join path, and every dead-end
// error on the join/follow/link/confidential-write/legacy-config paths must
// name its own remedy command. commandHelpText is shared from
// help_campfire_test.go (same package).

import (
	"regexp"
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

// rdCmdRemedy matches an 'rd <word>' command token anywhere in an error
// string — the shape every dead-end error's remedy must carry.
var rdCmdRemedy = regexp.MustCompile(`\brd [a-z][a-z-]*`)

// TestDeadEndErrors_NameTheirRemedy is the snapshot test for (4): every
// dead-end error on the join/follow/link/confidential-write/legacy-config
// paths must contain an `rd <cmd>` remedy — the exact next command to run.
// Each case below drives the real error-producing code path (not a copy of
// the string), so a future edit that drops the remedy fails this test.
func TestDeadEndErrors_NameTheirRemedy(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{
			name: "join: non-token argument",
			err:  joinCmd.RunE(joinCmd, []string{"not-a-token"}),
		},
		{
			name: "resolveBoardAuthorD: unresolvable board d",
			err: func() error {
				_, _, err := resolveBoardAuthorD("/x", "deadbeef")
				return err
			}(),
		},
		{
			name: "rd grant: not a nostr-native project",
			err: func() error {
				dir := isolateTempDir(t)
				_ = dir
				return grantCmd.RunE(grantCmd, []string{strings.Repeat("ab", 32), "contributor"})
			}(),
		},
		{
			name: "rd sessions: no pinned board",
			err: func() error {
				dir := isolateTempDir(t)
				return runSessionsNostr(dir, false)
			}(),
		},
		{
			name: "rd invite: not a nostr-native project",
			err: func() error {
				isolateTempDir(t)
				_, err := runNostrInvite(0)
				return err
			}(),
		},
		{
			name: "rd link: no .ready project directory",
			err: func() error {
				isolateTempDir(t)
				return runLinkOrPinBoard(nostrLinkCmd, nil)
			}(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil {
				t.Fatalf("%s: expected an error, got nil", tc.name)
			}
			msg := tc.err.Error()
			if !rdCmdRemedy.MatchString(msg) {
				t.Errorf("%s: error does not name an `rd <cmd>` remedy: %q", tc.name, msg)
			}
		})
	}
}

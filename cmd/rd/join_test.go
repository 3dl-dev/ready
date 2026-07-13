package main

// join_test.go — unit tests for the surviving rd join helpers.
//
// CUTOVER (ready-cb6 I7): campfire open-join (join-by-name/ID, role-grant
// polling, TOFU beacon-root pinning via the campfire client, and the campfire
// transport-dir resolver) was retired with the campfire backend. The only join
// path is the nostr rd1_ invite token (covered by nostr_invite_test.go). What
// remains here is the shared isHex helper and the config-only beacon-root reset.
//
// Done conditions tested:
//   - isHex rejects non-hex strings and accepts valid hex
//   - resetBeaconRoot clears a pinned root and is idempotent when none is pinned

import (
	"testing"

	"github.com/campfire-net/ready/pkg/rdconfig"
)

// TestIsHex verifies isHex correctly identifies hex strings.
func TestIsHex(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"0123456789abcdef", true},
		{"ABCDEF0123456789", true},
		{"0123456789abcdefABCDEF", true},
		{"", true}, // empty string — vacuously true (all chars are hex)
		{"xyz", false},
		{"0123456789abcdefg", false},
		{"ghijklmn", false},
	}
	for _, tc := range cases {
		got := isHex(tc.input)
		if got != tc.want {
			t.Errorf("isHex(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

// cfHomeTempDir creates a temp dir and sets rdHome so CFHome() returns it.
// Cleans up on test completion.
func cfHomeTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	origRDHome := rdHome
	rdHome = dir
	t.Cleanup(func() { rdHome = origRDHome })
	return dir
}

// sampleRoot is a 64-char hex string used as a fake beacon root.
const sampleRoot = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"

// TestBeaconRoot_Reset verifies --reset-beacon-root clears the pin from config
// and returns the previous value.
func TestBeaconRoot_Reset(t *testing.T) {
	cfHome := cfHomeTempDir(t)

	if err := rdconfig.Save(cfHome, &rdconfig.Config{BeaconRoot: sampleRoot}); err != nil {
		t.Fatalf("setup Save: %v", err)
	}

	prev, err := resetBeaconRoot(cfHome)
	if err != nil {
		t.Fatalf("resetBeaconRoot: unexpected error: %v", err)
	}
	if prev != sampleRoot {
		t.Errorf("resetBeaconRoot prev = %q, want %q", prev, sampleRoot)
	}

	saved, err := rdconfig.Load(cfHome)
	if err != nil {
		t.Fatalf("rdconfig.Load after reset: %v", err)
	}
	if saved.BeaconRoot != "" {
		t.Errorf("BeaconRoot after reset = %q, want empty", saved.BeaconRoot)
	}
}

// TestBeaconRoot_Reset_NoPinned verifies resetBeaconRoot returns empty string
// when no root is pinned (idempotent).
func TestBeaconRoot_Reset_NoPinned(t *testing.T) {
	cfHome := cfHomeTempDir(t)

	prev, err := resetBeaconRoot(cfHome)
	if err != nil {
		t.Fatalf("resetBeaconRoot on empty config: unexpected error: %v", err)
	}
	if prev != "" {
		t.Errorf("expected empty prev, got %q", prev)
	}
}

package main

// audit_label_test.go — branch coverage for nostrAuthorityResolver.label, the
// `rd show --audit` authority annotator (ready-cb6 veracity fix). Only the owner
// branch was previously exercised; this guards the graded-role and revoked
// branches so an audit annotation regression is caught by CI.

import (
	"strings"
	"testing"

	rdSync "github.com/campfire-net/ready/pkg/sync"
)

// TestAuthorityResolverLabel_AllBranches drives every authority-label branch of
// nostrAuthorityResolver.label from a directly-constructed graded-levels map
// (the same shape DeriveLevels produces): owner, maintainer, contributor,
// revoked, ungranted, and a non-pubkey actor.
func TestAuthorityResolverLabel_AllBranches(t *testing.T) {
	owner := strings.Repeat("a", 64)
	maint := strings.Repeat("b", 64)
	contrib := strings.Repeat("c", 64)
	revoked := strings.Repeat("d", 64)
	ungranted := strings.Repeat("e", 64)

	r := &nostrAuthorityResolver{
		owner: owner,
		levels: map[string]int{
			maint:   rdSync.LevelMaintainer,
			contrib: rdSync.LevelContributor,
			revoked: rdSync.LevelRevoked,
		},
	}

	cases := []struct {
		name  string
		actor string
		want  string
	}{
		{"owner", owner, "owner (root principal)"},
		{"maintainer", maint, "maintainer"},
		{"contributor", contrib, "contributor"},
		{"revoked", revoked, "revoked"},
		{"ungranted", ungranted, "no delegation grant"},
		{"non-pubkey actor", "baron@3dl.dev", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.label(tc.actor); got != tc.want {
				t.Fatalf("label(%q) = %q, want %q", tc.actor, got, tc.want)
			}
		})
	}
}

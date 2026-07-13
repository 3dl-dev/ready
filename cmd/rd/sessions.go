package main

import (
	"fmt"

	rdSync "github.com/campfire-net/ready/pkg/sync"
	"github.com/spf13/cobra"
)

var sessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "List active delegation grant-holders",
	Long: `List the active delegation grant-holders for this project — the
identities that have been granted work capabilities, minus any revoked
(rd kill) or expired grants. Derived from the signed kind-39301 role-grants
in the local authoritative nostr log.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonOut, _ := cmd.Flags().GetBool("json")

		// NOSTR-NATIVE path (ready-477): list the active grant-holders derived
		// from the signed kind-39301 role-grants (owner + non-revoked grantees).
		// Provisions no .cf and starts no campfire client (ready-cb6 I7).
		if dir, native := nostrNativeProject(); native {
			return runSessionsNostr(dir, jsonOut)
		}
		return errNotNostrProject()
	},
}

// shortKey abbreviates a 64-char hex pubkey for display.
func shortKey(k string) string {
	if len(k) > 12 {
		return k[:12] + "..."
	}
	return k
}

// nostrScopeForKey reports whether keyHex is authorized to claim work items on a
// nostr-native board (used by `rd ready --scope`), and a human note when it is
// not. Authority is derived from the signed kind-39301 role-grants in the local
// authoritative log: the board owner (root principal) is always allowed;
// otherwise the key must hold a live (non-revoked) contributor/maintainer grant.
func nostrScopeForKey(keyHex string) (bool, string) {
	r := loadNostrAuthorityResolver()
	if r == nil {
		return false, "scope check unavailable: not a nostr-native project or the log/pin is unreadable"
	}
	if keyHex == r.owner {
		return true, ""
	}
	lvl, ok := r.levels[keyHex]
	if !ok {
		return false, fmt.Sprintf("no active grant for %s (not a granted identity)", shortKey(keyHex))
	}
	switch lvl {
	case rdSync.LevelRevoked:
		return false, fmt.Sprintf("grant for %s has been revoked", shortKey(keyHex))
	default:
		// LevelMaintainer or contributor — both may claim.
		return true, ""
	}
}

// auditLabeler renders the authority annotation for a history actor in
// `rd show --audit`. On the nostr-native default path the sole implementation is
// nostrAuthorityResolver, which derives authority from the signed kind-39301
// role-grants without ever starting a campfire client or provisioning .cf.
type auditLabeler interface {
	label(actor string) string
}

// nostrAuthorityResolver renders `rd show --audit` authority annotations from the
// nostr projection — the signed kind-39301 role-grants in the local authoritative
// log. The board author is the root principal (level-2 bootstrap trust root);
// every other actor's label is its graded role derived by DeriveLevels.
type nostrAuthorityResolver struct {
	owner  string         // board author pubkey (root principal)
	levels map[string]int // graded operator level per explicitly-granted key
}

// label returns "" for non-pubkey actors so the show loop prints no annotation;
// a short authority description otherwise.
func (r *nostrAuthorityResolver) label(actor string) string {
	if len(actor) != 64 || !isHex(actor) {
		return ""
	}
	if actor == r.owner {
		return "owner (root principal)"
	}
	lvl, ok := r.levels[actor]
	if !ok {
		return "no delegation grant"
	}
	switch lvl {
	case rdSync.LevelRevoked:
		return "revoked"
	case rdSync.LevelMaintainer:
		return "maintainer"
	default:
		return "contributor"
	}
}

// loadNostrAuthorityResolver builds the nostr-native audit resolver from the
// pinned board coordinate and the signed role-grants in the local authoritative
// log. It performs NO campfire init and provisions NO .cf. Returns nil when the
// project is not nostr-native or the log/pin is unreadable, so callers degrade to
// non-annotated (but still correct) output.
func loadNostrAuthorityResolver() *nostrAuthorityResolver {
	dir, native := nostrNativeProject()
	if !native {
		return nil
	}
	owner, boardD, ok := rdSync.ParseBoardCoord(nostrPinnedBoard(dir))
	if !ok {
		return nil
	}
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		return nil
	}
	levels, _ := rdSync.DeriveLevels(events, owner, boardD)
	return &nostrAuthorityResolver{owner: owner, levels: levels}
}

func init() {
	sessionsCmd.Flags().Bool("json", false, "output as JSON")
	rootCmd.AddCommand(sessionsCmd)
}

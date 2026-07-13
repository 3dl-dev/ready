package main

import (
	"github.com/spf13/cobra"
)

// migrateCmd promotes the campfire→nostr cutover to a TOP-LEVEL `rd migrate`
// (ready-1c8), the operator-facing entry point for the nostr-native migration.
// It is a thin dispatcher over the existing, well-tested migrate/parity substrate
// (cmd/rd/nostr.go): the default action re-emits the campfire item set as nostr
// events (30301 board, 30302 cards, NIP-34 status log) preserving ids + full
// history; `--parity` instead asserts item-for-item FIELD equality between the
// campfire source and the nostr projection, exiting NON-ZERO on ANY mismatch.
//
// Reuse, not reimplementation: the RunE bodies live once on nostrMigrateCmd /
// nostrParityCmd and are invoked with THIS command as the flag scope, so the
// top-level flags below resolve identically. That keeps a single source of truth
// for the migration/parity logic — the two surfaces can never drift.
var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Re-emit the legacy item set as nostr events; --parity verifies the migration",
	Long: `Migrate this project's legacy (local JSONL) work items to the nostr-native backend.

Default (no flags): re-emit every item as nostr events — a 30301 board, one 30302
card per item materializing current state, and one NIP-34 status event per history
entry — into the local authoritative log and the locked write relays. Item ids and
full status history (with original timestamps, close-reasons, and actors) are
preserved. Migration is NON-DESTRUCTIVE: the legacy source is left intact.
Idempotent by event id (safe to re-run).

--parity: do NOT migrate. Instead assert item-for-item parity between the legacy
source and the nostr projection — every item compared on title, status, priority,
type, deps, gate, labels, eta, assignee, tree/scope fields, and full history length
+ close-reasons + provenance. Exits NON-ZERO on ANY single-field mismatch or a
lost/added item, so it is a hard CI gate on a faithful migration.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if parity, _ := cmd.Flags().GetBool("parity"); parity {
			// Parity mode: reuse the parity RunE with THIS command's flag scope
			// (verbose/sample resolve on migrateCmd's flags below).
			return nostrParityCmd.RunE(cmd, args)
		}
		// Migration mode: reuse the migrate RunE (local-only/limit/only/all).
		return nostrMigrateCmd.RunE(cmd, args)
	},
}

func init() {
	// --parity selects verification instead of migration.
	migrateCmd.Flags().Bool("parity", false, "assert item-for-item parity (legacy source == nostr projection) and exit non-zero on any mismatch, instead of migrating")

	// Migration flags — same semantics as `rd nostr migrate`.
	migrateCmd.Flags().Bool("local-only", false, "append to the local authoritative log only; skip relay publish")
	migrateCmd.Flags().Int("limit", 0, "migrate at most N items (0 = all); deterministic by id")
	migrateCmd.Flags().StringSlice("only", nil, "migrate ONLY these item ids (comma-separated)")
	migrateCmd.Flags().Bool("all", true, "include terminal items (done/cancelled/failed) — default true so history is not lost")

	// Parity flags — same semantics as `rd nostr parity`.
	migrateCmd.Flags().Bool("verbose", false, "with --parity: print every item (matched and mismatched), not just mismatches")
	migrateCmd.Flags().Bool("sample", false, "with --parity: the projection is an intentional subset (e.g. from --limit): compare only the projected ids instead of failing on the missing source items. WITHOUT this flag, projected<source is a HARD parity FAILURE (a lost item).")

	rootCmd.AddCommand(migrateCmd)
}

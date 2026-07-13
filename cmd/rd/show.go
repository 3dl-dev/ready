package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var showCmd = &cobra.Command{
	Use:   "show <item-id>",
	Short: "Show a work item",
	Long: `Show full details of a work item — status, context, dependencies, audit trail.

Example:
  rd show ready-a1b
  rd show ready-a1b --json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		itemID := args[0]
		auditFlag, _ := cmd.Flags().GetBool("audit")
		reconcileFlag, _ := cmd.Flags().GetBool("reconcile")

		// --reconcile (ready-f58, migrated from the deleted `rd nostr show`):
		// cache-fill this item from the read relays into the local authoritative log
		// BEFORE reconstructing it, so a fresh/clean cache can still show the item.
		// The local log stays authoritative; the relay fetch only adds trust-gated
		// events that were missing locally.
		var reconcileNote string
		if reconcileFlag {
			note, err := nostrReconcileItemIntoLog(itemID)
			if err != nil {
				cmd.SilenceUsage = true
				return err
			}
			reconcileNote = note
		}

		// The read + audit authority is the nostr projection or local JSONL —
		// never a campfire store. `rd show` (incl. --audit) provisions NO campfire
		// store.db (ready-cb6 I7): byIDFromJSONLOrStore resolves from the nostr
		// projection (nostr-native) or mutations.jsonl, and the audit path below
		// resolves authority from the same signed sources.
		item, err := byIDFromJSONLOrStore(itemID)
		if err != nil {
			cmd.SilenceUsage = true
			if reconcileNote != "" {
				return fmt.Errorf("%w; %s", err, reconcileNote)
			}
			return err
		}

		// CUTOVER (ready-cb6 I7): cross-campfire dep resolution was a
		// campfire-federation concern requiring a multi-campfire store. A nostr
		// board is a single project, so there is no cross-store topology to
		// resolve; the display has been removed with pkg/crossdep.
		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(item)
		}

		// Human-readable output.
		fmt.Printf("ID:       %s\n", item.ID)
		fmt.Printf("Title:    %s\n", item.Title)
		fmt.Printf("Status:   %s\n", item.Status)
		fmt.Printf("Type:     %s\n", item.Type)
		fmt.Printf("Priority: %s\n", item.Priority)
		fmt.Printf("For:      %s\n", item.For)
		if item.By != "" {
			fmt.Printf("By:       %s\n", item.By)
		}
		if item.Project != "" {
			fmt.Printf("Project:  %s\n", formatCampfireIDForDisplay(item.Project))
		}
		if item.Level != "" {
			fmt.Printf("Level:    %s\n", item.Level)
		}
		if item.ETA != "" {
			fmt.Printf("ETA:      %s (%s)\n", item.ETA, formatETA(item.ETA))
		}
		if item.Due != "" {
			fmt.Printf("Due:      %s\n", item.Due)
		}
		if item.ParentID != "" {
			fmt.Printf("Parent:   %s\n", item.ParentID)
		}
		if len(item.BlockedBy) > 0 {
			fmt.Printf("Blocked by: %s\n", strings.Join(item.BlockedBy, ", "))
		}
		if len(item.Blocks) > 0 {
			fmt.Printf("Blocks:   %s\n", strings.Join(item.Blocks, ", "))
		}
		if item.WaitingOn != "" {
			fmt.Printf("Waiting on: %s (%s)\n", item.WaitingOn, item.WaitingType)
		}
		if len(item.Labels) > 0 {
			fmt.Printf("Labels:   %s\n", strings.Join(item.Labels, ", "))
		}
		if item.Context != "" {
			fmt.Printf("\nContext:\n%s\n", item.Context)
		}
		if len(item.History) > 0 {
			// --audit: annotate each history entry with the cf-authority scope the
			// actor acted under. Silent (no annotation) for non-pubkey actors and
			// items whose actors carry no delegation grant.
			var auth auditLabeler
			if auditFlag {
				// Nostr-native: resolve authority from the signed nostr projection
				// (the signed kind-39301 role-grants). NEVER requireClient()/
				// protocol.Init here — that provisioned .cf/identity.json, the exact
				// artifact the cutover eliminates (ready-6ef veracity fix). Assign
				// only a non-nil concrete resolver so the interface nil-check below
				// is sound.
				if r := loadNostrAuthorityResolver(); r != nil {
					auth = r
				}
			}
			fmt.Printf("\nHistory:\n")
			for _, h := range item.History {
				actor := h.ChangedBy
				ts := h.Timestamp
				note := ""
				if h.Note != "" {
					note = " — " + h.Note
				}
				authNote := ""
				if auth != nil {
					if label := auth.label(actor); label != "" {
						authNote = "  [authority: " + label + "]"
					}
				}
				fmt.Printf("  [%s] %s → %s by %s%s%s\n", ts, h.FromStatus, h.ToStatus, actor, note, authNote)
			}
		}
		fmt.Println()
		// Campfire is fully removed (I7); a nostr-native item never carries a
		// CampfireID. Only print the label for a legacy item that still has one,
		// so `rd show` never emits an empty "Campfire:" on the happy path.
		if item.CampfireID != "" {
			fmt.Printf("Campfire: %s\n", formatCampfireIDForDisplay(item.CampfireID))
		}
		fmt.Printf("Msg ID:   %s\n", item.MsgID)
		if reconcileNote != "" {
			fmt.Printf("(%s)\n", reconcileNote)
		}
		return nil
	},
}

func init() {
	showCmd.Flags().Bool("audit", false, "annotate each history entry with the cf-authority grant scope its actor acted under")
	showCmd.Flags().Bool("reconcile", false, "cache-fill this item from the read relays into the local log before reconstructing (local log stays authoritative)")
	rootCmd.AddCommand(showCmd)
}

package main

import (
	"fmt"
	"os"
	"time"

	"github.com/campfire-net/ready/pkg/state"
	"github.com/campfire-net/ready/pkg/timeparse"
	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{
	Use:   "update <item-id>",
	Short: "Update fields on a work item",
	Long: `Update one or more mutable fields on a work item.

Field flags: --title, --context, --priority, --eta, --due
Status flags: --status, --waiting-on, --waiting-type (auto-sets status=waiting when --waiting-on is used)
Note flag:    --note (used as reason for status transitions)

Examples:
  rd update ready-a1b --priority p0 --eta 2026-04-01T12:00:00Z
  rd update ready-a1b --title "New title" --context "Updated context"
  rd update ready-a1b --status waiting --waiting-on "vendor quote" --waiting-type vendor
  rd update ready-a1b --waiting-on "design review" --waiting-type person`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Helpful redirect for --blocks (agents may try bd-style dep wiring via update).
		if blocks, _ := cmd.Flags().GetString("blocks"); blocks != "" {
			return fmt.Errorf("--blocks is not a flag on rd update. Use: rd dep add <this-item> %s", blocks)
		}

		itemID := args[0]
		title, _ := cmd.Flags().GetString("title")
		context, _ := cmd.Flags().GetString("context")
		priority, _ := cmd.Flags().GetString("priority")
		eta, _ := cmd.Flags().GetString("eta")
		due, _ := cmd.Flags().GetString("due")
		level, _ := cmd.Flags().GetString("level")
		statusTo, _ := cmd.Flags().GetString("status")
		waitingOn, _ := cmd.Flags().GetString("waiting-on")
		waitingType, _ := cmd.Flags().GetString("waiting-type")
		note, _ := cmd.Flags().GetString("note")
		claim, _ := cmd.Flags().GetBool("claim")

		// --claim alone implies --status active (bd-compat: bd update --claim sets active).
		if claim && statusTo == "" {
			statusTo = state.StatusActive
		}

		// Auto-set status=waiting if --waiting-on is set without --status.
		if waitingOn != "" && statusTo == "" {
			statusTo = state.StatusWaiting
		}

		// Validate that at least one flag is set.
		hasFieldUpdate := title != "" || context != "" || priority != "" ||
			eta != "" || due != "" || level != ""
		hasStatusUpdate := statusTo != "" || waitingOn != ""

		if !hasFieldUpdate && !hasStatusUpdate && !claim {
			return fmt.Errorf("no fields to update: specify at least one of --title, --context, --priority, --eta, --due, --level, --status, --waiting-on, --claim")
		}

		// Resolve status aliases (bd-compat).
		if statusTo != "" {
			if canonical := resolveStatus(statusTo); canonical != statusTo {
				fmt.Fprintf(os.Stderr, "warning: status %q is a bd alias — using %q instead\n", statusTo, canonical)
				statusTo = canonical
			}
		}

		// Normalize ETA/due to UTC if provided.
		if eta != "" {
			normalized, err := timeparse.Parse(eta, time.Now())
			if err != nil {
				return fmt.Errorf("invalid --eta: %w", err)
			}
			eta = normalized
		}
		if due != "" {
			normalized, err := timeparse.Parse(due, time.Now())
			if err != nil {
				return fmt.Errorf("invalid --due: %w", err)
			}
			due = normalized
		}

		// nostr-native write path (ready-cb6): no .cf, secp256k1 signer. Only path.
		if _, native := nostrNativeProject(); native {
			return runUpdateNostr(itemID, nostrUpdateSpec{
				title: title, context: context, priority: priority,
				eta: eta, due: due, level: level,
				statusTo: statusTo, waitingOn: waitingOn, waitingType: waitingType, note: note,
				hasFieldUpdate: hasFieldUpdate, hasStatusUpdate: hasStatusUpdate, claim: claim,
			})
		}
		return errNotNostrProject()
	},
}

func init() {
	updateCmd.Flags().String("title", "", "new title")
	updateCmd.Flags().String("context", "", "new context/description")
	updateCmd.Flags().String("priority", "", "priority: p0, p1, p2, p3")
	updateCmd.Flags().String("eta", "", "ETA in RFC3339 format")
	updateCmd.Flags().String("due", "", "hard deadline in RFC3339 format")
	updateCmd.Flags().String("level", "", "level: epic, task, subtask")
	updateCmd.Flags().String("status", "", "status: inbox, active, scheduled, waiting, done, cancelled, failed")
	updateCmd.Flags().String("waiting-on", "", "what we are waiting on (auto-sets status=waiting if no --status given)")
	updateCmd.Flags().String("waiting-type", "", "waiting type: person, vendor, client, date, event, external, agent, gate")
	updateCmd.Flags().String("note", "", "note or reason (used as reason for status transitions)")
	updateCmd.Flags().String("blocks", "", "")
	_ = updateCmd.Flags().MarkHidden("blocks")
	updateCmd.Flags().Bool("claim", false, "claim the item: set by=sender and transition to active (bd-compat: bd update --claim)")
	rootCmd.AddCommand(updateCmd)
}

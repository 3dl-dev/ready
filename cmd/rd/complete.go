package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// completeReasonWithTrace appends agent traceability metadata (branch, session)
// to the close reason so it lands durably in the nostr audit history (the close
// event's note). This is what makes --branch/--session observable rather than
// silently discarded: each provided flag is annotated onto the reason.
func completeReasonWithTrace(reason, branch, session string) string {
	var b strings.Builder
	b.WriteString(reason)
	if branch != "" {
		fmt.Fprintf(&b, " [branch=%s]", branch)
	}
	if session != "" {
		fmt.Fprintf(&b, " [session=%s]", session)
	}
	return b.String()
}

// completeCmd closes an item with resolution=done, adding agent metadata (branch, session).
var completeCmd = &cobra.Command{
	Use:   "complete <item-id>",
	Short: "Signal a work item is finished (agent-facing)",
	Long: `Close a work item with resolution=done, recording agent metadata.

Agent-facing completion command. Equivalent to rd done but supports
--branch and --session flags for traceability in agent workflows; when
provided, each is annotated onto the close reason so it lands in the
item's audit history.

Example:
  rd complete rudi-utt --reason "done" --branch work/rudi-utt
  rd complete rudi-utt --reason "implemented and merged" --branch work/rudi-utt --session abc123`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		itemID := args[0]
		reason, _ := cmd.Flags().GetString("reason")
		branch, _ := cmd.Flags().GetString("branch")
		session, _ := cmd.Flags().GetString("session")

		if reason == "" {
			return fmt.Errorf("--reason is required (why is this item being closed?)")
		}

		// nostr-native write path (ready-cb6): no .cf, secp256k1 signer. Only path.
		// branch/session are agent-facing traceability metadata with no dedicated
		// item-state field, so they are folded into the close reason — landing
		// durably in the nostr audit history rather than being discarded.
		if _, native := nostrNativeProject(); native {
			return runCloseNostr(itemID, "done", completeReasonWithTrace(reason, branch, session), "closed")
		}
		return errNotNostrProject()
	},
}

func init() {
	completeCmd.Flags().String("reason", "", "reason for completing")
	completeCmd.Flags().String("branch", "", "git branch name where work was done")
	completeCmd.Flags().String("session", "", "session ID for traceability")
	rootCmd.AddCommand(completeCmd)
}

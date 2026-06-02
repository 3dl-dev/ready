package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/spf13/cobra"

	rdSync "github.com/campfire-net/ready/pkg/sync"
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Manage campfire sync for this project",
	Long: `Manage campfire sync for the current ready project.

rd sync works in three modes:

  JSONL-only (offline, no campfire):
    All mutations are stored locally. No sync state.

  Campfire-backed (campfire configured):
    Mutations write locally first, then post to campfire.
    Failed campfire sends buffer to .ready/pending.jsonl.

SUBCOMMANDS
  rd sync status    Show sync state: pending count, last sync time
  rd sync pull      Pull missed campfire messages into local JSONL`,
}

var syncStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the current sync state",
	Long: `Show the current outbound sync state for this project.

Output includes:
  - Number of mutations buffered in .ready/pending.jsonl
  - Last successful sync time (most recent mutation posted to campfire)
  - Last synced message ID

Use --json for machine-readable output.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		projectDir, ok := readyProjectDir()
		if !ok {
			return fmt.Errorf("not a ready project directory (run 'rd init' first)")
		}

		status, err := rdSync.GetStatus(projectDir)
		if err != nil {
			return fmt.Errorf("reading sync state: %w", err)
		}

		_, _, hasCampfire := projectRoot()

		if jsonOutput {
			out := map[string]interface{}{
				"pending_count":      status.PendingCount,
				"has_synced":         status.HasSynced,
				"last_synced_msg_id": status.LastSyncedMsgID,
				"campfire_configured": hasCampfire,
			}
			if status.HasSynced {
				out["last_synced_at"] = status.LastSyncedAt.UTC().Format(time.RFC3339Nano)
			} else {
				out["last_synced_at"] = nil
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		// Human-readable output.
		if !hasCampfire {
			fmt.Println("sync: offline mode (no campfire configured)")
			fmt.Println("  all mutations stored locally in .ready/mutations.jsonl")
			fmt.Println("  run 'rd init' or connect a campfire to enable sync")
			return nil
		}

		if status.PendingCount > 0 {
			fmt.Printf("sync: %d mutation(s) pending\n", status.PendingCount)
		} else {
			fmt.Println("sync: up to date")
		}

		if status.HasSynced {
			fmt.Printf("  last synced: %s\n", status.LastSyncedAt.Local().Format("2006-01-02 15:04:05"))
			if status.LastSyncedMsgID != "" {
				msgShort := status.LastSyncedMsgID
				if len(msgShort) > 16 {
					msgShort = msgShort[:16] + "..."
				}
				fmt.Printf("  last msg:    %s\n", msgShort)
			}
		} else {
			fmt.Println("  last synced: never")
		}

		if status.PendingCount > 0 {
			fmt.Println()
			fmt.Println("  buffered mutations will sync on next successful campfire send")
		}

		return nil
	},
}

var syncPullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Pull missed campfire messages into local JSONL",
	Long: `Pull campfire messages missed since last sync into local JSONL.

Replays messages with work:* tags from the project campfire, deduplicating
against already-present records in .ready/mutations.jsonl. New records are
appended in campfire arrival order (last-writer-wins for concurrent edits).

Warns if offline duration exceeds the campfire max-ttl (possible message loss).

Use --json for machine-readable output.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		projectDir, ok := readyProjectDir()
		if !ok {
			return fmt.Errorf("not a ready project directory (run 'rd init' first)")
		}

		campfireID, _, hasCampfire := projectRoot()
		if !hasCampfire || campfireID == "" {
			if jsonOutput {
				out := map[string]interface{}{
					"pulled":          0,
					"skipped":         0,
					"campfire_present": false,
					"error":           "no campfire configured — run 'rd init' to connect",
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			fmt.Println("sync pull: offline mode (no campfire configured)")
			fmt.Println("  run 'rd init' to connect a campfire and enable sync")
			return nil
		}

		s, err := openStore()
		if err != nil {
			return fmt.Errorf("opening store: %w", err)
		}
		defer s.Close()

		jsonlFilePath := filepath.Join(projectDir, ".ready", "mutations.jsonl")

		result, err := rdSync.Pull(s, campfireID, jsonlFilePath, projectDir, 0)
		if err != nil {
			return fmt.Errorf("sync pull: %w", err)
		}

		if jsonOutput {
			out := map[string]interface{}{
				"pulled":           result.Pulled,
				"skipped":          result.Skipped,
				"campfire_present": true,
				"last_pull_at":     time.Unix(0, result.LastPullAt).UTC().Format(time.RFC3339Nano),
			}
			if result.GapWarning != "" {
				out["gap_warning"] = result.GapWarning
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		// Human-readable output.
		if result.GapWarning != "" {
			fmt.Fprintln(os.Stderr, result.GapWarning)
		}

		if result.Pulled == 0 && result.Skipped == 0 {
			fmt.Println("sync pull: already up to date")
			return nil
		}

		fmt.Printf("sync pull: %d message(s) pulled", result.Pulled)
		if result.Skipped > 0 {
			fmt.Printf(", %d duplicate(s) skipped", result.Skipped)
		}
		fmt.Println()
		return nil
	},
}

// campfireReadClient is the subset of protocol.Client needed by clientLister.
// Extracted as an interface so the type can be tested without a real campfire.
type campfireReadClient interface {
	Read(req protocol.ReadRequest) (*protocol.ReadResult, error)
}

// clientLister adapts protocol.Client to the rdSync.MessageLister interface.
// It uses client.Read() which fetches messages through the transport layer
// (filesystem or remote), ensuring campfire messages are accessible even if they
// predate the local join (ready-5cd).
type clientLister struct {
	client campfireReadClient
}

func (cl *clientLister) ListMessages(campfireID string, afterTimestamp int64, filter ...store.MessageFilter) ([]store.MessageRecord, error) {
	// Extract tags from filter if provided.
	var tags []string
	if len(filter) > 0 && len(filter[0].Tags) > 0 {
		tags = filter[0].Tags
	}

	// Read all messages with the requested tags. For hosted campfires this
	// fetches from the remote server; for filesystem transport it reads the
	// local campfire directory.
	result, err := cl.client.Read(protocol.ReadRequest{
		CampfireID: campfireID,
		Tags:       tags,
	})
	if err != nil {
		return nil, fmt.Errorf("reading campfire messages: %w", err)
	}

	// Convert protocol.Message to store.MessageRecord and apply timestamp filter.
	var records []store.MessageRecord
	for _, msg := range result.Messages {
		if afterTimestamp > 0 && msg.Timestamp <= afterTimestamp {
			continue
		}
		records = append(records, store.MessageRecord{
			ID:         msg.ID,
			CampfireID: campfireID,
			Timestamp:  msg.Timestamp,
			ReceivedAt: msg.Timestamp,
			Payload:    msg.Payload,
			Tags:       msg.Tags,
			Sender:     msg.Sender,
		})
	}
	return records, nil
}


// autoSyncPull performs a best-effort campfire sync pull before read commands
// (rd list, rd ready, rd show). If no campfire is configured, or the pull fails
// for any reason, the error is silently discarded so the read command continues
// uninterrupted.
//
// This eliminates the need for users to run 'rd sync pull' manually after a team
// member makes changes (ready-341).
func autoSyncPull() {
	campfireID, projectDir, hasCampfire := projectRoot()
	if !hasCampfire || campfireID == "" {
		return
	}

	client, err := requireClient()
	if err != nil {
		return
	}

	mutationsPath := filepath.Join(projectDir, ".ready", "mutations.jsonl")
	lister := &clientLister{client: client}
	_, _ = rdSync.Pull(lister, campfireID, mutationsPath, projectDir, 0)
}

func init() {
	syncCmd.AddCommand(syncStatusCmd)
	syncCmd.AddCommand(syncPullCmd)
	rootCmd.AddCommand(syncCmd)
}

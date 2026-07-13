package main

import (
	"fmt"
	"path/filepath"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/spf13/cobra"

	rdSync "github.com/campfire-net/ready/pkg/sync"
)

// syncCmd is the top-level `rd sync` — negentropy-sync the local nostr log with
// the relays so two machines converge on identical work-item state (ready-9ac
// promoted this from `rd nostr sync`; the former campfire `rd sync status/pull`
// surface is retired with the campfire command surface).
//
// The RunE body lives once on nostrSyncCmd (cmd/rd/nostr.go) and is reused here
// with THIS command as the scope, so the promoted surface and the substrate can
// never drift. nostrSyncCmd reads no flags of its own.
var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Negentropy-sync the local nostr log with the relays (two-machine convergence)",
	Long: `Reconcile the local append-only nostr event log against the configured
relays via NIP-77 negentropy and perform the resulting download + upload, so two
machines converge on identical work-item state by transferring only the
difference. The download is web-of-trust gated: a relay cannot inject a
validly-signed event authored by an ungranted key.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return nostrSyncCmd.RunE(cmd, args)
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
// This eliminates the need for users to run a manual pull after a team member
// makes changes (ready-341). It is part of the vestigial campfire backend that a
// later item removes with the campfire SDK.
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
	rootCmd.AddCommand(syncCmd)
}

package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	trust "github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/spf13/cobra"
)

// grantHolder is one active cf-authority delegation grant-holder, as shown by
// `rd sessions`.
type grantHolder struct {
	Pubkey       string `json:"pubkey"`
	Convention   string `json:"convention"`
	OpPattern    string `json:"op_pattern"`
	ExpiresAt    int64  `json:"expires_at"`     // nanoseconds since epoch
	TTLRemaining string `json:"ttl_remaining"`  // human-readable
	GrantMsgID   string `json:"grant_msg_id"`
}

// activeGrantHolders projects delegation:grant and identity:revoked messages into
// the set of active grant-holders: a holder whose grant is the most recent for
// its key, is not revoked, and is not expired at `now`. Pure for testability.
func activeGrantHolders(grants, revokes []protocol.Message, now time.Time) []grantHolder {
	revoked := make(map[string]bool, len(revokes))
	for _, msg := range revokes {
		var p struct {
			ChildPubkey string `json:"child_pubkey"`
		}
		if err := json.Unmarshal(msg.Payload, &p); err == nil && p.ChildPubkey != "" {
			revoked[p.ChildPubkey] = true
		}
	}

	// Most recent grant per child key (messages are in arrival order).
	latest := make(map[string]grantHolder)
	order := make([]string, 0)
	for _, msg := range grants {
		gp, err := trust.UnmarshalGrantPayloadCBOR(msg.Payload)
		if err != nil || len(gp.Capabilities) == 0 {
			continue
		}
		key := hex.EncodeToString(gp.ChildPubkey)
		cap := gp.Capabilities[0]
		if _, seen := latest[key]; !seen {
			order = append(order, key)
		}
		latest[key] = grantHolder{
			Pubkey:     key,
			Convention: cap.Convention,
			OpPattern:  cap.OpPattern,
			ExpiresAt:  cap.Until,
			GrantMsgID: msg.ID,
		}
	}

	holders := make([]grantHolder, 0, len(order))
	for _, key := range order {
		h := latest[key]
		if revoked[key] || h.ExpiresAt <= now.UnixNano() {
			continue
		}
		h.TTLRemaining = time.Duration(h.ExpiresAt - now.UnixNano()).Round(time.Minute).String()
		holders = append(holders, h)
	}
	return holders
}

var sessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "List active delegation grant-holders",
	Long: `List the active cf-authority delegation grant-holders for this project's
campfire — the identities admit has granted work capabilities to, minus any
revoked (rd kill) or expired grants. Shows each holder's scope and TTL.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonOut, _ := cmd.Flags().GetBool("json")
		campfireID, _, ok := projectRoot()
		if !ok {
			return fmt.Errorf("no campfire project found — run 'rd init' first")
		}
		client, err := requireClient()
		if err != nil {
			return err
		}
		grants, err := client.Read(protocol.ReadRequest{CampfireID: campfireID, Tags: []string{delegationGrantTag}})
		if err != nil {
			return fmt.Errorf("reading delegation grants: %w", err)
		}
		revokes, err := client.Read(protocol.ReadRequest{CampfireID: campfireID, Tags: []string{identityRevokedTag}})
		if err != nil {
			return fmt.Errorf("reading revocations: %w", err)
		}

		holders := activeGrantHolders(messagesOf(grants), messagesOf(revokes), time.Now())

		if jsonOut {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(holders)
		}

		if len(holders) == 0 {
			fmt.Fprintln(os.Stdout, "no active grant-holders")
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "PUBKEY\tSCOPE\tTTL")
		for _, h := range holders {
			key := h.Pubkey
			if len(key) > 12 {
				key = key[:12] + "..."
			}
			fmt.Fprintf(tw, "%s\t%s:%s\t%s\n", key, h.Convention, h.OpPattern, h.TTLRemaining)
		}
		return tw.Flush()
	},
}

// messagesOf returns the messages from a ReadResult, tolerating nil.
func messagesOf(r *protocol.ReadResult) []protocol.Message {
	if r == nil {
		return nil
	}
	return r.Messages
}

// identityRevokedTag carries {"child_pubkey": hex} revoking a grant-holder
// (posted by rd kill). delegationGrantTag is defined in delegation_grant.go.
const identityRevokedTag = "identity:revoked"

func init() {
	sessionsCmd.Flags().Bool("json", false, "output as JSON")
	rootCmd.AddCommand(sessionsCmd)
}

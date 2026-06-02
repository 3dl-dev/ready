package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	trust "github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
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

// scopeClient is the slice of protocol.Client that scopeForKey needs.
type scopeClient interface {
	GetMembership(campfireID string) (*store.Membership, error)
	Read(req protocol.ReadRequest) (*protocol.ReadResult, error)
}

// scopeForKey reports whether keyHex is authorized to claim work items in the
// campfire (used by `rd ready --scope`), and a note when it is not. The campfire
// creator (root principal) is always allowed; otherwise the key must hold an
// active (non-revoked, non-expired) delegation grant whose OpPattern covers
// "claim". A key with no grant is reported as out of scope for this view.
func scopeForKey(client scopeClient, campfireID, keyHex string) (bool, string) {
	if m, err := client.GetMembership(campfireID); err == nil && m != nil && m.CreatorPubkey == keyHex {
		return true, ""
	}
	grants, _ := client.Read(protocol.ReadRequest{CampfireID: campfireID, Tags: []string{delegationGrantTag}})
	revokes, _ := client.Read(protocol.ReadRequest{CampfireID: campfireID, Tags: []string{identityRevokedTag}})
	for _, h := range activeGrantHolders(messagesOf(grants), messagesOf(revokes), time.Now()) {
		if h.Pubkey == keyHex {
			if opPatternCovers(h.OpPattern, "claim") {
				return true, ""
			}
			return false, fmt.Sprintf("grant for %s does not cover 'claim' (scope: %s:%s)", shortKey(keyHex), h.Convention, h.OpPattern)
		}
	}
	return false, fmt.Sprintf("no active grant for %s (revoked, expired, or not a granted identity)", shortKey(keyHex))
}

// opPatternCovers matches a cf-authority OpPattern against an operation. "*"
// matches any operation; otherwise the pattern is pipe-alternation of exact ops.
func opPatternCovers(pattern, op string) bool {
	if pattern == "*" {
		return true
	}
	for _, alt := range strings.Split(pattern, "|") {
		if alt == "*" || alt == op {
			return true
		}
	}
	return false
}

func shortKey(k string) string {
	if len(k) > 12 {
		return k[:12] + "..."
	}
	return k
}

// authorityResolver renders the cf-authority attribution for an actor pubkey
// (used by `rd show --audit`): owner, granted scope, expired, revoked, or none.
type authorityResolver struct {
	creator    string
	revoked    map[string]bool
	grantByKey map[string]trust.GrantPayload
	now        time.Time
}

// newAuthorityResolver builds the resolver from the campfire creator and the
// delegation:grant / identity:revoked messages. Pure for testability.
func newAuthorityResolver(creator string, grants, revokes []protocol.Message, now time.Time) *authorityResolver {
	r := &authorityResolver{
		creator:    creator,
		revoked:    make(map[string]bool),
		grantByKey: make(map[string]trust.GrantPayload),
		now:        now,
	}
	for _, msg := range revokes {
		var p struct {
			ChildPubkey string `json:"child_pubkey"`
		}
		if err := json.Unmarshal(msg.Payload, &p); err == nil && p.ChildPubkey != "" {
			r.revoked[p.ChildPubkey] = true
		}
	}
	for _, msg := range grants {
		gp, err := trust.UnmarshalGrantPayloadCBOR(msg.Payload)
		if err != nil || len(gp.Capabilities) == 0 {
			continue
		}
		r.grantByKey[hex.EncodeToString(gp.ChildPubkey)] = gp
	}
	return r
}

// label returns a short authority description for an actor, or "" for actors
// that are not pubkeys (e.g. "system" or an email).
func (r *authorityResolver) label(actor string) string {
	if len(actor) != 64 || !isHex(actor) {
		return ""
	}
	if actor == r.creator {
		return "owner (root principal)"
	}
	if r.revoked[actor] {
		return "revoked"
	}
	gp, ok := r.grantByKey[actor]
	if !ok {
		return "no delegation grant"
	}
	cap := gp.Capabilities[0]
	if cap.Until <= r.now.UnixNano() {
		return "grant expired"
	}
	return fmt.Sprintf("%s:%s", cap.Convention, cap.OpPattern)
}

// loadAuthorityResolver reads the campfire's grants/revocations/creator and
// builds an authorityResolver. Returns nil on any read failure (so callers can
// degrade to non-annotated output).
func loadAuthorityResolver(client scopeClient, campfireID string) *authorityResolver {
	var creator string
	if m, err := client.GetMembership(campfireID); err == nil && m != nil {
		creator = m.CreatorPubkey
	}
	grants, err := client.Read(protocol.ReadRequest{CampfireID: campfireID, Tags: []string{delegationGrantTag}})
	if err != nil {
		return nil
	}
	revokes, err := client.Read(protocol.ReadRequest{CampfireID: campfireID, Tags: []string{identityRevokedTag}})
	if err != nil {
		return nil
	}
	return newAuthorityResolver(creator, messagesOf(grants), messagesOf(revokes), time.Now())
}

// identityRevokedTag carries {"child_pubkey": hex} revoking a grant-holder
// (posted by rd kill). delegationGrantTag is defined in delegation_grant.go.
const identityRevokedTag = "identity:revoked"

func init() {
	sessionsCmd.Flags().Bool("json", false, "output as JSON")
	rootCmd.AddCommand(sessionsCmd)
}

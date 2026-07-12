package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	rdSync "github.com/campfire-net/ready/pkg/sync"
	"github.com/spf13/cobra"
)

var revokeCmd = &cobra.Command{
	Use:   "revoke <pubkey>",
	Short: "Revoke a member's role in the project campfire",
	Long: `Revoke a member's role by posting a work:role-grant with role="revoked".

The target must be identified by their 64-character hex public key.
Name-based revocation is not supported because name resolution produces
campfire IDs, not member pubkeys — using one in place of the other is a
semantic type error (ready-34d).

Use --retroactive to also post retroactive revocation records for every member
that the revoked key previously admitted (reads audit trail from campfire message log).

EXAMPLES
  rd revoke abcdef1234...
  rd revoke abcdef1234... --retroactive`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		target := args[0]
		retroactive, _ := cmd.Flags().GetBool("retroactive")

		// Validate target is a 64-char hex pubkey.
		// Name-based resolution is intentionally not supported here because
		// resolveName() resolves names to campfire IDs — a different 64-char hex
		// type — not to member pubkeys. Using a campfire ID as a pubkey in a
		// role-grant message is a semantic type confusion (ready-34d).
		// Use the member's full 64-character hex public key instead.
		if len(target) != 64 || !isHex(target) {
			return fmt.Errorf("revoke target %q is not a valid pubkey: must be a 64-character hex string\n  hint: use the member's public key, not a name or campfire ID", target)
		}
		pubKeyHex := target

		// NOSTR-NATIVE default path (ready-477): revocation = publish an owner-signed
		// kind-39301 role=revoked grant + regenerate the relay write-allowlist. This
		// runs BEFORE any projectRoot()/requireClient() so the cf-authority delegation
		// path is never invoked and no .cf is provisioned. Prospective by default;
		// retroactive (compromise) repudiation is 'rd nostr revoke --from <unix>'.
		if dir, native := nostrNativeProject(); native {
			if retroactive {
				return fmt.Errorf("--retroactive is campfire-only; on a nostr-native project use 'rd nostr revoke %s --from <unix>' for retroactive (compromise) repudiation", pubKeyHex)
			}
			return runNostrGrantRevoke(dir, pubKeyHex, rdSync.RoleRevoked, "", 0)
		}

		campfireID, _, ok := projectRoot()
		if !ok {
			return fmt.Errorf("no campfire project found — run 'rd init' first")
		}

		client, err := requireClient()
		if err != nil {
			return err
		}

		exec, _, execErr := requireExecutor()
		if execErr != nil {
			return fmt.Errorf("initializing executor: %w", execErr)
		}

		decl, declErr := loadDeclaration("role-grant")
		if declErr != nil {
			return fmt.Errorf("loading role-grant declaration: %w", declErr)
		}

		ctx := context.Background()
		now := time.Now().UTC().Format(time.RFC3339)

		result, err := exec.Execute(ctx, decl, campfireID, map[string]any{
			"pubkey":     pubKeyHex,
			"role":       "revoked",
			"granted_at": now,
		})
		if err != nil {
			return fmt.Errorf("posting revocation: %w", err)
		}

		displayKey := pubKeyHex
		if len(displayKey) > 12 {
			displayKey = displayKey[:12] + "..."
		}
		fmt.Fprintf(os.Stdout, "revoked %s (msg %s)\n", displayKey, truncateID(result.MessageID, 12))

		if !retroactive {
			return nil
		}

		// Retroactive: find every member this key previously admitted and post
		// revocation records for them.
		admitted, auditErr := findMembersAdmittedBy(client, campfireID, pubKeyHex)
		if auditErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not read audit trail for retroactive revocation: %v\n", auditErr)
			return nil
		}

		if len(admitted) == 0 {
			fmt.Fprintf(os.Stdout, "retroactive: no members found that were admitted by this key\n")
			return nil
		}

		// Guard against runaway retroactive revocations from a compromised key
		// that issued thousands of grants. Cap at 500 and warn.
		const retroactiveCap = 500
		if len(admitted) > retroactiveCap {
			fmt.Fprintf(os.Stderr, "warning: %d admitted members found — retroactive revocation capped at %d\n", len(admitted), retroactiveCap)
			fmt.Fprintf(os.Stderr, "  review the full list manually with: rd list --type role-grant\n")
		}
		admitted = capAdmitted(admitted, retroactiveCap)

		for _, memberKey := range admitted {
			now2 := time.Now().UTC().Format(time.RFC3339)
			r2, err2 := exec.Execute(ctx, decl, campfireID, map[string]any{
				"pubkey":     memberKey,
				"role":       "revoked",
				"granted_at": now2,
			})
			if err2 != nil {
				fmt.Fprintf(os.Stderr, "warning: could not revoke %s...: %v\n", truncateID(memberKey, 12), err2)
				continue
			}
			fmt.Fprintf(os.Stdout, "retroactive revoke %s... (msg %s)\n", truncateID(memberKey, 12), truncateID(r2.MessageID, 12))
		}

		return nil
	},
}

// findMembersAdmittedBy reads the campfire message log and returns the set of
// pubkeys that were granted roles (via work:role-grant) by the given senderKey.
// Only non-revocation grants are returned — this finds who the revokedKey admitted.
//
// Security: only the pubkey field is extracted from each message payload.
// Extra payload fields (title, description, or any other item content) are
// intentionally discarded — audit entries reference pubkeys only, not item content.
func findMembersAdmittedBy(client campfireReader, campfireID, senderKey string) ([]string, error) {
	result, err := client.Read(protocol.ReadRequest{
		CampfireID: campfireID,
		Tags:       []string{"work:role-grant"},
		Sender:     senderKey,
	})
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	var admitted []string
	for _, msg := range result.Messages {
		// Extract only pubkey and role — any extra fields in the payload are
		// intentionally ignored to prevent content leaking into audit output.
		var payload struct {
			Pubkey string `json:"pubkey"`
			Role   string `json:"role"`
		}
		if err2 := json.Unmarshal(msg.Payload, &payload); err2 != nil {
			continue
		}
		// Only track grants (not revocations) issued by the revoked key.
		if payload.Role == "revoked" || payload.Pubkey == "" {
			continue
		}
		if !seen[payload.Pubkey] {
			seen[payload.Pubkey] = true
			admitted = append(admitted, payload.Pubkey)
		}
	}
	return admitted, nil
}

// capAdmitted returns at most maxCap entries from the admitted slice.
// This enforces a hard upper bound on retroactive revocations to prevent
// DoS from a compromised key that issued thousands of grants.
func capAdmitted(admitted []string, maxCap int) []string {
	if len(admitted) > maxCap {
		return admitted[:maxCap]
	}
	return admitted
}

func init() {
	revokeCmd.Flags().Bool("retroactive", false, "also revoke all members admitted by this key")
	rootCmd.AddCommand(revokeCmd)
}

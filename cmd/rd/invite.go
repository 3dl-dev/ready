package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/campfire-net/ready/pkg/rdconfig"
)

// inviteTokenPrefix is the prefix for rd invite tokens.
const inviteTokenPrefix = "rdx1_"

// invitePayload is the JSON structure embedded in an invite token.
type invitePayload struct {
	Version    int    `json:"v"`
	CampfireID string `json:"campfire_id"`
	PrivateKey string `json:"private_key"` // 64-char hex ed25519 seed
	Role       string `json:"role"`
	IssuedAt   int64  `json:"issued_at"`
	ExpiresAt  int64  `json:"expires_at"`
	Issuer     string `json:"issuer"` // 64-char hex pubkey of inviter (informational)
}

var inviteCmd = &cobra.Command{
	Use:   "invite",
	Short: "Generate a one-use invite token for this project",
	Long: `Generate a one-use invite token that lets a worker join this project
with a single 'rd join <token>' command.

The token contains a pre-provisioned ed25519 identity that is admitted to the
project campfire before the token is printed. The joiner uses this identity
automatically — no separate key exchange needed.

SECURITY
  The token contains a private key — treat it as a secret.
  Use --ttl to limit the exposure window (default 2h).
  The admitted identity is scoped to one campfire.

EXAMPLES
  rd invite                  # default: 2h TTL, member role
  rd invite --ttl 30m        # shorter TTL
  rd invite --role agent     # agent role`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ttl, _ := cmd.Flags().GetDuration("ttl")
		role, _ := cmd.Flags().GetString("role")

		// Nostr-native projects (a pinned board) mint a nostr mint-and-ship rd1_
		// token instead of a campfire admit + ed25519-seed rdx1_ token (ready-a49):
		// a fresh secp256k1 key, an owner-signed kind-39301 contributor grant, and
		// the board coord + relays + TTL + one-use nonce + secret bundled in the
		// token. The campfire path below stays for campfire-backed projects.
		if _, native := nostrNativeProject(); native {
			token, err := runNostrInvite(ttl)
			if err != nil {
				return err
			}
			fmt.Println(token)
			return nil
		}

		// Load project campfire ID.
		projectDir, ok := readyProjectDir()
		if !ok {
			return fmt.Errorf("no ready project found in current directory or parents")
		}

		syncCfg, err := rdconfig.LoadSyncConfig(projectDir)
		if err != nil {
			return fmt.Errorf("loading sync config: %w", err)
		}
		if syncCfg.CampfireID == "" {
			return fmt.Errorf("no campfire configured for this project (offline mode?)")
		}

		client, err := requireClient()
		if err != nil {
			return err
		}

		// Get the issuer's public key for the informational issuer field.
		issuerPubKey := client.PublicKeyHex()

		// Generate a fresh ed25519 keypair for the invitee.
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return fmt.Errorf("generating keypair: %w", err)
		}

		pubHex := hex.EncodeToString(pub)

		// Admit the generated pubkey to the campfire.
		admitRole := role
		if admitRole == "member" {
			admitRole = "" // admitMemberWithRole uses empty string for default member role
		}
		if err := admitMemberWithRole(client, syncCfg.CampfireID, pubHex, admitRole, "main campfire"); err != nil {
			return fmt.Errorf("admitting invite identity: %w", err)
		}

		// Build the invite payload. The private key seed is the first 32 bytes of
		// the ed25519 private key (which is seed || public key in Go's representation).
		seed := priv.Seed()
		now := time.Now()
		expiresAt := now.Add(ttl)

		payload := invitePayload{
			Version:    1,
			CampfireID: syncCfg.CampfireID,
			PrivateKey: hex.EncodeToString(seed),
			Role:       role,
			IssuedAt:   now.Unix(),
			ExpiresAt:  expiresAt.Unix(),
			Issuer:     issuerPubKey,
		}

		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshaling invite payload: %w", err)
		}

		// Post a server-side role-grant with expires_at so the TTL is enforced
		// beyond the client-side token check. An attacker who extracts the seed
		// from an expired token and bypasses the CLI can be rejected here:
		// checkServerSideInviteTTL reads this grant and rejects joins where
		// expires_at is past.
		if postErr := postInviteGrant(syncCfg.CampfireID, pubHex, admitRole, expiresAt); postErr != nil {
			// Non-fatal: log and continue. The client-side TTL check in
			// decodeInviteToken still applies; the server-side record is best-effort.
			fmt.Fprintf(os.Stderr, "warning: could not post server-side invite grant: %v\n", postErr)
		}

		// Encode: base64url(json), prefix with 'rdx1_'.
		token := inviteTokenPrefix + base64.RawURLEncoding.EncodeToString(payloadJSON)

		fmt.Println(token)
		return nil
	},
}

// postInviteGrant posts a work:role-grant with expires_at to record the
// server-side TTL for the invite token. This allows checkServerSideInviteTTL
// to reject admitted-but-never-joined keys after TTL expiry.
func postInviteGrant(campfireID, pubKeyHex, role string, expiresAt time.Time) error {
	exec, _, err := requireExecutor()
	if err != nil {
		return fmt.Errorf("initializing executor: %w", err)
	}
	decl, err := loadDeclaration("role-grant")
	if err != nil {
		return fmt.Errorf("loading role-grant declaration: %w", err)
	}
	grantRole := role
	if grantRole == "" {
		grantRole = "member"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	expiresAtStr := expiresAt.UTC().Format(time.RFC3339)
	ctx := context.Background()
	_, err = exec.Execute(ctx, decl, campfireID, map[string]any{
		"pubkey":     pubKeyHex,
		"role":       grantRole,
		"granted_at": now,
		"expires_at": expiresAtStr,
	})
	return err
}

// decodeInviteToken decodes an invite token, validating its format and expiry.
// Returns the payload or an error.
func decodeInviteToken(token string) (*invitePayload, error) {
	if len(token) <= len(inviteTokenPrefix) {
		return nil, fmt.Errorf("token too short")
	}

	// Strip prefix.
	encoded := token[len(inviteTokenPrefix):]

	// Base64url decode.
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid token encoding: %w", err)
	}

	// Parse JSON payload.
	var payload invitePayload
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return nil, fmt.Errorf("invalid token payload: %w", err)
	}

	// Validate version.
	if payload.Version != 1 {
		return nil, fmt.Errorf("unsupported token version %d", payload.Version)
	}

	// Validate required fields.
	if len(payload.CampfireID) != 64 || !isHex(payload.CampfireID) {
		return nil, fmt.Errorf("invalid campfire_id in token")
	}
	if len(payload.PrivateKey) != 64 || !isHex(payload.PrivateKey) {
		return nil, fmt.Errorf("invalid private_key in token")
	}

	// Check expiry.
	if time.Now().Unix() > payload.ExpiresAt {
		return nil, fmt.Errorf("token expired at %s", time.Unix(payload.ExpiresAt, 0).UTC().Format(time.RFC3339))
	}

	return &payload, nil
}

func init() {
	inviteCmd.Flags().Duration("ttl", 2*time.Hour, "token time-to-live")
	inviteCmd.Flags().String("role", "member", "role to grant: member or agent")
	rootCmd.AddCommand(inviteCmd)
}

// probe — publish a signed rd card event to a relay with either the ALLOWLISTED
// portfolio key or a fresh RANDOM key, and report whether the relay accepted it.
// Ground-source proof for the ready-266 write-allowlist: allowlisted -> accepted,
// random -> rejected, against the LIVE locked relays. No mocks.
//
// Usage: probe <relay-ws-url> <allowlisted|random> [portfolio-key-path]
//
// Exit code: 0 if the relay ACCEPTED the write, 1 if it REJECTED (or errored).
// The demo script asserts the expected code per mode.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/campfire-net/ready/pkg/nostr"
	"github.com/campfire-net/ready/pkg/rdconfig"
	"github.com/campfire-net/ready/pkg/state"
	rdsync "github.com/campfire-net/ready/pkg/sync"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: probe <relay-url> <allowlisted|random> [key-path]")
		os.Exit(2)
	}
	relay := os.Args[1]
	mode := os.Args[2]

	var k *nostr.Key
	var err error
	switch mode {
	case "allowlisted":
		keyPath := nostr.DefaultKeyPath(os.Getenv("HOME") + "/.cf")
		if len(os.Args) >= 4 {
			keyPath = os.Args[3]
		}
		k, err = nostr.LoadKeyFile(keyPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load portfolio key %s: %v\n", keyPath, err)
			os.Exit(2)
		}
	case "random":
		k, err = nostr.GenerateKey()
		if err != nil {
			fmt.Fprintf(os.Stderr, "gen random key: %v\n", err)
			os.Exit(2)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q (want allowlisted|random)\n", mode)
		os.Exit(2)
	}

	_ = rdconfig.Config{} // endpoints are passed in explicitly by the demo
	itemID := fmt.Sprintf("ready-266-probe-%s-%d", mode, time.Now().UnixNano())
	card := rdsync.CardSpec{ItemID: itemID, Title: "266 write-allowlist probe", Status: state.StatusActive, Priority: "p3", Type: "task", BoardD: "ready"}
	ev, err := rdsync.BuildCardEvent(k, card, time.Now().Unix())
	if err != nil {
		fmt.Fprintf(os.Stderr, "build event: %v\n", err)
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), nostr.DefaultTimeout)
	defer cancel()
	accepted, msg, err := nostr.Publish(ctx, relay, ev)
	if err != nil {
		fmt.Fprintf(os.Stderr, "publish error to %s: %v\n", relay, err)
		os.Exit(1)
	}
	fmt.Printf("relay=%s mode=%s pubkey=%s accepted=%v msg=%q\n", relay, mode, k.PubKeyHex(), accepted, msg)
	if accepted {
		os.Exit(0)
	}
	os.Exit(1)
}

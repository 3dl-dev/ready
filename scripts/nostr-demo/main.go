// Command nostr-demo is the Go side of the ready-41d ground-source proof. It is
// a thin CLI over pkg/nostr so scripts/nostr-demo.sh can drive the REAL Go
// signer/publisher against LIVE relays and cross-check the id/sig against nak.
//
// Subcommands:
//
//	relays                      print write-relay URLs from pkg/rdconfig (one/line)
//	sign  --sec --ts --content  build+sign an event, print event JSON to stdout
//	                            (deterministic — for byte-exact nak cross-check)
//	prove --relay --content     build+sign a fresh-key event, publish to the relay
//	                            (expect OK,true), fetch it back, Verify (ACCEPT),
//	                            then tamper a byte and Verify (REJECT)
//
// This is a demo/proof tool, not shipped rd functionality.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/campfire-net/ready/pkg/nostr"
	"github.com/campfire-net/ready/pkg/rdconfig"
)

func main() {
	if len(os.Args) < 2 {
		fatalf("usage: nostr-demo <relays|sign|prove> [flags]")
	}
	switch os.Args[1] {
	case "relays":
		cmdRelays()
	case "sign":
		cmdSign(os.Args[2:])
	case "prove":
		cmdProve(os.Args[2:])
	default:
		fatalf("unknown subcommand %q", os.Args[1])
	}
}

func cmdRelays() {
	var cfg rdconfig.Config
	for _, u := range cfg.WriteRelayURLs() {
		fmt.Println(u)
	}
}

// parseTags parses "k=v" pairs into [][]string{{k,v},...}.
func parseTags(specs []string) [][]string {
	tags := [][]string{}
	for _, s := range specs {
		if s == "" {
			continue
		}
		kv := strings.SplitN(s, "=", 2)
		if len(kv) != 2 {
			fatalf("bad --tag %q (want k=v)", s)
		}
		tags = append(tags, []string{kv[0], kv[1]})
	}
	return tags
}

type tagFlag []string

func (t *tagFlag) String() string     { return strings.Join(*t, ",") }
func (t *tagFlag) Set(v string) error { *t = append(*t, v); return nil }

func cmdSign(args []string) {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	sec := fs.String("sec", "", "32-byte hex secret key (required)")
	ts := fs.Int64("ts", 0, "created_at unix timestamp (required)")
	content := fs.String("content", "", "event content")
	kind := fs.Int("kind", 1, "event kind")
	var tags tagFlag
	fs.Var(&tags, "tag", "tag as k=v (repeatable)")
	_ = fs.Parse(args)

	if *sec == "" || *ts == 0 {
		fatalf("sign requires --sec and --ts")
	}
	k, err := nostr.KeyFromHex(*sec)
	if err != nil {
		fatalf("key: %v", err)
	}
	e := &nostr.Event{CreatedAt: *ts, Kind: *kind, Tags: parseTags(tags), Content: *content}
	if err := e.Sign(k); err != nil {
		fatalf("sign: %v", err)
	}
	out, _ := json.Marshal(e)
	fmt.Println(string(out))
}

func cmdProve(args []string) {
	fs := flag.NewFlagSet("prove", flag.ExitOnError)
	relay := fs.String("relay", "", "relay ws:// URL (required)")
	content := fs.String("content", "", "event content")
	_ = fs.Parse(args)
	if *relay == "" {
		fatalf("prove requires --relay")
	}

	k, err := nostr.GenerateKey()
	if err != nil {
		fatalf("genkey: %v", err)
	}
	c := *content
	if c == "" {
		c = fmt.Sprintf("ready-41d go-publisher proof %d <>&\"", time.Now().UnixNano())
	}
	e := &nostr.Event{
		CreatedAt: time.Now().Unix(),
		Kind:      1,
		Tags:      [][]string{{"t", "rd-nostr-proof"}},
		Content:   c,
	}
	if err := e.Sign(k); err != nil {
		fatalf("sign: %v", err)
	}
	fmt.Printf("EVENT_ID %s\n", e.ID)
	fmt.Printf("PUBKEY   %s\n", e.PubKey)

	ctx, cancel := context.WithTimeout(context.Background(), nostr.DefaultTimeout)
	defer cancel()
	accepted, msg, err := nostr.Publish(ctx, *relay, e)
	if err != nil {
		fatalf("publish: %v", err)
	}
	if !accepted {
		fatalf("relay REJECTED valid event: %q", msg)
	}
	fmt.Printf("RELAY_OK true (%s) msg=%q\n", *relay, msg)

	fctx, fcancel := context.WithTimeout(context.Background(), nostr.DefaultTimeout)
	defer fcancel()
	got, err := nostr.Fetch(fctx, *relay, e.ID)
	if err != nil {
		fatalf("fetch: %v", err)
	}
	if err := got.Verify(); err != nil {
		fatalf("Verify REJECTED relay-served event: %v", err)
	}
	fmt.Println("VERIFY_ACCEPT ok (independent re-derive id + schnorr verify on relay-served event)")

	got.Content += "X"
	if err := got.Verify(); err == nil {
		fatalf("Verify ACCEPTED a tampered event — tamper gate broken")
	}
	fmt.Println("VERIFY_REJECT ok (tampered byte rejected)")
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "nostr-demo: "+format+"\n", a...)
	os.Exit(1)
}

// Command gen writes testdata/fold.vectors.json.
//
//	go run ./internal/foldvectors/gen -out testdata/fold.vectors.json
//
// It builds every vector, CHECKS each hand-authored expectation against rd's
// live fold, and refuses to write anything if one disagrees. Regeneration is not
// a no-op: schnorr signatures and the confidential envelope's AEAD nonces are
// freshly randomized, so the output differs byte-for-byte from the committed
// file even when no behaviour changed. Review a regeneration; never assume it.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/3dl-dev/ready/internal/foldvectors"
)

func main() {
	out := flag.String("out", "testdata/fold.vectors.json", "output path")
	flag.Parse()

	f, err := foldvectors.Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "build vectors: %v\n", err)
		os.Exit(1)
	}
	blob, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal: %v\n", err)
		os.Exit(1)
	}
	blob = append(blob, '\n')
	if err := os.WriteFile(*out, blob, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", *out, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d vectors)\n", *out, len(f.Vectors))
}

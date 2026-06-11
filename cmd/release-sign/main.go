package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/anex/wg-monitor/internal/releasesig"
)

func main() {
	in := flag.String("in", "", "checksums.txt path")
	out := flag.String("out", "", "signature output path")
	flag.Parse()
	if *in == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: release-sign -in dist/checksums.txt -out dist/checksums.txt.sig")
		os.Exit(2)
	}
	seed := os.Getenv("WG_MONITOR_RELEASE_SIGNING_SEED_B64")
	if seed == "" {
		fmt.Fprintln(os.Stderr, "WG_MONITOR_RELEASE_SIGNING_SEED_B64 is required")
		os.Exit(2)
	}
	body, err := os.ReadFile(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sig, err := releasesig.SignChecksumsWithSeed(body, seed)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, sig, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

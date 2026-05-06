package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/keenetic"
)

func cmdShowDiscoveredDNS(args []string) {
	fs := flag.NewFlagSet("show-discovered-dns", flag.ExitOnError)
	awgmgrURL := fs.String("awg-manager-url", "http://127.0.0.1:2222", "awg-manager API base URL (for NDMSName→linux iface map)")
	ndmcBin := fs.String("ndmc", "/bin/ndmc", "path to /bin/ndmc")
	_ = fs.Parse(args)

	runner := osRunner{}
	ndmc := keenetic.NDMC{Runner: runner, BinaryPath: *ndmcBin}
	rc, err := ndmc.Show(context.Background(), "running-config")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ndmc show running-config: %v\n", err)
		os.Exit(1)
	}
	eps := keenetic.ParseDNSEndpoints(rc)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ifaceMap, mapErr := keenetic.FetchIfaceMap(ctx, keenetic.IfaceMapOptions{AwgManagerURL: *awgmgrURL})
	if mapErr != nil {
		fmt.Fprintf(os.Stderr, "warning: fetch iface map: %v\n", mapErr)
	}

	fmt.Printf("Discovered %d DNS endpoint(s):\n", len(eps))
	for i, ep := range eps {
		fmt.Printf("  [%d] type=%s ", i+1, ep.Type)
		switch ep.Type {
		case "plain", "dot":
			fmt.Printf("target=%s:%d", ep.Host, ep.Port)
			if ep.NDMSName != "" {
				linux := ifaceMap[ep.NDMSName]
				if linux == "" {
					linux = "(unmapped)"
				}
				fmt.Printf(" via NDMS=%s linux=%s", ep.NDMSName, linux)
			}
		case "doh":
			fmt.Printf("url=%s", ep.URL)
		}
		fmt.Println()
	}
}

// osRunner — minimal exec.Cmd-based CmdRunner for the CLI.
type osRunner struct{}

func (osRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	return string(out), err
}

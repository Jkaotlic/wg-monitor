package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	versionFlag := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Println(Version)
		return
	}

	args := flag.Args()
	if len(args) == 0 {
		fmt.Println("wg-monitor-deploy", Version)
		fmt.Println("(меню пока не реализовано — Task 12)")
		return
	}

	fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[0])
	os.Exit(2)
}

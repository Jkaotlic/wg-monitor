package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
)

type awgmURLPatchOptions struct {
	URL              string
	APIKey           string
	Login            string
	Password         string
	TerminalUser     string
	TerminalPassword string
	Nickname         string
	OldURL           string
	NewURL           string
}

func actionAWGMURLPatch(args []string) error {
	opts, err := parseAWGMURLPatchArgs(args)
	if err != nil {
		return err
	}
	awgm := NewAWGMClient(opts.URL, opts.Login, opts.Password).WithAPIKey(opts.APIKey)
	if _, err := awgm.SystemInfo(context.Background()); err != nil {
		return fmt.Errorf("system-info: %w", err)
	}
	script := renderAWGMURLPatchScript(opts.Nickname, opts.OldURL, opts.NewURL)
	res, err := runAWGMBootstrapDirect(awgm, script, opts.TerminalUser, opts.TerminalPassword)
	if err != nil {
		return err
	}
	if strings.TrimSpace(res.Output) != "" {
		fmt.Println(strings.TrimSpace(res.Output))
	}
	return nil
}

func parseAWGMURLPatchArgs(args []string) (awgmURLPatchOptions, error) {
	fs := flag.NewFlagSet("awgm-url-patch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	opts := awgmURLPatchOptions{}
	fs.StringVar(&opts.URL, "url", "", "AWG Manager base URL")
	fs.StringVar(&opts.APIKey, "api-key", "", "AWG Manager API key")
	fs.StringVar(&opts.Login, "login", "", "AWG Manager login")
	fs.StringVar(&opts.Password, "password", "", "AWG Manager password")
	fs.StringVar(&opts.TerminalUser, "terminal-user", "root", "terminal login user")
	fs.StringVar(&opts.TerminalPassword, "terminal-password", "", "terminal login password")
	fs.StringVar(&opts.Nickname, "nickname", "", "expected agent nickname")
	fs.StringVar(&opts.OldURL, "old", "https://wgmonitor.example.com", "old backend URL")
	fs.StringVar(&opts.NewURL, "new", "https://wgmonitor.example.com", "new backend URL")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if opts.Password == "" {
		opts.Password = os.Getenv("AWGM_PATCH_PASSWORD")
	}
	if opts.TerminalPassword == "" {
		opts.TerminalPassword = os.Getenv("AWGM_PATCH_TERMINAL_PASSWORD")
	}
	if strings.TrimSpace(opts.URL) == "" || strings.TrimSpace(opts.Nickname) == "" {
		return opts, fmt.Errorf("url and nickname are required")
	}
	if strings.TrimSpace(opts.APIKey) == "" && (strings.TrimSpace(opts.Login) == "" || strings.TrimSpace(opts.Password) == "") {
		return opts, fmt.Errorf("api-key or login/password are required")
	}
	if strings.TrimSpace(opts.TerminalPassword) == "" {
		return opts, fmt.Errorf("terminal password is required")
	}
	return opts, nil
}

func renderAWGMURLPatchScript(nickname, oldURL, newURL string) string {
	return "#!/bin/sh\nset -eu\n" +
		"CFG=/opt/etc/wg-monitor/config.yaml\n" +
		"INIT=/opt/etc/init.d/S99wg-monitor\n" +
		"OLD=" + shellQuote(strings.TrimRight(strings.TrimSpace(oldURL), "/")) + "\n" +
		"NEW=" + shellQuote(strings.TrimRight(strings.TrimSpace(newURL), "/")) + "\n" +
		"test -f \"$CFG\"\n" +
		"if grep -Fq \"$OLD\" \"$CFG\"; then\n" +
		"  sed -i \"s|$OLD|$NEW|g\" \"$CFG\"\n" +
		"fi\n" +
		"if ! grep -Fq \"$NEW\" \"$CFG\"; then\n" +
		"  echo \"backend url not patched in $CFG\"\n" +
		"  exit 2\n" +
		"fi\n" +
		awgmServiceStartBlock("wg-monitor url patched for "+shellQuote(nickname)) + "\n"
}

//go:build windows
// +build windows

package main

import "golang.org/x/term"

func readPasswordNoEcho(fd int) ([]byte, error) {
	return term.ReadPassword(fd)
}

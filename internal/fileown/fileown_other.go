//go:build !unix

package fileown

import "os"

// MatchDir вне unix ничего не делает: владельцев как на Pi там нет.
func MatchDir(_ *os.File, _ string) error { return nil }

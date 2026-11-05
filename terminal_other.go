//go:build !darwin && !linux

package main

import "os"

// Other platforms can opt into ANSI colors with SITEWATCH_COLOR=always.
func isTerminal(file *os.File) bool {
	return false
}

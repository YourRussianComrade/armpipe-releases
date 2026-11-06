//go:build !windows

package main

import "os"

func setupConsole(title string) bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

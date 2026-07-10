//go:build !darwin

package main

import "fmt"

func runImeCommand(args []string) error {
	return fmt.Errorf("agent ime is only supported on macOS")
}

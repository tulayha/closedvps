package main

import (
	"os"
	"os/exec"
	"strconv"
)

// keepAwake stops the Mac from sleeping on idle while closedvps waits for stock.
func keepAwake() func() {
	cmd := exec.Command("caffeinate", "-i", "-w", strconv.Itoa(os.Getpid()))
	if cmd.Start() != nil {
		return func() {}
	}
	return func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }
}

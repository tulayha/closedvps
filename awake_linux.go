package main

import (
	"os"
	"os/exec"
	"strconv"
)

// keepAwake holds a systemd sleep inhibitor while closedvps waits for stock, where systemd is present.
func keepAwake() func() {
	cmd := exec.Command("systemd-inhibit", "--what=idle:sleep", "--who=closedvps", "--why=Waiting to buy a server", "--mode=block",
		"tail", "--pid="+strconv.Itoa(os.Getpid()), "-f", "/dev/null") // ends with closedvps even if it is killed
	if cmd.Start() != nil {
		return func() {}
	}
	return func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }
}

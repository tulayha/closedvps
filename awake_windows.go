package main

import "golang.org/x/sys/windows"

const (
	esContinuous     = 0x80000000
	esSystemRequired = 0x00000001
)

var setThreadExecutionState = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadExecutionState")

// keepAwake stops Windows from sleeping on idle while closedvps waits for stock.
func keepAwake() func() {
	_, _, _ = setThreadExecutionState.Call(esContinuous | esSystemRequired)
	return func() { _, _, _ = setThreadExecutionState.Call(esContinuous) }
}

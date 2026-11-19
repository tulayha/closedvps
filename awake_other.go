//go:build !darwin && !linux && !windows

package main

func keepAwake() func() { return func() {} }

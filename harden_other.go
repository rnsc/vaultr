//go:build !linux && !darwin

package main

// hardenOS does nothing on this system.
func hardenOS() {}

package main

import "golang.org/x/sys/unix"

// hardenOS stops debuggers from attaching (PT_DENY_ATTACH); root can get
// around it, other processes of the user can't.
func hardenOS() {
	_ = unix.PtraceDenyAttach()
}

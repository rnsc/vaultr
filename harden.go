package main

import "os"

// harden makes it harder for other processes to read vaultr's memory,
// which holds the token and, in a long TUI session, secret values that
// were opened. It is best effort and silent: nothing changes for the
// user. VAULTR_ALLOW_DEBUG=1 skips it, to attach a debugger to vaultr.
//
// This narrows exposure; it isn't a boundary against malware running as
// the same user, which could use the token itself.
func harden() {
	if os.Getenv("VAULTR_ALLOW_DEBUG") == "1" {
		return
	}
	hardenOS()
}

package main

import "golang.org/x/sys/unix"

// hardenOS marks the process not dumpable: no core dumps, and other
// non-root processes of the same user can no longer ptrace it or read
// /proc/<pid>/mem, environ and the like, whatever the ptrace_scope
// setting says.
func hardenOS() {
	_ = unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0)
}

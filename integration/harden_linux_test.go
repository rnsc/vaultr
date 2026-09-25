//go:build integration && linux

package integration

import (
	"os"
	"strconv"
	"testing"
	"time"
)

// Another process of the same user can't read a running vaultr's memory or
// environment (which holds the token); with VAULTR_ALLOW_DEBUG=1 it can.
func TestOtherProcessesCannotReadMemory(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root reads any process's memory; run as a normal user (CI does)")
	}
	for _, allow := range []string{"", "1"} {
		c := newCLI(t, rootChild(t, time.Hour))
		c.env["VAULTR_ALLOW_DEBUG"] = allow
		tm := startTUI(t, c)
		tm.waitFor(0, "indexed ")
		proc := "/proc/" + strconv.Itoa(tm.cmd.Process.Pid)
		_, envErr := os.ReadFile(proc + "/environ")
		_, memErr := os.Open(proc + "/mem")
		if allow == "" && (envErr == nil || memErr == nil) {
			t.Errorf("hardened: environ err %v, mem err %v", envErr, memErr)
		}
		if allow == "1" && envErr != nil {
			t.Errorf("VAULTR_ALLOW_DEBUG=1: %v", envErr)
		}
		tm.send(keyCtrlC)
		tm.waitExit()
	}
}

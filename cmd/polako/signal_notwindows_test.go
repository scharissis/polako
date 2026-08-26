//go:build !windows

package main

// Not on Windows, where SIGTERM cannot be delivered at all. The constant
// compiles there and is simply never raised, which is the whole reason naming
// it in shutdownSignals costs the cross-compile nothing.

import (
	"context"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"testing"
	"time"
)

// The portable test says SIGTERM is in the list; this one says the list is
// wired to something. It matters because cancelling the context is the only
// thing that kills the running claude: exec.CommandContext holds the child, and
// a supervisor killed without cancelling leaves it behind, still able to push a
// branch and open a PR behind a restarted drain's back.
func TestSigtermCancelsTheRun(t *testing.T) {
	// Checked before raising anything. With SIGTERM absent from the list
	// nothing would catch it and the default disposition would take the whole
	// test binary down — a regression should fail by name, not by killing the
	// suite from under the other tests.
	if !slices.Contains(shutdownSignals(), os.Signal(syscall.SIGTERM)) {
		t.Fatal("shutdownSignals() does not carry SIGTERM, so raising it here would " +
			"kill the test process rather than test anything")
	}

	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()

	// Raised at this very process, so nothing here needs a shell, a child or a
	// clock to reach the handler installed above.
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("raising SIGTERM at this process: %v", err)
	}

	select {
	case <-ctx.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("SIGTERM did not cancel the context, so a running claude would outlive the supervisor")
	}
}

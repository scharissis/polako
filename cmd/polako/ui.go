package main

// The work verb narrates to two sinks: the terminal, for an operator glancing
// over, and a per-shift log file that keeps everything. Which lines matter is
// decided at the call site — the default logger carries milestones, `detail`
// carries texture — and how each sink renders a line (timestamps, filtering)
// is decided here, so a call site never has to know how it will be shown.
//
// The shift log is write-only, like the run-data records: nothing in this
// binary ever reads it back, deleting it mid-drain changes no behaviour, and
// it never leaves the machine. Unlike those records it holds transcript text,
// which is why it gets the recorder's private-by-default permissions.

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// stampLayout matches what log.Ldate|log.Ltime produced before the sinks took
// over stamping, so piped and logged output look like they always did.
const stampLayout = "2006/01/02 15:04:05 "

type ui struct {
	// One writer at a time: the two loggers each serialise their own callers
	// but not each other, and the claude stderr copier is a third writer.
	mu       sync.Mutex
	terminal io.Writer
	stamp    bool      // timestamp terminal lines; the shift log is always stamped
	file     io.Writer // the shift log; nil when -log is off or preflight has not opened it yet
	verbose  bool      // -verbose: detail lines reach the terminal too
	warned   bool      // one warning per process: a full disk must not fill the terminal too
}

// sinks is the process's one ui. Package-level for the same reason the stdlib
// logger is: narration comes from everywhere, and threading a handle through
// every signature would dwarf the feature.
var sinks = &ui{terminal: os.Stderr, stamp: true}

// detail is the second narration channel: lines worth keeping but not worth an
// operator's glance. They always reach the shift log and, unless -verbose says
// otherwise, nothing else.
var detail = log.New(detailWriter{u: sinks}, "", 0)

// milestoneWriter is what the default logger writes to on the work path.
type milestoneWriter struct{ u *ui }

func (w milestoneWriter) Write(p []byte) (int, error) {
	w.u.emit(p, true)
	return len(p), nil
}

// detailWriter is what the detail logger writes to.
type detailWriter struct{ u *ui }

func (w detailWriter) Write(p []byte) (int, error) {
	w.u.emit(p, false)
	return len(p), nil
}

// emit renders one record to the sinks. The log package delivers each record
// as a single Write call, so stamping per call never splits a line.
func (u *ui) emit(p []byte, milestone bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := time.Now().Format(stampLayout)
	if milestone || u.verbose {
		if u.stamp {
			io.WriteString(u.terminal, now)
		}
		u.terminal.Write(p)
	}
	if u.file == nil {
		return
	}
	if _, err := u.file.Write(append([]byte(now), p...)); err != nil && !u.warned {
		u.warned = true
		// Straight to the terminal rather than through the logger, which would
		// re-enter emit while mu is held.
		if u.stamp {
			io.WriteString(u.terminal, now)
		}
		fmt.Fprintf(u.terminal, "shift log not written (%v) — the shift continues on the terminal alone; "+
			"-log <dir> to move it or -log off to silence this\n", err)
	}
}

// openShiftLog turns the file sink on. Called once preflight learns which
// repository to name the file after; everything logged before that had nowhere
// durable to go, and everything after lands in the file. O_APPEND like the
// recorder: the shift id in the name makes a collision an accident worth
// surviving, not one worth locking against.
func (u *ui) openShiftLog(dir, repo, shiftID string) (string, error) {
	// 0o700/0o600 for the same reason the run-data records get them: this
	// file holds the whole transcript stream of runs on repositories that may
	// be private, and a default umask would share it.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, repoSlug(repo)+"--"+shiftID+".log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	u.mu.Lock()
	u.file = f
	u.mu.Unlock()
	return path, nil
}

// resolveLogDir resolves the -log flag: a directory, or "off". The default
// lives beside the run-data directory, deliberately outside any checkout —
// the skill commits things there, and a transcript must not become
// committable by accident.
func resolveLogDir(spec string) string {
	dir := strings.TrimSpace(spec)
	if strings.EqualFold(dir, metricsOff) {
		return ""
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Printf("no home directory for the shift log (%v) — continuing without one; "+
				"pass -log <dir> to choose a location, or -log off to stop asking", err)
			return ""
		}
		dir = filepath.Join(home, ".polako", "logs")
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return dir
}

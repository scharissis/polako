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
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// stampLayout matches what log.Ldate|log.Ltime produced before the sinks took
// over stamping, so piped and logged output look like they always did.
const stampLayout = "2006/01/02 15:04:05 "

// ttyStampLayout is what a TTY gets instead: a quiet gutter that still places
// a line in time relative to its neighbours, deliberately shorter than the
// shift log's stamp regardless of whether a shift log is even open this run —
// see stampKind.
const ttyStampLayout = "15:04:05 "

// stampKind controls how — or whether — the terminal sink stamps a line. The
// shift log ignores it entirely: a file write always gets the full
// stampLayout, unconditionally.
type stampKind int

const (
	stampFull   stampKind = iota // the full stampLayout, matching a pipe or redirect exactly — the default until a verb says otherwise
	stampOff                     // no terminal stamp at all — status/stats never open a shift log, so piped output there never had one
	stampTTYDim                  // dim, time-only gutter — work on a capable TTY
)

type ui struct {
	// One writer at a time: the two loggers each serialise their own callers
	// but not each other, and the claude stderr copier is a third writer.
	mu       sync.Mutex
	terminal io.Writer
	stamp    stampKind // how the terminal sink stamps a line
	style    styler    // ANSI on a capable TTY; the zero value renders plain
	file     io.Writer // the shift log; nil when -log is off or preflight has not opened it yet
	verbose  bool      // -verbose: detail lines reach the terminal too
	warned   bool      // one warning per process: a full disk must not fill the terminal too
	// lastTerm is when a line last reached the terminal sink — a milestone, or
	// under -verbose any line at all. The heartbeat (see dispatchClaude)
	// samples it to tell a quiet terminal from a quiet run; nothing else reads
	// it, and nothing branches on it. Atomic so that reader never has to take
	// mu, which emit holds while it writes.
	lastTerm atomic.Int64 // UnixNano, 0 until the first terminal write
}

// sinks is the process's one ui. Package-level for the same reason the stdlib
// logger is: narration comes from everywhere, and threading a handle through
// every signature would dwarf the feature. stampFull is stampKind's zero
// value, so it needs no explicit field here.
var sinks = &ui{terminal: os.Stderr}

// detail is the second narration channel: lines worth keeping but not worth an
// operator's glance. They always reach the shift log and, unless -verbose says
// otherwise, nothing else.
var detail = log.New(detailWriter{u: sinks}, "", 0)

// milestoneWriter is what the default logger writes to on the work path.
type milestoneWriter struct{ u *ui }

func (w milestoneWriter) Write(p []byte) (int, error) {
	w.u.emit(p, true, sevProgress)
	return len(p), nil
}

// detailWriter is what the detail logger writes to.
type detailWriter struct{ u *ui }

func (w detailWriter) Write(p []byte) (int, error) {
	w.u.emit(p, false, sevProgress)
	return len(p), nil
}

// severity is what a call site declares about a milestone line, so render
// can choose its colour without re-deriving meaning from the wording the way
// the rule table it replaces did. sevProgress, the zero value, is what an
// unmigrated log.Printf gets implicitly — the stdlib log package has no way
// to carry anything richer per call — and it renders exactly as an
// unclassified line always has: plain. sevSection is not one of the
// semantic severities a caller "feels" (success/warning/error/settings); it
// marks the shift's own two structural headings, which need a colour but no
// sentiment: the issue's acceptance criteria pin the other five colours to
// named severities but also require "bold for section marks," and no
// combination of those five leaves a slot for bold without turning some
// unrelated severity bold too — so it gets a sixth, purely structural value
// instead.
type severity int

const (
	sevProgress severity = iota
	sevSuccess
	sevWarning
	sevError
	sevSettings
	sevSection
)

// activeUI is the *ui the narration loggers currently point at: the real
// sinks in production, a test's own ui while wireSinks is in effect. It is
// the same resolution narrate does to pick its target — the heartbeat uses
// it to read lastTerm off whichever ui its own narrate calls will write to.
func activeUI() *ui {
	if mw, ok := log.Writer().(milestoneWriter); ok {
		return mw.u
	}
	return sinks
}

// narrate emits one milestone line at the severity its caller declares. It
// resolves its target the same way the default logger's Write does — through
// whatever log.SetOutput currently points at — so a test that redirects the
// default logger redirects severity-aware narration too, with no separate
// wiring to keep in sync.
func narrate(sev severity, format string, args ...any) {
	mw, ok := log.Writer().(milestoneWriter)
	if !ok {
		// The default logger points somewhere that isn't a *ui — a test
		// redirected it directly (log.SetOutput(io.Discard), a strings.Builder)
		// expecting plain log.Printf semantics. Matching that, rather than
		// falling back to the real sinks, is what keeps the promise above.
		log.Printf(format, args...)
		return
	}
	s := fmt.Sprintf(format, args...)
	if len(s) == 0 || s[len(s)-1] != '\n' {
		s += "\n"
	}
	mw.u.emit([]byte(s), true, sev)
}

// fatal narrates at error severity, then ends the process the same way
// log.Fatalf always did.
func fatal(format string, args ...any) {
	narrate(sevError, format, args...)
	os.Exit(1)
}

// emit renders one record to the sinks. The log package delivers each record
// as a single Write call, so stamping per call never splits a line.
func (u *ui) emit(p []byte, milestone bool, sev severity) {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := time.Now()
	if milestone || u.verbose {
		if s := u.termStamp(now); s != "" {
			io.WriteString(u.terminal, s)
		}
		io.WriteString(u.terminal, u.style.render(string(p), milestone, sev))
		// The heartbeat keys off this: any line the terminal was actually
		// given resets its clock, which is also why a -verbose run — where
		// every line lands here — never sees a heartbeat.
		u.lastTerm.Store(now.UnixNano())
	}
	if u.file == nil {
		return
	}
	if _, err := u.file.Write(append([]byte(now.Format(stampLayout)), p...)); err != nil && !u.warned {
		u.warned = true
		// Straight to the terminal rather than through the logger, which would
		// re-enter emit while mu is held — but still through render, so this
		// warning gets the same yellow the styler promises it.
		if s := u.termStamp(now); s != "" {
			io.WriteString(u.terminal, s)
		}
		io.WriteString(u.terminal, u.style.render(fmt.Sprintf(logLostFmt+"\n", err), true, sevWarning))
	}
}

// termStamp renders the stamp for the terminal sink alone, "" for stampOff.
// stampTTYDim is time-only and dim, styled by the same styler that gates
// every other colour, so NO_COLOR, TERM=dumb and Windows get it unstyled
// rather than dropped; stampFull is the same full stampLayout a pipe or
// redirect has always seen.
func (u *ui) termStamp(now time.Time) string {
	switch u.stamp {
	case stampOff:
		return ""
	case stampTTYDim:
		return u.style.dim(now.Format(ttyStampLayout))
	default:
		return now.Format(stampLayout)
	}
}

// logLostFmt is said at both moments a shift log can fail — opening it and
// writing to it — one spelling so the two cannot drift apart, and so the
// styler's rule for it matches both. It is honest about the stakes: the quiet
// terminal carries milestones alone, so a lost log is a lost stream, not a
// duplicate.
const logLostFmt = "shift log not written (%v) — the shift continues, but the full claude stream " +
	"is lost unless -verbose mirrors it here; -log <dir> moves the log, -log off silences this"

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

// styler is the terminal's one concession to presentation: whole lines get a
// colour when their content earns one, and nothing gets coloured anywhere but
// a capable TTY. The zero value is off, which is what every test and every
// pipe sees.
type styler struct{ on bool }

// isTerminal reports whether f is a character device — the portable half of
// the decision; whether ANSI is worth emitting there is ansiCapable's half.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// styleFor decides whether a terminal gets ANSI colour: only on a TTY, on a
// platform that renders ANSI (see ui_windows.go), with NO_COLOR unset —
// presence disables, even set to nothing, per no-color.org — and TERM not
// declaring itself "dumb".
func styleFor(tty bool) styler {
	_, noColor := os.LookupEnv("NO_COLOR")
	return styler{on: tty && ansiCapable && !noColor && os.Getenv("TERM") != "dumb"}
}

func (s styler) wrap(code, line string) string {
	if !s.on {
		return line
	}
	return code + line + "\x1b[0m"
}

// dim is the styler's one low-emphasis code, shared by every caller that
// wants "present, but not what the eye should land on first" — detail
// lines, the settings recap, a report's column headers, and the terminal's
// own stamp — so the four cannot drift to different codes independently.
func (s styler) dim(text string) string {
	return s.wrap("\x1b[2m", text)
}

// render styles one terminal record. Detail lines (seen only under -verbose)
// are dimmed as a block so the milestones keep standing out; a milestone's
// colour comes from the severity its call site declared, never from the
// wording — a caller that forgets to declare one renders plain, never
// wrong, and every future rewording keeps its colour for free.
func (s styler) render(line string, milestone bool, sev severity) string {
	if !s.on {
		return line
	}
	text, nl, _ := strings.Cut(line, "\n")
	if !milestone {
		return s.dim(text) + "\n" + nl
	}
	switch sev {
	case sevSection:
		text = s.wrap("\x1b[1m", text) // bold: the shift's section marks
	case sevError:
		text = s.wrap("\x1b[31m", text) // red: the shift lost something here
	case sevSuccess:
		text = s.wrap("\x1b[32m", text) // green: forward progress
	case sevWarning:
		text = s.wrap("\x1b[33m", text) // yellow: needs an eye, not a stop
	case sevSettings:
		text = s.dim(text) // dim: the startup preference recap
	}
	return text + "\n" + nl
}

// report renders `status` and `stats`: printPairs and printTable share it,
// styled sparingly in work's own palette. TTY-detected on stdout — printing to
// a terminal and piping are different acts, and pipe output must stay exactly
// what it always was. The zero value renders plain, which is what every
// existing test and every pipe sees.
type report struct {
	style styler
}

func newReport(tty bool) report {
	return report{style: styleFor(tty)}
}

// bold marks a section head: a printTable title, and the two report lines
// that aren't printPairs/printTable output at all — the status repo line and
// stats' "run data from …" line — wrapped directly at their call sites.
func (r report) bold(s string) string { return r.style.wrap("\x1b[1m", s) }

// dim marks a printTable header row, the same code narration uses for detail
// lines: present, but not what the eye should land on first.
func (r report) dim(s string) string { return r.style.dim(s) }

// attentionMarkers are the cell contents worth an eye: a failing check, a
// review still blocking, a park, an unreviewed proposal, or a state nobody
// looked up. Substring match because a cell can carry more than the marker
// alone — checksCell appends the failing checks' names, for one. "proposed"
// belongs beside "parked": needsYou treats curating one and deciding the
// other with the same urgency, so the report should too.
var attentionMarkers = []string{"failing", "changes requested", "parked", "proposed", unknownCell}

// cell styles one printPairs/printTable cell — key or value alike, so a
// "parked" row label gets the same yellow a "not read" value cell does.
// Centralised here rather than decided per call site, which is what lets
// the stats and status renderers pass plain strings and never think about colour.
func (r report) cell(s string) string {
	for _, m := range attentionMarkers {
		if strings.Contains(s, m) {
			return r.style.wrap("\x1b[33m", s)
		}
	}
	return s
}

// lineWriter carries a child process's stderr into the narration stream one
// line at a time, so it lands in the shift log stamped and prefixed instead of
// tearing raw and unattributed across whatever else is printing. Written from
// os/exec's copier goroutine alone; the detail logger and the sinks do their
// own locking downstream.
type lineWriter struct {
	prefix string
	buf    []byte
}

// maxStderrLine bounds what lineWriter holds while waiting for a newline. A
// child painting \r progress into the pipe never sends one, and — as the
// neighbouring tailWriter's cap already records — a run that logs for hours
// must not be held in memory. A chunk that long is emitted as a line of its
// own instead of waiting.
const maxStderrLine = 64 * 1024

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			if len(w.buf) > maxStderrLine {
				w.emit(w.buf)
				w.buf = w.buf[:0]
			}
			return len(p), nil
		}
		w.emit(w.buf[:i])
		w.buf = w.buf[i+1:]
	}
}

// flush renders whatever a child left unterminated. Call it after cmd.Wait,
// when the copier is done writing.
func (w *lineWriter) flush() {
	w.emit(w.buf)
	w.buf = nil
}

func (w *lineWriter) emit(line []byte) {
	// A CRLF child's \r would otherwise land in the shift log on every line.
	line = bytes.TrimSuffix(line, []byte("\r"))
	// Blank lines carry nothing worth a timestamp and a prefix.
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	detail.Printf("%s %s", w.prefix, line)
}

// resolveLogDir resolves the -log flag: a directory, or "off". The default
// lives beside the run-data directory, deliberately outside any checkout —
// the skill commits things there, and a transcript must not become
// committable by accident.
func resolveLogDir(spec string) string {
	return resolveDataDir(spec, "logs", "log", "for the shift log")
}

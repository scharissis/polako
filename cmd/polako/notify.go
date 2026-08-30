package main

// Notification hooks: -notify runs a command of the operator's choosing every
// time the drain reaches a state only a person can move past.
//
// It exists because the states worth knowing about are the quiet ones. A drain
// left running overnight goes on working the queue when an issue parks or stops
// to ask a question, so the only trace is a label on a thread nobody is
// watching — and the operator finds out in the morning, hours after they could
// have answered in a minute.
//
// What it deliberately is not is a notifier. Email, Slack, a desktop toast and
// a webhook are all policy, they all need credentials, and none of them belongs
// in a stdlib-only supervisor. The command is the operator's; the context it
// needs arrives in the environment.

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// The states that need a human. Three of them end an issue's turn, one ends
// the drain, and one is a `polako plan` run finishing with a backlog to
// curate; nothing else fires, because a hook that goes off on every ordinary
// event is one an operator mutes.
const (
	notifyParked = "parked"
	// Spelled like the label, because that is what the operator will see on the
	// thread when they go and look.
	notifyAwaiting = "awaiting-answer"
	notifyCleared  = "cleared"
	// notifyStopped is the drain ending before the backlog did — a fatal error,
	// or a session budget spent. Not on the issue's list, but it is the 2am
	// case the flag exists for: a refused token stops everything, and without
	// this nothing says so until somebody looks at the terminal.
	notifyStopped = "stopped"
	// notifyProposed fires once, at the end of a `polako plan` run that left
	// proposals behind: the tool did the right thing — a curated backlog sits
	// behind the `proposed` label — and on a repo nobody is watching that is
	// the only trace. A plan run that proposed nothing fires nothing. Not on
	// an issue's list because it is not a drain event at all; it is here
	// because it is the same class of quiet moment the flag exists for.
	notifyProposed = "proposed"
	// notifyEpicDone fires once, the same occasion commentFinishedContainers
	// earns its own log line: every child of a container closed, waiting only
	// on a human to judge whether they added up to the design and close it.
	// The one event above announcing something good rather than something
	// stuck — an epic finishing is still a state only a person can move past.
	notifyEpicDone = "epic-done"
)

// notifyPrefix namespaces the variables a notify command receives.
//
// The NOTIFY_ infix is load-bearing rather than decorative. POLAKO_<FLAG>
// already sets flag defaults for both the drain and `stats`, so a plain
// POLAKO_REPO here would silently filter a `-notify "polako stats"`
// to one repository — the flag setting a flag it was never meant to touch. A
// test holds every variable below clear of every flag's.
const notifyPrefix = envPrefix + "NOTIFY_"

// notifyTimeout bounds a hook. A notifier that hangs — a curl to a host that
// stopped answering — would otherwise hang the drain with it, which is a far
// worse failure than the notification being lost.
const notifyTimeout = 30 * time.Second

// notification is one thing worth waking somebody for.
type notification struct {
	event string
	// issue is 0 when it is the drain rather than one issue that needs
	// attention, and the variable is then empty rather than "0".
	issue int
	// reason is this program's own words for what happened, in the terms the
	// log and the exit summary use. Never issue, comment or PR text: the
	// records keep that discipline and a hook that leaves the machine has more
	// reason to, not less.
	reason string
}

// env is the context a hook is handed. Every variable is always set, empty
// where it does not apply, so a script can read them without testing for their
// existence first.
func (n notification) env(cfg config) []string {
	issue := ""
	if n.issue > 0 {
		issue = strconv.Itoa(n.issue)
	}
	return []string{
		notifyPrefix + "EVENT=" + n.event,
		notifyPrefix + "ISSUE=" + issue,
		notifyPrefix + "REPO=" + cfg.repo,
		notifyPrefix + "REASON=" + n.reason,
	}
}

// describe names a notification the way the log refers to it.
func (n notification) describe() string {
	if n.issue > 0 {
		return fmt.Sprintf("%s #%d", n.event, n.issue)
	}
	return n.event
}

// notify runs the -notify command, if there is one. Failure is reported and
// never returned: a hook is a courtesy to the operator, and a drain that ended
// because their notifier was broken would be the flag making things worse.
func notify(ctx context.Context, cfg config, n notification) {
	fields := splitCommand(cfg.notifyCmd)
	if len(fields) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, notifyTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, fields[0], fields[1:]...)
	// Deliberately no Dir: the command is one the operator typed on this
	// command line, so it runs where they started the drain rather than in
	// -dir, and a relative path in it resolves the way exec already resolves it.
	cmd.Env = append(os.Environ(), n.env(cfg)...)
	// Captured rather than inherited, so a chatty notifier cannot interleave
	// itself with the run log — and quoted back only when it failed, where it
	// is usually the whole diagnosis.
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		said := ""
		if s := strings.TrimSpace(out.String()); s != "" {
			said = ": " + clip(s, 160)
		}
		log.Printf("-notify command failed for %s (%v)%s — nobody was told, but the shift continues",
			n.describe(), err, said)
		return
	}
	log.Printf("-notify ran for %s", n.describe())
}

// splitCommand splits a -notify command line into a program and its arguments,
// honouring single and double quotes so a path with a space in it can be
// spelled.
//
// It is not a shell, and does not become one: no expansion, no pipeline, no
// redirection. Everything a hook needs to know arrives in the environment
// instead, which is what makes the command it runs simple enough to split this
// way — and anything more than simple belongs in a script the operator owns and
// can test on its own. The alternative, handing the string to `sh -c` or
// `cmd /c`, would also mean two dialects and one of them quoting differently
// from the other on the platform hardest to check.
func splitCommand(s string) []string {
	var fields []string
	var cur strings.Builder
	quote, started := rune(0), false
	for _, r := range s {
		switch {
		case quote != 0:
			// An unclosed quote runs to the end of the line rather than being an
			// error: this is a convenience for spelling one path, not a parser
			// worth failing a drain over.
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, started = r, true
		case unicode.IsSpace(r):
			if started {
				fields = append(fields, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if started {
		fields = append(fields, cur.String())
	}
	return fields
}

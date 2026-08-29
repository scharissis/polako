package main

// -notify runs a command nobody here wrote, so the tests stand in for one the
// same way the suite stands in for claude and gh: the test binary re-executes
// itself as the notifier and writes down what it was told.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// fakeNotifyEnv points the fake notifier at the file it appends to — one line
// per notification, holding that notification's own variables. The literal
// "fail" instead makes it exit nonzero, which is the other thing an operator's
// command can do.
const fakeNotifyEnv = "POLAKO_FAKE_NOTIFY"

// fakeNotify stands in for an operator's -notify command.
func fakeNotify(dest string) int {
	if dest == "fail" {
		os.Stderr.WriteString("fake notify: the webhook is down\n")
		return 3
	}
	var told []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, notifyPrefix) {
			told = append(told, kv)
		}
	}
	f, err := os.OpenFile(dest, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		os.Stderr.WriteString("fake notify: " + err.Error() + "\n")
		return 1
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(told, " ") + "\n"); err != nil {
		return 1
	}
	return 0
}

// notifyLog turns a config into one that notifies through the fake, and returns
// a reader for what it was told.
func notifyLog(t *testing.T, cfg *config) func() []string {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "notifications")
	t.Setenv(fakeNotifyEnv, dest)
	// Quoted, because a test binary's path is not guaranteed to be free of
	// spaces — which is the case splitCommand exists for.
	cfg.notifyCmd = `"` + fakeCLI(t) + `"`
	return func() []string {
		b, err := os.ReadFile(dest)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			t.Fatalf("reading notifications: %v", err)
		}
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

func TestNotifyHandsTheHookItsContext(t *testing.T) {
	captureLog(t)
	cfg := config{repo: "owner/repo"}
	told := notifyLog(t, &cfg)

	notify(context.Background(), cfg, notification{
		event: notifyParked, issue: 7, reason: "the run produced no PR"})
	notify(context.Background(), cfg, notification{
		event: notifyCleared, reason: "no open issues left to work"})

	got := told()
	if len(got) != 2 {
		t.Fatalf("notifications = %v, want one per call", got)
	}
	for _, want := range []string{
		notifyPrefix + "EVENT=parked",
		notifyPrefix + "ISSUE=7",
		notifyPrefix + "REPO=owner/repo",
		notifyPrefix + "REASON=the run produced no PR",
	} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the hook was not told %q\ngot: %s", want, got[0])
		}
	}
	// A drain-wide event carries an empty issue rather than "0", and every
	// variable is set either way so a script need not test for them.
	if !strings.Contains(got[1], notifyPrefix+"ISSUE= ") &&
		!strings.HasSuffix(got[1], notifyPrefix+"ISSUE=") {
		t.Errorf("drained notification = %s, want an empty %sISSUE", got[1], notifyPrefix)
	}
}

// The flag is a courtesy. A notifier that is broken, slow or missing must cost
// the operator notifications and nothing else.
func TestNotifyFailureNeverBreaksTheRun(t *testing.T) {
	buf := captureLog(t)
	cfg := config{repo: "owner/repo"}
	notifyLog(t, &cfg)
	t.Setenv(fakeNotifyEnv, "fail")

	notify(context.Background(), cfg, notification{event: notifyParked, issue: 3, reason: "no"})

	out := buf.String()
	for _, want := range []string{
		"-notify command failed for parked #3",
		"the webhook is down", // what it said, which is usually the diagnosis
		"the shift continues",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q\ngot:\n%s", want, out)
		}
	}
}

// No -notify is the default, and it must cost nothing: no process, no log line.
func TestNotifyDoesNothingWithoutACommand(t *testing.T) {
	buf := captureLog(t)
	notify(context.Background(), config{repo: "owner/repo"},
		notification{event: notifyCleared})
	notify(context.Background(), config{repo: "owner/repo", notifyCmd: "   "},
		notification{event: notifyCleared})
	if out := buf.String(); out != "" {
		t.Errorf("a drain with no -notify said %q, want silence", out)
	}
}

// The variables a hook receives share a namespace with the ones that set flag
// defaults, and `polako stats` — which takes -repo — is a plausible
// notify command. A collision would have a notification quietly reconfigure the
// process it notifies.
func TestNotifyVariablesNeverShadowAFlag(t *testing.T) {
	flags := declaredFlags(t)
	for _, kv := range (notification{event: "e", issue: 1, reason: "r"}).env(config{repo: "o/r"}) {
		name, _, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(name, notifyPrefix) {
			t.Errorf("%s is outside the %s namespace", name, notifyPrefix)
		}
		for _, f := range flags {
			if name == envVarName(f) {
				t.Errorf("%s is both a notification's variable and the default for -%s", name, f)
			}
		}
	}
}

func TestSplitCommandHonoursQuotes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"notify-send", []string{"notify-send"}},
		{"  notify-send   drained  ", []string{"notify-send", "drained"}},
		{`"/Applications/My Notifier" --terse`, []string{"/Applications/My Notifier", "--terse"}},
		{`notify-send 'Backlog drain' "needs you"`, []string{"notify-send", "Backlog drain", "needs you"}},
		// An empty argument is a real one, and an unclosed quote runs to the end
		// rather than failing a drain over punctuation.
		{`say "" 'unclosed`, []string{"say", "", "unclosed"}},
	} {
		got := splitCommand(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("splitCommand(%q) = %q, want %q", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitCommand(%q) = %q, want %q", tc.in, got, tc.want)
				break
			}
		}
	}
}

// A -notify nobody can run is a night of notifications nobody receives, so it
// is caught at startup rather than at the first one — hours in, on a backlog
// that was going fine.
func TestCheckNotifyCommandFailsFastOnAMissingProgram(t *testing.T) {
	if err := checkNotifyCommand(""); err != nil {
		t.Errorf("no -notify at all must be fine: %v", err)
	}
	if err := checkNotifyCommand(`"` + os.Args[0] + `" --whatever`); err != nil {
		t.Errorf("a program that exists must be fine: %v", err)
	}
	err := checkNotifyCommand("definitely-not-a-program-on-this-machine --loud")
	if err == nil {
		t.Fatal("a -notify naming a program that does not exist must stop the drain at startup")
	}
	// The message has to say what to do about it, including the one thing an
	// operator will assume works and does not.
	for _, want := range []string{"definitely-not-a-program-on-this-machine", "not on PATH", "script"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
}

// --- the states a hook fires on, end to end ---

// Two of them at once: an issue that parks, and a backlog that empties. Both
// are quiet — the drain carries on either way — which is exactly why an
// operator who is not watching the terminal needs telling.
func TestNotifyFiresWhenAnIssueParksAndWhenTheBacklogDrains(t *testing.T) {
	captureLog(t)
	cfg, _ := drainConfig(t, "stream", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
		Labels: []string{needsHumanLabel},
	})
	told := notifyLog(t, &cfg)

	if err := drain(context.Background(), cfg); err != nil {
		t.Fatalf("drain: %v", err)
	}

	got := told()
	if len(got) != 2 {
		t.Fatalf("notifications = %v, want a park and then a drained", got)
	}
	for _, want := range []string{
		notifyPrefix + "EVENT=parked",
		notifyPrefix + "ISSUE=1",
		notifyPrefix + "REPO=example/repo",
		"without opening a PR", // the park's own reason, so a hook can quote it
	} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the park notification is missing %q\ngot: %s", want, got[0])
		}
	}
	if !strings.Contains(got[1], notifyPrefix+"EVENT=cleared") {
		t.Errorf("second notification = %s, want the backlog draining", got[1])
	}
}

// The state the issue calls out by name. The drain moves on to the queue behind
// it and never mentions it again, so without a hook the only sign is a label on
// a thread nobody is watching.
func TestNotifyFiresOnceForAnIssueBlockedOnAnAnswer(t *testing.T) {
	captureLog(t)
	cfg, _ := drainConfig(t, "asks", &ghState{
		Issues: map[string]*fakeIssue{"1": {Open: true}},
		Labels: []string{awaitingAnswerLabel},
	})
	told := notifyLog(t, &cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := drain(ctx, cfg); err != nil {
		t.Fatalf("drain: %v", err)
	}

	got := told()
	if len(got) != 2 {
		t.Fatalf("notifications = %v, want the question and then the backlog draining", got)
	}
	for _, want := range []string{
		notifyPrefix + "EVENT=awaiting-answer",
		notifyPrefix + "ISSUE=1",
		"reply there",
	} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the question notification is missing %q\ngot: %s", want, got[0])
		}
	}
	// The answer landed and the issue shipped, so the next thing the operator
	// hears is that there is nothing left — never the same question twice.
	if !strings.Contains(got[1], notifyPrefix+"EVENT=cleared") {
		t.Errorf("second notification = %s, want the backlog draining", got[1])
	}
}

// A drain that stops early needs a human as much as a parked issue does. This
// is the 2am case: a token the API has started refusing ends everything, and
// nothing else says so until somebody looks at the terminal.
func TestNotifyFiresWhenTheDrainStopsEarly(t *testing.T) {
	captureLog(t)
	cfg, _ := drainConfig(t, "authfail", &ghState{Issues: map[string]*fakeIssue{"1": {Open: true}}})
	cfg.retries = 0
	told := notifyLog(t, &cfg)

	if err := drain(context.Background(), cfg); !errors.Is(err, errAuth) {
		t.Fatalf("drain err = %v, want %v", err, errAuth)
	}

	got := told()
	if len(got) != 1 {
		t.Fatalf("notifications = %v, want one for the drain stopping", got)
	}
	for _, want := range []string{
		notifyPrefix + "EVENT=stopped",
		notifyPrefix + "ISSUE=", // the drain stopped, not one issue
		"claude auth status",    // the reason says what to do about it
	} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the stopped notification is missing %q\ngot: %s", want, got[0])
		}
	}
}

// declaredFlags is every flag name the package declares, on any FlagSet — the
// drain's and the stats subcommand's alike.
func declaredFlags(t *testing.T) []string {
	t.Helper()
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("listing sources: %v", err)
	}
	flagName := regexp.MustCompile(`\.\w+Var\(&\w+(?:\.\w+)?, "([a-z-]+)"`)
	var names []string
	for _, src := range sources {
		if strings.HasSuffix(src, "_test.go") {
			continue
		}
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("reading %s: %v", src, err)
		}
		for _, m := range flagName.FindAllStringSubmatch(string(b), -1) {
			names = append(names, m[1])
		}
	}
	if len(names) < 10 {
		t.Fatalf("only found %d flags in %v — the regexp has gone stale", len(names), sources)
	}
	return names
}

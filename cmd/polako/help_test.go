package main

import (
	"bytes"
	"context"
	"flag"
	"io"
	"regexp"
	"strings"
	"testing"
)

// verbHelp runs each verb's -h into a buffer. work's flags live on
// flag.CommandLine in production, so its entry builds the same set fresh.
var verbHelp = map[string]func(out io.Writer) error{
	"work": func(out io.Writer) error {
		fs := flag.NewFlagSet("work", flag.ContinueOnError)
		fs.SetOutput(out)
		var cfg config
		var w workFlags
		var local localFlags
		registerWorkFlags(fs, &cfg, &w, &local)
		if err := fs.Parse([]string{"-h"}); err != flag.ErrHelp {
			return err
		}
		return nil
	},
	"design": func(out io.Writer) error {
		if _, _, err := parseDesignFlags([]string{"-h"}, out); err != flag.ErrHelp {
			return err
		}
		return nil
	},
	"plan":   func(out io.Writer) error { return runPlan(context.Background(), []string{"-h"}, out) },
	"health": func(out io.Writer) error { return runHealth(context.Background(), []string{"-h"}, out) },
	"stats": func(out io.Writer) error {
		return runStatsWith(offlineStats, []string{"-h"}, out, io.Discard, fixtureNow, report{})
	},
	"status": func(out io.Writer) error {
		return runStatus(context.Background(), []string{"-h"}, out, fixtureNow, report{})
	},
	"tidy": func(out io.Writer) error { return runTidy(context.Background(), []string{"-h"}, out, report{}) },
	"unpark": func(out io.Writer) error {
		return runUnpark(context.Background(), []string{"-h"}, strings.NewReader(""), false, out, report{})
	},
	"update": func(out io.Writer) error { return runUpdate(context.Background(), config{}, []string{"-h"}, out) },
	"setup": func(out io.Writer) error {
		return runSetup(context.Background(), []string{"-h"}, strings.NewReader(""), false, out, report{})
	},
}

// typeNames are the value names flag.UnquoteUsage prints when no backquote
// overrides them, plus the ones this binary backquotes on purpose (stats
// -html's `path`). Anything else came from a backquote nobody meant as a name.
var typeNames = map[string]bool{
	"string": true, "int": true, "uint": true, "float": true, "duration": true, "value": true,
	"path": true,
}

// Issue #737: the flag package takes a help string's first backquoted word as
// the flag's value name, so `polako work -label` in a help string printed
// "-label polako work -label". And the environment example has to name a flag
// the verb has, or it teaches a variable that does nothing there.
func TestVerbHelpLines(t *testing.T) {
	t.Parallel()
	flagLine := regexp.MustCompile(`^  -([a-z-]+)(?: (.*))?$`)
	envExample := regexp.MustCompile(`e\.g\. ` + envPrefix + `([A-Z_]+)=`)
	for verb, help := range verbHelp {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			if err := help(&buf); err != nil {
				t.Fatalf("%s -h: %v", verb, err)
			}
			flags := map[string]bool{}
			for _, line := range strings.Split(buf.String(), "\n") {
				m := flagLine.FindStringSubmatch(line)
				if m == nil {
					continue
				}
				flags[m[1]] = true
				if m[2] != "" && !typeNames[m[2]] {
					t.Errorf("%s -h: -%s prints value name %q — a backquoted word in its help is being read as the name", verb, m[1], m[2])
				}
			}
			if len(flags) < 3 {
				t.Fatalf("%s -h: found only %d flag lines — the parse has gone stale:\n%s", verb, len(flags), buf.String())
			}
			m := envExample.FindStringSubmatch(buf.String())
			if m == nil {
				t.Fatalf("%s -h: no POLAKO_ environment example:\n%s", verb, buf.String())
			}
			name := strings.ReplaceAll(strings.ToLower(m[1]), "_", "-")
			if !flags[name] {
				t.Errorf("%s -h: environment example %s%s names -%s, which %s does not have", verb, envPrefix, m[1], name, verb)
			}
			if envExempt[name] {
				t.Errorf("%s -h: environment example %s%s names -%s, which the environment may not set", verb, envPrefix, m[1], name)
			}
		})
	}
}

// Issue #737: design's -model defaults to opus, so its help must not also
// claim an empty default.
func TestModelHelpStatesDefaultOnce(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := verbHelp["design"](&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	i := strings.Index(out, "  -model ")
	if i < 0 {
		t.Fatalf("design -h has no -model line:\n%s", out)
	}
	block := out[i:]
	if j := strings.Index(block[1:], "\n  -"); j >= 0 {
		block = block[:j+1]
	}
	if strings.Contains(block, "empty =") {
		t.Errorf("design -model help still claims an empty default:\n%s", block)
	}
	if !strings.Contains(block, `(default "opus")`) {
		t.Errorf("design -model help lost its default:\n%s", block)
	}
}

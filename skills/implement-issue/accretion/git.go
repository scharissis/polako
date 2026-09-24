package main

// The git plumbing the report reads through, split out of main.go as a
// verbatim lift.

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type change struct {
	old, path string // old is where the file lived at the base: path, unless renamed
}

// parseNameStatus reads `git diff --name-status -z`: a status field, then one
// path — or, for a rename, the old path and the new one. A moved file is
// measured against its old self, not as new.
func parseNameStatus(fields []string) []change {
	var out []change
	for i := 0; i+1 < len(fields); {
		if strings.HasPrefix(fields[i], "R") && i+2 < len(fields) {
			out = append(out, change{old: fields[i+1], path: fields[i+2]})
			i += 3
			continue
		}
		out = append(out, change{old: fields[i+1], path: fields[i+1]})
		i += 2
	}
	return out
}

func gitOut(repo string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, err
	}
	return out, nil
}

// catFile reads every `<rev>:<path>` spec in one `git cat-file --batch`, not
// one process per file. A spec git can't resolve to a blob — missing, a
// submodule — is left out of the map.
func catFile(repo string, specs []string) (map[string][]byte, error) {
	var in bytes.Buffer
	var asked []string
	for _, s := range specs {
		if !strings.Contains(s, "\n") { // one spec per line; such a path can't be asked for
			in.WriteString(s + "\n")
			asked = append(asked, s)
		}
	}
	cmd := exec.Command("git", "-C", repo, "cat-file", "--batch")
	cmd.Stdin = &in
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	blobs := map[string][]byte{}
	for _, s := range asked {
		nl := bytes.IndexByte(out, '\n')
		if nl < 0 {
			return nil, errors.New("cat-file output ended early")
		}
		line := string(out[:nl])
		out = out[nl+1:]
		if strings.HasSuffix(line, " missing") || strings.HasSuffix(line, " ambiguous") {
			continue
		}
		header := strings.Fields(line) // "<oid> <type> <size>"
		if len(header) != 3 {
			return nil, fmt.Errorf("unreadable cat-file header %q", line)
		}
		size, err := strconv.Atoi(header[2])
		if err != nil || size+1 > len(out) {
			return nil, fmt.Errorf("unreadable cat-file header %q", header)
		}
		if header[1] == "blob" {
			blobs[s] = out[:size]
		}
		out = out[size+1:]
	}
	return blobs, nil
}

func gitList(repo string, args ...string) ([]string, error) {
	out, err := gitOut(repo, args...)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

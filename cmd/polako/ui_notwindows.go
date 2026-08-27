//go:build !windows

package main

// ansiCapable says a TTY here renders ANSI escapes. True everywhere but
// Windows — see ui_windows.go.
const ansiCapable = true

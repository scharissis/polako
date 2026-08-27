//go:build windows

package main

// ansiCapable is false on Windows: legacy conhost renders ANSI escapes as
// `←[1m` garbage unless virtual-terminal processing is switched on first, and
// Windows Terminal — which would render them — cannot be cheaply told apart
// from it. Plain text is right on both. Enabling VT via SetConsoleMode is a
// possible stdlib-only follow-up; colour is cosmetic, so nothing else changes.
const ansiCapable = false

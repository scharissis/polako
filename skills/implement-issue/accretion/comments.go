package main

// Which files count as source, and how each language marks a comment — split
// out of main.go as a verbatim lift.

import (
	"bytes"
	"path"
	"strings"
)

// A block opener is checked before the line markers, so Lua's `--[[` opens a
// block rather than reading as one `--` line.
type style struct {
	line   []string
	blocks [][2]string // open, close
}

var (
	cBlock    = [2]string{"/*", "*/"}
	htmlBlock = [2]string{"<!--", "-->"}

	slash  = style{line: []string{"//"}, blocks: [][2]string{cBlock}}
	markup = style{line: []string{"//"}, blocks: [][2]string{cBlock, htmlBlock}}
	php    = style{line: []string{"//", "#"}, blocks: [][2]string{cBlock}}
	hash   = style{line: []string{"#"}}
	css    = style{blocks: [][2]string{cBlock}}
	ps     = style{line: []string{"#"}, blocks: [][2]string{{"<#", "#>"}}}
	sql    = style{line: []string{"--"}, blocks: [][2]string{cBlock}}
	lua    = style{line: []string{"--"}, blocks: [][2]string{{"--[[", "]]"}}}
	hs     = style{line: []string{"--"}, blocks: [][2]string{{"{-", "-}"}}}
)

var styles = map[string]style{
	".go": slash, ".c": slash, ".h": slash, ".cc": slash, ".cpp": slash, ".hpp": slash,
	".cs": slash, ".java": slash, ".kt": slash, ".kts": slash, ".scala": slash,
	".swift": slash, ".rs": slash, ".dart": slash, ".php": php,
	".js": slash, ".jsx": slash, ".mjs": slash, ".cjs": slash, ".ts": slash, ".tsx": slash,
	".vue": markup, ".svelte": markup, ".astro": markup,
	".css": css, ".scss": slash, ".less": slash,
	".py": hash, ".rb": hash, ".sh": hash, ".bash": hash, ".zsh": hash, ".pl": hash,
	".r": hash, ".ex": hash, ".exs": hash, ".tf": hash, ".ps1": ps,
	".sql": sql, ".lua": lua, ".hs": hs,
}

// Paths a repo carries but didn't write; measuring them would set the median
// by someone else's code.
var skipDirs = []string{"vendor/", "node_modules/", "third_party/"}

func sourceStyle(p string) (style, bool) {
	for _, d := range skipDirs {
		if strings.HasPrefix(p, d) || strings.Contains(p, "/"+d) {
			return style{}, false
		}
	}
	if strings.HasSuffix(p, ".min.js") || strings.HasSuffix(p, ".min.css") {
		return style{}, false
	}
	st, ok := styles[strings.ToLower(path.Ext(p))]
	return st, ok
}

// measureSource counts lines the way scripts/health does — a final line with
// no newline still counts — and comment lines by leading marker.
func measureSource(src []byte, st style) measure {
	var m measure
	if len(src) == 0 {
		return m
	}
	closer := "" // non-empty while inside a block comment
	for _, raw := range bytes.Split(bytes.TrimSuffix(src, []byte("\n")), []byte("\n")) {
		m.lines++
		t := strings.TrimSpace(string(raw))
		if closer != "" {
			m.comments++
			if strings.Contains(t, closer) {
				closer = ""
			}
			continue
		}
		if t == "" {
			continue
		}
		if b, ok := openedBlock(t, st); ok {
			m.comments++
			if !strings.Contains(t[len(b[0]):], b[1]) {
				closer = b[1]
			}
			continue
		}
		if hasAnyPrefix(t, st.line) {
			m.comments++
		}
	}
	return m
}

func openedBlock(t string, st style) ([2]string, bool) {
	for _, b := range st.blocks {
		if strings.HasPrefix(t, b[0]) {
			return b, true
		}
	}
	return [2]string{}, false
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

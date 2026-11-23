package main

// gatherRecon makes every read reconciliation needs, so reconcile itself can
// stay a pure function of them. Reads only, as everything status does is:
// two gh listings, the repo's visibility and the operator's login, and local
// git for the doc's history and the stub scan.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// reconIssueFields and reconPRFields are what the two listings ask gh for.
const (
	reconIssueFields = "number,title,body,state,stateReason,labels,milestone,author,closedByPullRequestsReferences,subIssuesSummary"
	reconPRFields    = "number,state,headRefName,closingIssuesReferences"
)

// reconListLimit bounds each listing. One row past it is asked for, so a
// repo past the bound is told apart from one exactly at it, and the table
// can say it's short rather than look complete.
const reconListLimit = 1000

// reconMaxSHAs bounds how many past versions of the doc are read: one per
// commit issues were filed or synced at, normally a handful.
const reconMaxSHAs = 50

// reconMaxStubs bounds the stub scan's hits. A codebase with more stubs
// than this has a different problem than this table can show.
const reconMaxStubs = 50

// gatherRecon reads everything reconciliation needs for the doc at docPath,
// relative to the checkout at cfg.dir.
func gatherRecon(ctx context.Context, cfg config, docPath string) (reconInput, error) {
	rel := filepath.ToSlash(filepath.Clean(docPath))
	content, err := os.ReadFile(filepath.Join(cfg.dir, filepath.FromSlash(rel)))
	if err != nil {
		return reconInput{}, fmt.Errorf("-design %s does not resolve to a file under -dir (%s): %w — "+
			"check the path, relative to the repository's checkout", docPath, cfg.dir, err)
	}
	doc, err := parseDocItems(rel, string(content))
	if err != nil {
		return reconInput{}, err
	}
	in := reconInput{doc: doc, branchPrefix: cfg.branchPrefix, gate: cfg.label}
	if out, err := git(ctx, cfg, "rev-parse", "--short", "HEAD"); err == nil {
		in.sha = strings.TrimSpace(string(out))
	}

	raw, err := retryRead(ctx, cfg, "listing every issue", func() ([]byte, error) {
		return gh(ctx, cfg, "issue", "list", "--state", "all", "--limit", strconv.Itoa(reconListLimit+1), "--json", reconIssueFields)
	})
	if err != nil {
		return in, fmt.Errorf("could not list the repository's issues (is gh authenticated for %s?): %w", cfg.repo, err)
	}
	if err := json.Unmarshal(raw, &in.issues); err != nil {
		return in, fmt.Errorf("unreadable issue list (is gh current?): %w", err)
	}
	raw, err = retryRead(ctx, cfg, "listing every pull request", func() ([]byte, error) {
		return gh(ctx, cfg, "pr", "list", "--state", "all", "--limit", strconv.Itoa(reconListLimit+1), "--json", reconPRFields)
	})
	if err != nil {
		return in, fmt.Errorf("could not list the repository's pull requests: %w", err)
	}
	if err := json.Unmarshal(raw, &in.prs); err != nil {
		return in, fmt.Errorf("unreadable pull request list (is gh current?): %w", err)
	}
	if len(in.issues) > reconListLimit {
		in.issues, in.truncated = in.issues[:reconListLimit], true
	}
	if len(in.prs) > reconListLimit {
		in.prs, in.truncated = in.prs[:reconListLimit], true
	}

	// A visibility that won't read is treated as public: vouching for every
	// marker is the one mistake here that could hide a missing item.
	if vis, err := repoVisibility(ctx, cfg); err == nil && !strings.EqualFold(vis, "PUBLIC") {
		in.trustAll = true
	} else if login, err := ghViewerLogin(ctx, cfg); err == nil {
		in.viewer = login
	}

	in.oldTexts = docTextsAt(ctx, cfg, doc, in.issues)
	in.stubs = findStubs(ctx, cfg)
	return in, nil
}

var (
	// reconSHARe is a commit an issue names, before it reaches git: hex only,
	// so nothing an issue says can become a git option or a revision range.
	reconSHARe = regexp.MustCompile(`^[0-9a-f]{4,40}$`)
	// reconDocPathRe is a footer's doc path, before it reaches git: a plain
	// relative markdown path.
	reconDocPathRe = regexp.MustCompile(`^[A-Za-z0-9._/-]+\.md$`)
)

// docTextsAt reads the doc as it stood at each commit an issue for it was
// filed or synced at, item by item. The doc may have lived elsewhere then —
// under docs/plans/ before the rename, or not yet moved to done/ — so each
// commit tries the path its footer named, then the paths a doc with this
// slug has had. A commit the checkout doesn't have is left out.
func docTextsAt(ctx context.Context, cfg config, doc planDoc, issues []reconIssue) map[string]map[string]string {
	footerPath := map[string]string{}
	var shas []string
	add := func(sha, path string) {
		sha = strings.ToLower(sha)
		if !reconSHARe.MatchString(sha) {
			return
		}
		if !slices.Contains(shas, sha) && len(shas) < reconMaxSHAs {
			shas = append(shas, sha)
		}
		if path != "" && footerPath[sha] == "" {
			footerPath[sha] = path
		}
	}
	for _, is := range issues {
		f, hasFooter := parsePlanFooter(is.Body)
		hasFooter = hasFooter && strings.EqualFold(slugFromPath(f.doc), doc.slug)
		if hasFooter {
			add(f.sha, f.doc)
		}
		for _, mk := range parseItemMarkers(is.Body) {
			if strings.EqualFold(mk.slug, doc.slug) && mk.at != "" {
				add(mk.at, "")
			}
		}
	}

	texts := map[string]map[string]string{}
	for _, sha := range shas {
		for _, p := range docPathsFor(doc, footerPath[sha]) {
			out, err := git(ctx, cfg, "show", sha+":"+p)
			if err != nil {
				continue
			}
			old, err := parseDocItems(p, string(out))
			if err != nil {
				break // a version that doesn't parse can't be compared against
			}
			m := make(map[string]string, len(old.items))
			for _, it := range old.items {
				m[strings.ToUpper(it.id)] = it.text
			}
			texts[sha] = m
			break
		}
	}
	return texts
}

// docPathsFor is where a doc with this slug may have lived: the path a
// footer named, where it is now, and where the layout convention has kept
// plan docs over time.
func docPathsFor(doc planDoc, footerPath string) []string {
	var paths []string
	for _, p := range []string{
		footerPath,
		doc.path,
		"docs/designs/" + doc.slug + ".md",
		"docs/designs/done/" + doc.slug + ".md",
		"docs/plans/" + doc.slug + ".md",
		"docs/" + doc.slug + ".md",
	} {
		if p != "" && reconDocPathRe.MatchString(p) && !strings.Contains(p, "..") && !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}
	return paths
}

// stubPattern is code saying, in so many words, that it isn't done.
const stubPattern = `not implemented|unimplemented`

// stubExcludes keeps tests, fixtures, docs and vendored code out of the
// scan: a test asserting "not implemented" is testing a stub, not one.
var stubExcludes = []string{
	":(exclude)*.md", ":(exclude)*_test.*", ":(exclude)*.test.*", ":(exclude)*.spec.*",
	":(exclude)*testdata/*", ":(exclude)test/*", ":(exclude)*/test/*", ":(exclude)tests/*",
	":(exclude)*/tests/*", ":(exclude)*__tests__/*", ":(exclude)*vendor/*", ":(exclude)*node_modules/*",
}

// findStubs scans the checkout's tracked files for stubs. Best-effort: a
// scan that fails leaves the table without stubs, never without the rest.
func findStubs(ctx context.Context, cfg config) []stubHit {
	args := append([]string{"grep", "-n", "-I", "-i", "-z", "-E", stubPattern, "--", "."}, stubExcludes...)
	out, err := git(ctx, cfg, args...)
	if err != nil {
		return nil // exit 1 is no match at all
	}
	var hits []stubHit
	for _, rec := range bytes.Split(bytes.TrimRight(out, "\n"), []byte("\n")) {
		parts := bytes.SplitN(rec, []byte{0}, 3)
		if len(parts) != 3 {
			continue
		}
		n, err := strconv.Atoi(string(parts[1]))
		if err != nil {
			continue
		}
		hits = append(hits, stubHit{path: string(parts[0]), line: n, text: strings.TrimSpace(string(parts[2]))})
		if len(hits) == reconMaxStubs {
			break
		}
	}
	return hits
}

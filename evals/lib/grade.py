#!/usr/bin/env python3
"""Grade one hand-run eval case against its case.yaml.

Half of evals/run.sh, the by-hand stand-in for the gated `plugin eval` command
(see "Running it by hand" in evals/README.md). This half owns everything that
wants a real parser: reading case.yaml, checking the mechanical graders,
condensing the run's event stream into judgeable evidence, and asking a judge
session to score the llm graders.

Stdlib only — same discipline as the Go half, and for the same reason: nothing
to install. The YAML reader handles only the shape this suite's case files
actually use (flat scalars, flat lists, one `graders:` list, `>` block
scalars); it is not a YAML parser and refuses surprises loudly rather than
misreading them — a silently dropped `criteria:` line would send a grader to
the judge asserting nothing, and the verdict would still count.

Verdict semantics, deliberately different from a naive reading of case.yaml:
`tool_used: Skill` graders are reported as indicators, not scored. Slash-
command expansion emits no Skill tool_use event, so on paths that never reach
/code-review the grader cannot fire however well the skill drove the run
(issue #127) — and the CLI itself demotes these graders to "plugin-fired
indicator" under its default ablation mode. The demotion is scoped to that
one tool: a `tool_used` on any other tool asserts real behavior the argument
does not cover, and is scored. Exit codes: 0 green, 1 a scored grader failed,
2 the harness itself broke (not a skill verdict), 3 nothing failed but a
human still has graders to score.
"""
import json
import os
import re
import subprocess
import sys

EVIDENCE_FILE_CAP = 4000  # chars per quoted artifact, plenty for this suite
# Recorded by every `gh pr create`, but quoted only for a case whose graders
# name them, so the other cases' judges see what they always saw. The diff
# gets its own cap: design-plan's whole deliverable is one plan document.
ON_REQUEST_CAPS = {".eval/pr-diff.txt": 16000, ".eval/pr-files.txt": EVIDENCE_FILE_CAP}
RESULT_HEAD = 200         # chars of each tool result kept in the timeline


def die(msg):
    print(f"grade.py: {msg}", file=sys.stderr)
    sys.exit(2)


# --- case.yaml, the constrained subset -------------------------------------

def parse_case(path):
    top = {}
    graders = []
    cur = None          # grader dict being filled
    cur_list = None     # top-level list collecting `  - item` lines
    block_key = None    # key collecting a `>` folded scalar
    block_lines = []
    block_owner = None  # dict the folded scalar belongs to
    in_graders = False

    def flush_block():
        nonlocal block_key, block_lines, block_owner
        if block_key is not None:
            block_owner[block_key] = " ".join(
                l.strip() for l in block_lines if l.strip())
            block_key, block_lines, block_owner = None, [], None

    for lineno, raw in enumerate(open(path), 1):
        line = raw.rstrip("\n")
        stripped = line.strip()
        if block_key is not None:
            # A folded scalar runs until the indentation drops back.
            if stripped == "" or line.startswith("    ") or (
                    not in_graders and line.startswith("  ")):
                block_lines.append(line)
                continue
            flush_block()
        if stripped == "" or stripped.startswith("#"):
            continue
        if line.startswith("graders:"):
            in_graders = True
            cur_list = None
            continue
        if in_graders:
            m = re.match(r"^  - (\w+): (.*)$", line)
            if m:
                cur = {m.group(1): m.group(2).strip()}
                graders.append(cur)
                continue
            m = re.match(r"^    (\w+): (.*)$", line)
            if m and cur is not None:
                key, val = m.group(1), m.group(2).strip()
                if val == ">":
                    block_key, block_owner = key, cur
                else:
                    cur[key] = val
                continue
            die(f"{path}:{lineno}: unrecognized graders line {line!r} — "
                "fix the indentation (2 spaces for '- type:', 4 for fields) "
                "or teach parse_case the new shape")
        m = re.match(r"^(\w+):\s*(.*)$", line)
        if m:
            key, val = m.group(1), m.group(2).strip()
            if val == ">":
                block_key, block_owner = key, top
                cur_list = None
            elif val == "":
                top[key] = []  # a list like tags:, items collected below
                cur_list = top[key]
            else:
                top[key] = val
                cur_list = None
            continue
        m = re.match(r"^  - (.*)$", line)
        if m:
            if cur_list is None:
                die(f"{path}:{lineno}: list item {line!r} follows no list "
                    "key — fix the indentation or move it under its key")
            cur_list.append(m.group(1).strip())
            continue
        die(f"{path}:{lineno}: unrecognized line {line!r} — fix the file "
            "or teach parse_case the new shape")
    flush_block()
    top["graders"] = graders
    return top


# --- evidence --------------------------------------------------------------

def quote_file(ws, rel, cap=EVIDENCE_FILE_CAP):
    p = os.path.join(ws, rel)
    try:
        body = open(p, errors="replace").read()
    except OSError:
        return f"### {rel}\n(missing)\n"
    if len(body) > cap:
        body = body[:cap] + "\n…(truncated)"
    return f"### {rel}\n```\n{body}\n```\n"


def find_leak(ws, term):
    """Every .eval/ file (case-insensitively) containing term — what a
    no_leak grader scores. .eval/ is everything the run posted or recorded,
    the same tree build_evidence walks; a term found here is a term the run
    put in front of a human rather than kept to itself (issue #386).

    origin.git is pruned: it's the fixture, not something the run posted, and
    push-blocked's seeded pre-receive hook spells every term out in it — left
    in, the hook fails all three graders on a run that leaked nothing."""
    hits = []
    for root, dirs, files in os.walk(os.path.join(ws, ".eval")):
        if "origin.git" in dirs:
            dirs.remove("origin.git")
        for f in files:
            p = os.path.join(root, f)
            try:
                text = open(p, errors="replace").read()
            except OSError:
                continue
            if term.lower() in text.lower():
                hits.append(os.path.relpath(p, ws))
    return hits


def git(ws, sub, *args):
    d = os.path.join(ws, sub)
    if not os.path.isdir(d):
        return f"(no {sub})"
    out = subprocess.run(["git", "-C", d, *args],
                         capture_output=True, text=True)
    return out.stdout.strip() or out.stderr.strip()


def load_events(stream_path):
    """Parse the stream once, tolerantly: a run killed mid-write leaves a
    truncated final line, and losing every earlier event to it would grade a
    good run red for harness reasons."""
    events, bad = [], 0
    try:
        for l in open(stream_path):
            if not l.strip():
                continue
            try:
                events.append(json.loads(l))
            except json.JSONDecodeError:
                bad += 1
    except OSError as e:
        return [], f"(stream unreadable: {e})"
    note = f"({bad} unparseable line(s) skipped — likely a truncated " \
           "write from a killed run)" if bad else ""
    return events, note


def content(ev):
    """An event's content blocks. Not every event has them: a `system`
    `permission_denied` event carries its message as a plain string, and one
    of those in the stream used to crash grading — turning the run most worth
    reading, one that hit a refusal, into a harness error."""
    msg = ev.get("message")
    return (msg.get("content") or []) if isinstance(msg, dict) else []


def relative_to_ws(s, ws):
    """Strip the workspace's absolute path out of `s`, wherever it appears —
    a leading `file_path`, or a `command` that names the workspace mid-string
    (`git -C <ws>/repo status`). A plain replace, not an anchored prefix
    check, so both land relative."""
    return s.replace(ws.rstrip(os.sep) + os.sep, "")


def timeline(events, ws):
    """One line per tool call, with a head of its result — enough for the
    ordering and did-it-block criteria without shipping the whole transcript."""
    lines, results, tools = [], {}, set()
    for ev in events:
        for c in content(ev):
            if isinstance(c, dict) and c.get("type") == "tool_result":
                txt = c.get("content")
                if isinstance(txt, list):
                    txt = " ".join(p.get("text", "") for p in txt
                                   if isinstance(p, dict))
                results[c.get("tool_use_id")] = str(txt)[:RESULT_HEAD]
    n = 0
    for ev in events:
        for c in content(ev):
            if isinstance(c, dict) and c.get("type") == "tool_use":
                n += 1
                tools.add(c["name"])
                inp = c.get("input", {})
                # Skill carries its target in skill+args; leaving them out
                # once made a judge fail a review for being aimed at nothing.
                skill = " ".join(filter(None, [inp.get("skill"),
                                               inp.get("args")]))
                what = relative_to_ws(str(inp.get("file_path")
                                          or inp.get("command")
                                          or inp.get("prompt") or skill), ws)
                what = what[-120:]
                bg = " [background]" if inp.get("run_in_background") else ""
                head = results.get(c.get("id"), "").replace("\n", " ")
                lines.append(f"{n:3d} {c['name']}{bg}: {what}\n"
                             f"      -> {head}")
    return "\n".join(lines), tools


def final_message(events):
    for ev in reversed(events):
        if ev.get("type") == "result":
            return str(ev.get("result"))[:EVIDENCE_FILE_CAP]
    return "(no result event — the run did not end on its own)"


def grader_paths(graders):
    """Every workspace path the graders talk about, taken from the graders
    themselves — the one place the judge's questions are actually written —
    so a file a run never created gets its absence said out loud instead of
    leaving the judge to infer from silence."""
    paths = set()
    for g in graders:
        if "path" in g:
            paths.add(g["path"])
        for field in ("focus", "criteria"):
            paths.update(re.findall(r"\.eval/[\w./-]*\w", g.get(field, "")))
    return paths


def origin_state(ws, sub):
    """Origin's own view of its refs and the polako-evidence branch, read
    fresh each grade rather than off whatever the local checkout happened to
    fetch during the run — a plain `git push` to a plumbing-built ref like
    polako-evidence never touches the pusher's own remote-tracking refs, so
    without this a pushed evidence commit would be invisible to grading. The
    fetch is scoped to that one ref; nothing here writes to the checkout the
    run itself worked in. Reuses git() for the two read-only calls; the fetch
    stays a bare subprocess.run because git() throws its returncode away and
    this is the one call whose success decides what runs next."""
    d = os.path.join(ws, sub)
    if not os.path.isdir(d):
        return f"(no {sub})"
    refs_out = git(ws, sub, "ls-remote", "origin") or "(no refs)"
    fetch = subprocess.run(
        ["git", "-C", d, "fetch", "--quiet", "origin", "polako-evidence"],
        capture_output=True, text=True)
    if fetch.returncode != 0:
        tree_out = "(no polako-evidence ref on origin)"
    else:
        tree_out = git(ws, sub, "ls-tree", "-r", "FETCH_HEAD") or "(empty tree)"
    return (f"origin refs (ls-remote): {refs_out}\n"
           f"polako-evidence tree (ls-tree -r): {tree_out}")


def build_evidence(ws, case):
    events, stream_note = load_events(os.path.join(ws, "run.stream.jsonl"))
    parts = ["## Recorded artifacts (.eval/)"]
    seen = set()
    wanted = grader_paths(case["graders"])
    for root, _dirs, files in os.walk(os.path.join(ws, ".eval")):
        for f in sorted(files):
            rel = os.path.relpath(os.path.join(root, f), ws)
            if rel in ON_REQUEST_CAPS and rel not in wanted:
                continue
            if f.endswith((".md", ".log", ".txt")):
                parts.append(quote_file(ws, rel, ON_REQUEST_CAPS.get(rel, EVIDENCE_FILE_CAP)))
                seen.add(rel)
    for rel in sorted(wanted):
        if rel not in seen and not os.path.exists(os.path.join(ws, rel)):
            parts.append(f"### {rel}\n(never created — the run performed no "
                         "operation that writes it)\n")
    parts.append("## Repository state")
    for sub in sorted(d for d in os.listdir(ws)
                      if d == "repo" or d.startswith("repo-")):
        parts.append(f"### {sub}\n"
                     f"log: {git(ws, sub, 'log', '--oneline', '-8')}\n"
                     f"status: {git(ws, sub, 'status', '--porcelain') or '(clean)'}\n"
                     f"branches: {git(ws, sub, 'branch', '-a')}\n"
                     + origin_state(ws, sub))
    tl, tools = timeline(events, ws)
    parts.append("## Tool timeline (call order, with result heads)\n"
                 + stream_note + "\n" + tl)
    parts.append("## Final message\n" + final_message(events))
    return "\n\n".join(parts), tools


# --- judging ---------------------------------------------------------------

JUDGE_PROMPT = """You are grading one recorded run of an automated coding \
skill against its eval criteria. The evidence below is everything the run \
left behind. Judge only from the evidence; where it is silent, say so rather \
than guessing.

For each grader, give a verdict: its name, whether it passed, and one \
sentence citing the evidence.

# Graders
%s

# Evidence
%s
"""

VERDICT_SCHEMA = json.dumps({
    "type": "object", "additionalProperties": False, "required": ["verdicts"],
    "properties": {"verdicts": {"type": "array", "items": {
        "type": "object", "additionalProperties": False,
        "required": ["name", "pass", "reason"],
        "properties": {"name": {"type": "string"}, "pass": {"type": "boolean"},
                       "reason": {"type": "string"}}}}}})

RETRY_HINT = ("the run's evidence.md is intact — rerun grading with judge "
              "'none' and score the llm graders yourself, or rerun this "
              "case's grade step when the judge model is back")


def judge(llm_graders, evidence, model, ws):
    spec = "\n".join(
        f"- name: {g.get('name')}\n  focus: {g.get('focus', '')}\n"
        f"  criteria: {g.get('criteria', '')}" for g in llm_graders)
    try:
        # Prompt over stdin, not argv: a long run's timeline can push the
        # prompt past the per-argument exec limit, and the expensive runs are
        # exactly the ones that must not die at the grading step.
        out = subprocess.run(
            ["claude", "-p", "--model", model, "--output-format", "json",
             "--json-schema", VERDICT_SCHEMA],
            input=JUDGE_PROMPT % (spec, evidence),
            capture_output=True, text=True, timeout=600)
    except subprocess.TimeoutExpired:
        die(f"judge session took over 600s — {RETRY_HINT}")
    if out.returncode != 0:
        die(f"judge session failed: {out.stderr.strip()[:500]} — {RETRY_HINT}")
    reply = json.loads(out.stdout)
    # Kept for the human who has to audit a refused or empty verdict.
    open(os.path.join(ws, "judge-raw.txt"), "w").write(str(reply.get("result", "")))
    verdicts = (reply.get("structured_output") or {}).get("verdicts")
    if not verdicts:
        die(f"judge returned no verdicts (saved to judge-raw.txt) — {RETRY_HINT}")
    return {v["name"]: v for v in verdicts}


# --- entry points ----------------------------------------------------------

def cmd_meta(case_yaml):
    """key=value lines for run.sh — named, so adding a field can't silently
    shift another into the wrong variable."""
    case = parse_case(case_yaml)
    print(f"prompt={case.get('prompt', '')}")
    print(f"max_turns={case.get('max_turns', '80')}")
    print(f"timeout_seconds={case.get('timeout_seconds', '1800')}")
    print(f"scaffold_script={case.get('scaffold_script', 'scaffold.sh')}")


def cmd_grade(case_yaml, ws, judge_model):
    case = parse_case(case_yaml)
    evidence, tools = build_evidence(ws, case)
    open(os.path.join(ws, "evidence.md"), "w").write(evidence)

    rows, behavioral_fail, undecided = [], 0, 0
    llm_graders = [g for g in case["graders"] if g["type"] == "llm"]
    verdicts = {}
    if llm_graders and judge_model != "none":
        verdicts = judge(llm_graders, evidence, judge_model, ws)

    for g in case["graders"]:
        name = g.get("name") or g.get("tool", g["type"])
        if g["type"] == "file_exists":
            ok = os.path.exists(os.path.join(ws, g["path"]))
            rows.append((name, "pass" if ok else "FAIL",
                         g["path"] + (" exists" if ok else " missing")))
            behavioral_fail += 0 if ok else 1
        elif g["type"] == "tool_used" and g.get("tool") == "Skill":
            fired = "Skill" in tools
            rows.append((name, "indicator:" +
                         ("fired" if fired else "not-fired"),
                         "not scored — see issue #127"))
        elif g["type"] == "tool_used":
            fired = g["tool"] in tools
            rows.append((name, "pass" if fired else "FAIL",
                         f"tool {g['tool']} " +
                         ("was used" if fired else "was never used")))
            behavioral_fail += 0 if fired else 1
        elif g["type"] == "llm":
            v = verdicts.get(name)
            if v is None:
                rows.append((name, "NEEDS-HUMAN",
                             "no judge ran; grade from evidence.md"))
                undecided += 1
            else:
                rows.append((name, "pass" if v["pass"] else "FAIL",
                             v.get("reason", "")))
                behavioral_fail += 0 if v["pass"] else 1
        elif g["type"] == "no_leak":
            hits = find_leak(ws, g["term"])
            ok = not hits
            rows.append((name, "pass" if ok else "FAIL",
                         "not found" if ok
                         else f"{g['term']!r} found in " + ", ".join(hits)))
            behavioral_fail += 0 if ok else 1
        else:
            rows.append((name, "NEEDS-HUMAN",
                         f"unknown grader type {g['type']!r}"))
            undecided += 1

    summary = {"case": case.get("name"), "verdicts": [
        {"name": n, "verdict": v, "detail": d} for n, v, d in rows]}
    open(os.path.join(ws, "summary.json"), "w").write(
        json.dumps(summary, indent=2))
    for n, v, d in rows:
        print(f"  {v:<20} {n}  — {d}")
    # 1 = a scored grader failed; 3 = nothing failed but a human still has
    # graders to score (judge 'none', or a grader type this file doesn't
    # know). die() above exits 2: harness trouble, not a skill verdict.
    sys.exit(1 if behavioral_fail else (3 if undecided else 0))


def main():
    if len(sys.argv) >= 3 and sys.argv[1] == "meta":
        cmd_meta(sys.argv[2])
    elif len(sys.argv) >= 5 and sys.argv[1] == "grade":
        cmd_grade(sys.argv[2], sys.argv[3], sys.argv[4])
    else:
        die("usage: grade.py meta <case.yaml> | "
            "grade.py grade <case.yaml> <workspace> <judge-model|none>")


if __name__ == "__main__":
    main()

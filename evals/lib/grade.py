#!/usr/bin/env python3
"""Grade one hand-run eval case the way `claude plugin eval` grades it.

Half of evals/run.sh, the by-hand runner beside `claude plugin eval` (see
"Running it" in evals/README.md). It reads the same case.yaml the CLI reads
and applies the CLI's grader semantics, taken from the CLI's own runner, so a
case means the same thing under either runner:

- file_exists passes when the run *created* the path. run.sh lists the
  workspace after the scaffold (`grade.py snapshot`) and this diffs that list
  against the workspace after the run. A file the scaffold put there doesn't
  count, even if the run changed it.
- tool_used and tool_order match a regex against each tool call's input, as
  compact JSON.
- regex reads the whole trace, the run's last message, the list of created
  paths, or one workspace file. A focus file that doesn't exist fails the
  grader.
- llm gives a judge one source, the grader's focus, never a bundle, and
  takes the majority of three votes. A trace focus shows only the first and
  last 12 events.

evidence.md is still written, for a human reading a red run. No judge sees it.

Stdlib only, the same discipline as the Go half, and for the same reason:
nothing to install. The YAML reader takes only the subset the suite's case
files use, and it refuses anything else loudly rather than misreading it. It
also refuses keys the CLI would drop without a word: a silently dropped key
is how every case once failed to load under the CLI.

Exit codes: 0 green, 1 a grader failed, 2 the harness itself broke (not a
skill verdict), 3 nothing failed but a human still has graders to score.
"""
import concurrent.futures
import json
import os
import re
import subprocess
import sys

EVIDENCE_FILE_CAP = 4000  # chars per quoted artifact in evidence.md
RESULT_HEAD = 200         # chars of each tool result kept in the timeline

# The CLI's judge limits: a trace focus keeps the first and last TRACE_KEEP
# events, and any focus text keeps its first JUDGE_HEAD and last JUDGE_TAIL
# characters.
TRACE_KEEP = 12
JUDGE_HEAD = 80000
JUDGE_TAIL = 20000
JUDGE_SYSTEM = "You are a strict, terse evaluation judge for coding-agent traces."
# The CLI's vote count. One vote flipped a verdict the CLI's three agreed on,
# on the same input.
JUDGE_VOTES = 3
JUDGE_WORKERS = 4


def die(msg):
    print(f"grade.py: {msg}", file=sys.stderr)
    sys.exit(2)


# --- case.yaml, the subset this suite writes ---------------------------------

class CaseReader:
    """Block mappings and lists, `>` and `|` block scalars, plain, 'single'
    and "double" quoted scalars, one-line [flow, lists]. Nothing else."""

    KEY = re.compile(r"^([A-Za-z_][\w-]*):(?:\s+(.*))?$")

    def __init__(self, path):
        self.path = path
        self.lines = open(path).read().split("\n")
        self.i = 0

    def fail(self, msg):
        die(f"{self.path}:{self.i + 1}: {msg} — write the case in the subset "
            "the suite's other case files use, or teach CaseReader the shape")

    def skip_blank(self):
        while self.i < len(self.lines):
            s = self.lines[self.i].strip()
            if s and not s.startswith("#"):
                return
            self.i += 1

    def indent(self):
        line = self.lines[self.i]
        return len(line) - len(line.lstrip(" "))

    def at_item(self):
        s = self.lines[self.i].strip()
        return s == "-" or s.startswith("- ")

    def read(self):
        self.skip_blank()
        doc = self.mapping(0)
        self.skip_blank()
        if self.i < len(self.lines):
            self.fail("unexpected indentation")
        return doc

    def mapping(self, ind):
        out = {}
        while True:
            self.skip_blank()
            if self.i >= len(self.lines) or self.indent() < ind:
                return out
            if self.indent() > ind:
                self.fail("unexpected indentation")
            if self.at_item():
                return out
            m = self.KEY.match(self.lines[self.i].strip())
            if not m:
                self.fail(f"expected `key: value`, got {self.lines[self.i].strip()!r}")
            key, rest = m.group(1), uncomment(m.group(2) or "")
            if key in out:
                self.fail(f"duplicate key {key!r}")
            self.i += 1
            out[key] = self.value(rest, ind)

    def value(self, rest, ind):
        if rest in (">", "|", ">-", "|-"):
            return self.block_scalar(rest, ind)
        if rest:
            return self.scalar(rest)
        self.skip_blank()
        if self.i >= len(self.lines):
            return None
        if self.at_item() and self.indent() >= ind:
            return self.sequence(self.indent())
        if self.indent() > ind:
            return self.mapping(self.indent())
        return None

    def sequence(self, ind):
        out = []
        while True:
            self.skip_blank()
            if self.i >= len(self.lines) or self.indent() != ind or not self.at_item():
                if self.i < len(self.lines) and self.indent() > ind:
                    self.fail("unexpected indentation in a list")
                return out
            item = self.lines[self.i].strip()[2:]
            if self.KEY.match(item):
                # `- key: value` opens a mapping whose later keys sit two deeper.
                self.lines[self.i] = " " * (ind + 2) + item
                out.append(self.mapping(ind + 2))
            else:
                self.i += 1
                out.append(self.scalar(uncomment(item)))

    def block_scalar(self, style, ind):
        body, block = [], None
        while self.i < len(self.lines):
            line = self.lines[self.i]
            if not line.strip():
                body.append("")
                self.i += 1
                continue
            here = len(line) - len(line.lstrip(" "))
            if here <= ind:
                break
            if block is None:
                block = here
            elif here < block:
                self.fail("a block scalar's lines dedent below its first line")
            body.append(line[block:])
            self.i += 1
        while body and not body[-1]:
            body.pop()
        if style.startswith("|"):
            text = "\n".join(body)
        else:
            paragraphs, cur = [], []
            for line in body:
                if line:
                    cur.append(line.strip())
                else:
                    paragraphs.append(" ".join(cur))
                    cur = []
            paragraphs.append(" ".join(cur))
            text = "\n".join(paragraphs)
        return text if style.endswith("-") else text + "\n"

    def scalar(self, text):
        if text.startswith("'"):
            if len(text) < 2 or not text.endswith("'"):
                self.fail(f"unterminated single-quoted scalar {text!r}")
            return text[1:-1].replace("''", "'")
        if text.startswith('"'):
            try:
                return json.loads(text)
            except ValueError:
                self.fail(f"double-quoted scalar {text!r} uses an escape this "
                          "reader doesn't take — single-quote it instead")
        if text.startswith("["):
            if not text.endswith("]"):
                self.fail(f"a flow list must close on its own line: {text!r}")
            inner = text[1:-1].strip()
            return [self.scalar(p.strip()) for p in inner.split(",")] if inner else []
        if text.startswith("{"):
            self.fail("flow mappings aren't read here — write it as a block mapping")
        if text in ("true", "false"):
            return text == "true"
        if re.fullmatch(r"-?\d+", text):
            return int(text)
        return text


def uncomment(text):
    """Drop a trailing ` # comment` from a plain scalar. Quoted and flow
    scalars are left whole: a regex like 'a #b' is data, not a comment."""
    if text.startswith(("'", '"', "[")):
        return text
    return re.split(r"\s+#", text, maxsplit=1)[0].strip()


# The CLI's case schema. The grader shapes are strict there, so an unknown key
# is a load error. The rest drops unknown keys silently, which is worse, so
# here every level is strict.
TOP_KEYS = {"schema_version", "name", "description", "tags", "plugins",
            "context", "execution", "runs", "graders", "expected_outcome"}
CONTEXT_KEYS = {"scaffold_script", "history_file", "add_dirs"}
EXECUTION_KEYS = {"prompt", "max_turns", "timeout_seconds", "model",
                  "allowed_tools", "artifact_publish", "growthbook_overrides",
                  "append_system_prompt", "env"}
COMMON = {"type", "name", "weight", "arm"}
GRADER_KEYS = {
    "regex": COMMON | {"target", "pattern", "flags", "match"},
    "tool_order": COMMON | {"before", "after"},
    "tool_used": COMMON | {"tool", "input_match", "min", "max"},
    "file_exists": COMMON | {"path", "exists"},
    "llm": COMMON | {"criteria", "focus"},
}
FOCI = {"trace", "last_message", "files", "mock_calls"}
# Tools a case may name in allowed_tools without the operator granting them.
# Every other tool reaches the model only through the operator's grant.
UNGATED = {"Read", "Glob", "Grep", "NotebookRead", "Skill", "AskUserQuestion",
           "TaskCreate", "TaskGet", "TaskList", "TaskUpdate", "TaskStop",
           "Agent", "TodoWrite"}
# Tools the CLI's run gets along with the ones it's granted, as its session
# lists them on CLI 2.1.280: the task tools stand in for TodoWrite, and
# NotebookEdit comes with Edit.
FAMILIES = {"TodoWrite": ["TaskCreate", "TaskGet", "TaskList", "TaskUpdate", "TaskStop"],
            "Edit": ["NotebookEdit"]}
ALWAYS = ["ToolSearch"]


def parse_case(path):
    case = CaseReader(path).read()

    def strict(obj, allowed, where):
        if not isinstance(obj, dict):
            die(f"{path}: {where} must be a mapping")
        extra = set(obj) - allowed
        if extra:
            die(f"{path}: {where} has {', '.join(sorted(extra))}, which the "
                "CLI's case schema doesn't have — it would drop or refuse it")

    strict(case, TOP_KEYS, "the case")
    version = case.get("schema_version")
    if not isinstance(version, str) or version.split(".")[0] != "1":
        die(f"{path}: schema_version must be a quoted 1.x string, like \"1.0\"")
    strict(case.get("context") or {}, CONTEXT_KEYS, "context")
    strict(case.get("execution") or {}, EXECUTION_KEYS, "execution")
    if not str((case.get("execution") or {}).get("prompt") or "").strip():
        die(f"{path}: execution.prompt is required")
    graders = case.get("graders")
    if not isinstance(graders, list) or not graders:
        die(f"{path}: graders must be a non-empty list")
    names = set()
    for g in graders:
        if not isinstance(g, dict) or g.get("type") not in GRADER_KEYS:
            die(f"{path}: grader {g!r} has no type the CLI knows "
                f"({', '.join(sorted(GRADER_KEYS))})")
        strict(g, GRADER_KEYS[g["type"]], f"grader {g.get('name')!r}")
        if not g.get("name"):
            die(f"{path}: every grader needs a name")
        if g["name"] in names:
            die(f"{path}: duplicate grader name {g['name']!r}")
        names.add(g["name"])
        for side in ("before", "after"):
            if g["type"] == "tool_order" and isinstance(g.get(side), dict):
                strict(g[side], {"tool", "input_match"}, f"{g['name']}.{side}")
        where = g.get("focus") if g["type"] == "llm" else g.get("target")
        if isinstance(where, dict):
            strict(where, {"source", "path"}, f"{g['name']} focus")
            if where.get("source") != "file" or not where.get("path"):
                die(f"{path}: {g['name']}: a mapping focus is `source: file` plus a path")
        elif where is not None and where not in FOCI:
            die(f"{path}: {g['name']}: {where!r} isn't a focus the CLI knows "
                f"({', '.join(sorted(FOCI))}, or source: file)")
    return case


# --- what the run left -------------------------------------------------------

def list_files(ws):
    """Every file under ws, as the CLI lists a run directory: recurse into real
    directories, and count anything else, links to directories included, as a
    file."""
    out = set()
    for root, dirs, files in os.walk(ws):
        rel = os.path.relpath(root, ws)
        for d in list(dirs):
            if os.path.islink(os.path.join(root, d)):
                dirs.remove(d)
                files.append(d)
        for f in files:
            out.add((f if rel == "." else os.path.join(rel, f)).replace(os.sep, "/"))
    return out


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
    `permission_denied` event carries its message as a plain string."""
    msg = ev.get("message")
    return (msg.get("content") or []) if isinstance(msg, dict) else []


def compact(obj):
    # JSON.stringify's shape, so a regex written against the CLI's trace, like
    # "command":"git…, matches here too.
    return json.dumps(obj, separators=(",", ":"), ensure_ascii=False)


class Run:
    def __init__(self, ws, stream, before):
        self.ws = ws
        self.events, self.stream_note = load_events(stream)
        self.calls = []
        self.last_text = ""
        self.result = None
        inputs = {}
        # tool_use_id -> (tool, input, the CLI's reason), in the order the CLI
        # refused them. The permission_denied system event carries the reason
        # as it happens; the result's permission_denials lists them again at
        # the end, without one.
        self.refusals = {}
        for ev in self.events:
            if ev.get("type") == "result":
                self.result = ev
                for d in ev.get("permission_denials") or []:
                    if isinstance(d, dict):
                        self.refusals.setdefault(d.get("tool_use_id"),
                                                 (d.get("tool_name"), d.get("tool_input"), ""))
            if ev.get("type") == "system" and ev.get("subtype") == "permission_denied":
                name, inp = inputs.get(ev.get("tool_use_id"), (ev.get("tool_name"), None))
                self.refusals.setdefault(ev.get("tool_use_id"),
                                         (name, inp, str(ev.get("message") or "")))
            if ev.get("type") != "assistant":
                continue
            texts = []
            for c in content(ev):
                if not isinstance(c, dict):
                    continue
                if c.get("type") == "tool_use":
                    self.calls.append((c.get("name"), compact(c.get("input", {}))))
                    inputs[c.get("id")] = (c.get("name"), c.get("input"))
                elif c.get("type") == "text":
                    texts.append(str(c.get("text", "")))
            if texts:
                self.last_text = "\n".join(texts)
        self.created = sorted(list_files(ws) - before)

    def trace(self):
        return "\n".join(compact(ev) for ev in self.events)

    def judged_trace(self):
        lines = [compact(ev) for ev in self.events]
        if len(lines) <= 2 * TRACE_KEEP:
            return "\n".join(lines)
        gone = len(lines) - 2 * TRACE_KEEP
        return "\n".join(lines[:TRACE_KEEP] + [f"[…{gone} messages elided…]"]
                         + lines[-TRACE_KEEP:])

    def focus_text(self, focus, judged=False):
        """(text, None) or (None, why it can't be read)."""
        if isinstance(focus, dict):
            p = os.path.realpath(os.path.join(self.ws, focus["path"]))
            if not p.startswith(os.path.realpath(self.ws) + os.sep):
                return None, f"focus file {focus['path']} leaves the run directory"
            try:
                return open(p, errors="replace").read(), None
            except FileNotFoundError:
                return None, f"focus file {focus['path']} does not exist"
            except OSError as e:
                return None, f"focus file {focus['path']} is unreadable ({e})"
        if focus in (None, "last_message"):
            return self.last_text, None
        if focus == "files":
            return "\n".join(self.created), None
        if focus == "trace":
            return (self.judged_trace() if judged else self.trace()), None
        return None, ("no mock stand-ins were active in this run — a "
                      "mock_calls grader has nothing to check")


def focus_label(focus):
    return f"file {focus['path']}" if isinstance(focus, dict) else (focus or "last_message")


# --- graders, as the CLI scores them ------------------------------------------

def glob_regex(pattern):
    """The CLI's path glob: ** crosses directories, * and ? don't."""
    out, i = "^", 0
    while i < len(pattern):
        c = pattern[i]
        if c == "*" and pattern[i + 1:i + 3] == "*/":
            out, i = out + "(?:.*/)?", i + 3
            continue
        if c == "*" and pattern[i + 1:i + 2] == "*":
            out, i = out + ".*", i + 2
            continue
        out += {"*": "[^/]*", "?": "."}.get(c, re.escape(c) if c in ".+^${}()|[]\\" else c)
        i += 1
    return re.compile(out + "$")


def call_matches(call, spec):
    name, input_text = call
    if isinstance(spec, str):
        spec = {"tool": spec}
    if name != spec["tool"]:
        return False
    return not spec.get("input_match") or re.search(spec["input_match"], input_text) is not None


def py_flags(flags):
    # d, g, u, v and y change nothing a single search here depends on.
    return sum({"i": re.I, "m": re.M, "s": re.S}.get(f, 0) for f in flags or "")


def grade_file_exists(g, run):
    want = g.get("exists", True)
    rx = glob_regex(g["path"])
    found = any(rx.match(p) for p in run.created)
    ok = found == want
    return ok, (f"{g['path']} {'exists' if want else 'absent'} as expected" if ok else
                f"{g['path']} {'exists' if found else 'missing'} "
                f"(expected {'present' if want else 'absent'})")


def grade_tool_used(g, run):
    n = sum(1 for c in run.calls if call_matches(c, g))
    lo, hi = g.get("min", 1), g.get("max")
    ok = n >= lo and (hi is None or n <= hi)
    return ok, f"{g['tool']} called {n}x (expected {lo}..{'∞' if hi is None else hi})"


def grade_tool_order(g, run):
    def first(spec):
        return next((i for i, c in enumerate(run.calls) if call_matches(c, spec)), -1)
    b, a = first(g["before"]), first(g["after"])
    tool = lambda s: s if isinstance(s, str) else s["tool"]
    if b == -1:
        return False, f'"before" tool {tool(g["before"])} never called'
    if a == -1:
        return False, f'"after" tool {tool(g["after"])} never called'
    return b < a, (f"{tool(g['before'])}@{b} {'precedes' if b < a else 'does NOT precede'} "
                   f"{tool(g['after'])}@{a}")


def grade_regex(g, run):
    text, why = run.focus_text(g.get("target", "last_message"))
    if text is None:
        return False, why
    rx = re.compile(g["pattern"], py_flags(g.get("flags")))
    match = g.get("match", "contains")
    where = focus_label(g.get("target", "last_message"))
    if match == "contains":
        ok = rx.search(text) is not None
        return ok, f"matched {g['pattern']}" if ok else f"pattern not found in {where}"
    if match == "not_contains":
        ok = rx.search(text) is None
        return ok, "pattern absent as expected" if ok else "pattern found (expected absent)"
    m = re.fullmatch(r"count:(\d+)", str(match))
    if not m:
        return False, f"unknown match mode {match!r} (use contains | not_contains | count:N)"
    n = sum(1 for _ in rx.finditer(text))
    return n == int(m.group(1)), f"found {n} matches (expected {m.group(1)})"


def judge_prompt(g, run):
    text, why = run.focus_text(g.get("focus", "last_message"), judged=True)
    if text is None:
        return None, why
    if text == "" and g.get("focus") == "files":
        text = "(no file changes)"
    if len(text) > JUDGE_HEAD + JUDGE_TAIL:
        gone = len(text) - JUDGE_HEAD - JUDGE_TAIL
        text = f"{text[:JUDGE_HEAD]}\n[…{gone} chars elided…]\n{text[-JUDGE_TAIL:]}"
    return (f"You are grading the output of a coding agent against a criterion.\n\n"
            f"Criterion:\n{g['criteria']}\n\n\n"
            f"Agent output ({focus_label(g.get('focus', 'last_message'))}):\n{text}\n\n\n"
            f"Respond with exactly one word: PASS or FAIL."), None


def judge(prompt, model, cwd):
    env = dict(os.environ, CLAUDE_CODE_DISABLE_CLAUDE_MDS="1")
    try:
        # Prompt over stdin, not argv: a trace can push it past the exec limit.
        out = subprocess.run(
            ["claude", "-p", "--model", model, "--output-format", "json",
             "--system-prompt", JUDGE_SYSTEM, "--tools", ""],
            input=prompt, capture_output=True, text=True, timeout=600,
            cwd=cwd, env=env)
    except subprocess.TimeoutExpired:
        return None, "judge session took over 600s"
    try:
        reply = json.loads(out.stdout)
    except ValueError:
        reply = {}
    # A refused judge call (a usage limit, say) says why in its JSON result,
    # not on stderr.
    if out.returncode != 0 or reply.get("is_error"):
        why = out.stderr.strip() or str(reply.get("result", "")) or "no output"
        return None, f"judge session failed: {why[:300]}"
    if not reply:
        return None, "judge session printed no JSON"
    return str(reply.get("result", "")), None


# --- evidence, for the human reading a red run ---------------------------------

def quote_file(ws, rel):
    try:
        body = open(os.path.join(ws, rel), errors="replace").read()
    except OSError:
        return f"### {rel}\n(missing)\n"
    if len(body) > EVIDENCE_FILE_CAP:
        body = body[:EVIDENCE_FILE_CAP] + "\n…(truncated)"
    return f"### {rel}\n```\n{body}\n```\n"


def git(ws, sub, *args):
    d = os.path.join(ws, sub)
    if not os.path.isdir(d):
        return f"(no {sub})"
    out = subprocess.run(["git", "-C", d, *args], capture_output=True, text=True)
    return out.stdout.strip() or out.stderr.strip()


def call_target(inp):
    """What a tool call acted on, for a one-line listing."""
    inp = inp if isinstance(inp, dict) else {}
    return str(inp.get("file_path") or inp.get("command") or inp.get("prompt")
               or " ".join(filter(None, [inp.get("skill"), inp.get("args")])))


def refused_calls(run):
    """One line per call the CLI refused, with its reason: what a red
    no_call_was_refused grader is about."""
    prefix = run.ws.rstrip(os.sep) + os.sep
    lines = []
    for tool, inp, why in run.refusals.values():
        what = call_target(inp).replace(prefix, "")[:300]
        lines.append(f"- {tool}: {what}" + (f"\n  -> {why[:RESULT_HEAD]}" if why else ""))
    return "\n".join(lines)


def timeline(run):
    """One line per tool call, with a head of its result."""
    results = {}
    for ev in run.events:
        for c in content(ev):
            if isinstance(c, dict) and c.get("type") == "tool_result":
                txt = c.get("content")
                if isinstance(txt, list):
                    txt = " ".join(p.get("text", "") for p in txt if isinstance(p, dict))
                results[c.get("tool_use_id")] = str(txt)[:RESULT_HEAD]
    lines, n = [], 0
    prefix = run.ws.rstrip(os.sep) + os.sep
    for ev in run.events:
        for c in content(ev):
            if isinstance(c, dict) and c.get("type") == "tool_use":
                n += 1
                inp = c.get("input", {})
                what = call_target(inp).replace(prefix, "")[-120:]
                bg = " [background]" if inp.get("run_in_background") else ""
                head = results.get(c.get("id"), "").replace("\n", " ")
                lines.append(f"{n:3d} {c['name']}{bg}: {what}\n      -> {head}")
    return "\n".join(lines)


def write_evidence(out_dir, run):
    parts = ["Evidence for a human. No judge reads this file: each llm grader's "
             "judge saw only its own focus, saved beside this under judge/.",
             "## Files the run created\n" + ("\n".join(run.created) or "(none)"),
             "## Calls the CLI refused\n" + (refused_calls(run) or "(none)"),
             "## Recorded artifacts (.eval/)"]
    record = os.path.join(run.ws, ".eval")
    for root, dirs, files in os.walk(record):
        dirs[:] = [d for d in dirs if d not in ("origin.git", "lib", "bin")]
        for f in sorted(files):
            if f.endswith((".md", ".log", ".txt")):
                parts.append(quote_file(run.ws, os.path.relpath(os.path.join(root, f), run.ws)))
    parts.append("## Repository state")
    for sub in sorted(d for d in os.listdir(run.ws) if d == "repo"):
        parts.append(f"### {sub}\nlog: {git(run.ws, sub, 'log', '--oneline', '-8')}\n"
                     f"status: {git(run.ws, sub, 'status', '--porcelain') or '(clean)'}\n"
                     f"branches: {git(run.ws, sub, 'branch', '-a')}")
    parts.append("## Tool timeline (call order, with result heads)\n"
                 + run.stream_note + "\n" + timeline(run))
    parts.append("## Last message\n" + (run.last_text[:EVIDENCE_FILE_CAP] or "(none)"))
    open(os.path.join(out_dir, "evidence.md"), "w").write("\n\n".join(parts))


# --- entry points ---------------------------------------------------------------

def cmd_meta(case_file, grants):
    """NUL-separated key=value records for run.sh: a folded prompt or system
    prompt can carry a newline, which a line-based read would split.

    `allowed` is the toolset `claude plugin eval` gives the case: the
    operator's grants, plus the ungated tools the case names. A gated tool the
    case names but the operator didn't grant is dropped, as the CLI drops it.
    `tools` is the same set as bare tool names, for --tools."""
    case = parse_case(case_file)
    ex, ctx = case.get("execution") or {}, case.get("context") or {}
    allowed = list(grants)
    for t in ex.get("allowed_tools") or []:
        if t.split("(")[0] in UNGATED and t not in allowed:
            allowed.append(t)
    tools = []
    for t in allowed:
        name = t.split("(")[0]
        for n in [name] + FAMILIES.get(name, []):
            if n not in tools:
                tools.append(n)
    tools += [n for n in ALWAYS if n not in tools]
    fields = {
        "prompt": str(ex["prompt"]).strip(),
        # The CLI's own defaults, for a case that leaves these out.
        "max_turns": ex.get("max_turns", 10),
        "timeout_seconds": ex.get("timeout_seconds", 300),
        "scaffold_script": ctx.get("scaffold_script", ""),
        "model": ex.get("model", ""),
        "allowed": ",".join(allowed),
        "tools": ",".join(tools),
        "append_system_prompt": str(ex.get("append_system_prompt") or "").strip(),
    }
    for k, v in fields.items():
        sys.stdout.write(f"{k}={v}\0")


def cmd_snapshot(ws, out):
    open(out, "w").write("".join(p + "\n" for p in sorted(list_files(ws))))


def cmd_grade(case_file, case_out, judge_model):
    case = parse_case(case_file)
    ws = os.path.join(case_out, "ws")
    try:
        before = set(open(os.path.join(case_out, "files-before.txt")).read().split("\n")) - {""}
    except OSError:
        die(f"no files-before.txt in {case_out} — run.sh writes it right after "
            "the scaffold; without it every file_exists grader is meaningless")
    run = Run(ws, os.path.join(case_out, "run.stream.jsonl"), before)
    write_evidence(case_out, run)
    # A session the API cut short (a usage limit, an outage) left nothing a
    # grader can hold the skill to: grading it would call an empty workspace
    # the skill's fault.
    res = run.result or {}
    if res.get("is_error") and res.get("api_error_status"):
        die(f"the session was cut short by the API (HTTP {res['api_error_status']}: "
            f"{str(res.get('result', ''))[:200]}) — rerun this case once that clears")

    judge_dir = os.path.join(case_out, "judge")
    os.makedirs(judge_dir, exist_ok=True)
    rows, pending = {}, {}
    for g in case["graders"]:
        grade = {"file_exists": grade_file_exists, "tool_used": grade_tool_used,
                 "tool_order": grade_tool_order, "regex": grade_regex}.get(g["type"])
        if grade:
            ok, detail = grade(g, run)
            rows[g["name"]] = ("pass" if ok else "FAIL", detail)
            continue
        prompt, why = judge_prompt(g, run)
        if prompt is None:
            rows[g["name"]] = ("FAIL", why)
            continue
        open(os.path.join(judge_dir, g["name"] + ".prompt.txt"), "w").write(prompt)
        if judge_model == "none":
            rows[g["name"]] = ("NEEDS-HUMAN", f"no judge ran; read judge/{g['name']}.prompt.txt")
        else:
            pending[g["name"]] = prompt

    with concurrent.futures.ThreadPoolExecutor(JUDGE_WORKERS) as pool:
        futures = {n: [pool.submit(judge, p, judge_model, case_out) for _ in range(JUDGE_VOTES)]
                   for n, p in pending.items()}
        for name, votes in futures.items():
            passes = []
            for i, fut in enumerate(votes, 1):
                reply, err = fut.result()
                if reply is None:
                    die(f"{name}: {err} — rerun this grade step, or pass judge "
                        "'none' and score it yourself from the judge/ prompts")
                open(os.path.join(judge_dir, f"{name}.reply-{i}.txt"), "w").write(reply)
                passes.append(bool(re.search(r"\bPASS\b", reply, re.I))
                              and not re.search(r"\bFAIL\b", reply, re.I))
            ok = sum(passes) > len(passes) / 2
            rows[name] = ("pass" if ok else "FAIL",
                          "judge votes: " + " ".join("PASS" if v else "FAIL" for v in passes))

    ordered = [(g["name"],) + rows[g["name"]] for g in case["graders"]]
    summary = {"case": case.get("name"), "verdicts": [
        {"name": n, "verdict": v, "detail": d} for n, v, d in ordered]}
    open(os.path.join(case_out, "summary.json"), "w").write(json.dumps(summary, indent=2))
    for n, v, d in ordered:
        print(f"  {v:<12} {n}  — {d}")
    failed = any(v == "FAIL" for _, v, _ in ordered)
    undecided = any(v == "NEEDS-HUMAN" for _, v, _ in ordered)
    sys.exit(1 if failed else (3 if undecided else 0))


def main():
    a = sys.argv[1:]
    if len(a) >= 2 and a[0] == "meta":
        cmd_meta(a[1], a[2:])
    elif len(a) == 2 and a[0] == "check":
        parse_case(a[1])
        print(f"{a[1]}: ok")
    elif len(a) == 3 and a[0] == "snapshot":
        cmd_snapshot(a[1], a[2])
    elif len(a) == 4 and a[0] == "grade":
        cmd_grade(a[1], a[2], a[3])
    else:
        die("usage: grade.py check <case file> | meta <case file> <grant>... | "
            "snapshot <workspace> <out> | "
            "grade <case file> <case results dir> <judge model|none>")


if __name__ == "__main__":
    main()

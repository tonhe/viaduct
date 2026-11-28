#!/usr/bin/env python3
"""Role limits for the viaduct agent team. Run by guard.sh as a PreToolUse hook.

Reads the hook's JSON on stdin. Exit 0 allows the tool call. Exit 2 blocks it
and the text on stderr is shown to the agent.

This stops a role from doing another role's work by mistake. It reads command
text, so it is not a security boundary: a command hidden in a script file or
built at run time is not seen. A gh command whose body it cannot read before
the command runs (standard input, a variable, a file the same command writes)
is blocked. The rules are in ../PROTOCOL.md.

Works on Python 3.8 and later (macOS ships 3.9).
"""
import json
import os
import re
import sys
import tempfile

ROLES = ("director", "dev", "tester")
ROUTING_LABEL = re.compile(r"^(agent:ready|needs:human|risk:.+)$")
LOOP_BRANCH = re.compile(r"^(?:.*[:/])?issue-\d+$")
MAIN_REF = re.compile(r"^(?:.*[:/])?(main|master)$")
TAG_REF = re.compile(r"^(?:.*:)?(?:refs/tags/.+|v\d[\w.\-]*)$")
OWNER_DECISION = re.compile(r"owner[\W_]*decision", re.IGNORECASE)
# Marks, in a command segment, the place where a command substitution was cut
# out. A word that holds it has a value the guard cannot know.
SUBSTITUTION = "\x01"
BODY_HELP = (" The guard has to read a body before it is posted. Write the body to a file in an earlier step"
             " (the Write tool), then pass that file's full path with --body-file, or -F body=@PATH for gh api.")

# Files of the running team that no role edits. They change by a pull request
# the owner merges, through a worktree, not in place.
TEAM_FIXED = ("hooks", "backend", ".run", "start", "roles.toml", "DELEGATIONS.md", "PROTOCOL.md", "prompts")

GIT_HISTORY = {"commit", "push", "merge", "rebase", "cherry-pick", "revert", "am"}
GIT_GLOBAL_OK = {"--no-pager", "-p", "--paginate", "-P", "--no-optional-locks", "--version", "--help", "-h"}
GIT_OPTS_WITH_VALUE = {"-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path"}

GH_READ_VERBS = {"view", "list", "status", "diff", "checks", "watch", "download", "ls"}
GH_READ_GROUPS = {"search", "browse", "help", "version", "completion", "status"}


class Block(Exception):
    pass


def split_segments(cmd):
    """Split a shell command line into simple commands.

    Splits on ; & | newlines, and on $( ) and backticks so a command inside a
    substitution is checked too. Quotes are respected, except that a
    substitution inside double quotes still splits.
    """
    segs, cur, quote, i = [], [], None, 0
    while i < len(cmd):
        c = cmd[i]
        if quote == "'":
            cur.append(c)
            if c == "'":
                quote = None
        elif c == "\\" and i + 1 < len(cmd):
            cur.append(c)
            cur.append(cmd[i + 1])
            i += 1
        elif c == "$" and cmd[i + 1:i + 2] == "(":
            cur.append(SUBSTITUTION)
            segs.append("".join(cur))
            cur = []
            i += 1
        elif c == "`":
            cur.append(SUBSTITUTION)
            segs.append("".join(cur))
            cur = []
        elif quote == '"':
            cur.append(c)
            if c == '"':
                quote = None
        elif c in "'\"":
            quote = c
            cur.append(c)
        elif c in ";|&\n()":
            segs.append("".join(cur))
            cur = []
        else:
            cur.append(c)
        i += 1
    segs.append("".join(cur))
    return [s for s in (x.strip() for x in segs) if s]


class Word(str):
    """A shell word. dynamic is True when the shell would put something into
    it that the command text does not show: a variable, a substitution, or
    a $'...' string, whose escapes the shell decodes."""
    dynamic = False


class Argv(list):
    """The words of one simple command. unparsed is True when the quoting
    could not be followed, so the words are a guess."""
    unparsed = False


def shell_words(segment):
    """Split one simple command into words as a POSIX shell would, noting
    which words hold an expansion. Raises ValueError on an open quote."""
    words, cur, started, dynamic, quote, i = [], [], False, False, None, 0

    def flush():
        w = Word("".join(cur))
        w.dynamic = dynamic
        words.append(w)

    while i < len(segment):
        c = segment[i]
        if quote == "'":
            if c == "'":
                quote = None
            else:
                cur.append(c)
        elif c == "\\" and i + 1 < len(segment) and (quote is None or segment[i + 1] in '"\\$`\n'):
            cur.append(segment[i + 1])
            started = True
            i += 1
        elif quote == '"' and c == '"':
            quote = None
        elif quote is None and c in " \t\r\n":
            if started:
                flush()
            cur, started, dynamic = [], False, False
        elif quote is None and c in "'\"":
            quote = c
            started = True
        else:
            if c == SUBSTITUTION or (c == "$" and re.match(r"[A-Za-z_{(@*#?!$0-9'\"-]", segment[i + 1:i + 2] or " ")):
                dynamic = True
            cur.append(c)
            started = True
        i += 1
    if quote:
        raise ValueError("open quote")
    if started:
        flush()
    return words


def argv_of(segment):
    unparsed = False
    try:
        argv = shell_words(segment)
    except ValueError:
        unparsed = True
        argv = []
        for text in segment.split():
            w = Word(text)
            w.dynamic = True
            argv.append(w)
    argv = _strip_wrappers(argv)
    out = Argv(argv)
    out.unparsed = unparsed
    return out


def _strip_wrappers(argv):
    # Drop leading VAR=value words and wrappers that run another command.
    while argv:
        a = argv[0]
        if re.match(r"^[A-Za-z_][A-Za-z0-9_]*=", a):
            argv = argv[1:]
        elif a in ("sudo", "command", "exec", "env", "nohup", "time", "then", "do", "else", "!"):
            argv = argv[1:]
            while a == "sudo" and argv and argv[0].startswith("-"):
                argv = argv[1:]
        else:
            break
    return argv


def is_dynamic(word):
    return getattr(word, "dynamic", False) or SUBSTITUTION in word


def commands_in(cmd, depth=0):
    """Yield the argv of every simple command, looking inside `sh -c '...'`."""
    for seg in split_segments(cmd):
        argv = argv_of(seg)
        if not argv:
            continue
        yield argv
        prog = os.path.basename(argv[0])
        if depth < 3 and prog in ("bash", "sh", "zsh", "dash") and "-c" in argv:
            i = argv.index("-c")
            if i + 1 < len(argv):
                for inner in commands_in(argv[i + 1], depth + 1):
                    yield inner
        if depth < 3 and prog == "eval" and len(argv) > 1:
            for inner in commands_in(" ".join(argv[1:]), depth + 1):
                yield inner


def git_sub(argv):
    """Return (subcommand, its args, the global options before it)."""
    i = 1
    while i < len(argv):
        a = argv[i]
        if a in GIT_OPTS_WITH_VALUE:
            i += 2
        elif a.startswith("-"):
            i += 1
        else:
            return a, argv[i + 1:], argv[1:i]
    return "", [], argv[1:]


def positionals(args):
    return [a for a in args if not a.startswith("-")]


def push_refs(args):
    """Every word of a `git push` that could be a refspec, without a leading
    "+". The remote's name is among them: which positional is the remote
    depends on options, so every one is checked against the patterns."""
    return [a.lstrip("+") for a in positionals(args)]


def check_git(role, argv):
    sub, args, global_opts = git_sub(argv)

    # Every role: no one-off configuration and no aliases. Either can turn a
    # harmless-looking command into a push or a merge the checks below miss.
    # -C (a directory) is the only global option that starts with -c or -C
    # and is allowed, so any other word starting with -c is configuration.
    # The options before the subcommand are held to a short known list. An
    # option this parser does not know could take the next word as its value,
    # and then the word it takes for the subcommand would be the wrong one.
    i = 0
    while i < len(global_opts):
        a = global_opts[i]
        if a in ("-C", "--git-dir", "--work-tree"):
            i += 2
            continue
        if a in GIT_GLOBAL_OK or a.startswith(("--git-dir=", "--work-tree=")):
            i += 1
            continue
        raise Block("the team uses git with only these options before the subcommand: -C, --git-dir, --work-tree, --no-pager, --paginate, --no-optional-locks (got %s)." % a)
    if not sub:
        return
    if sub == "config" and any(a.lower().startswith("alias.") for a in args):
        raise Block("the team does not define git aliases: they hide the command from the guard.")

    # Every role: the loop owns issue-N branches.
    new_branch = None
    if sub in ("checkout", "switch", "worktree"):
        for flag in ("-b", "-B", "-c", "-C"):
            if flag in args and args.index(flag) + 1 < len(args):
                new_branch = args[args.index(flag) + 1]
    elif sub == "branch":
        pos = positionals(args)
        if pos and not any(f in args for f in ("-d", "-D", "--delete", "-l", "--list")):
            new_branch = pos[0]
    if new_branch and LOOP_BRANCH.match(new_branch):
        raise Block("branches named issue-N belong to the nightly loop. Pick another name.")
    if sub == "push":
        for a in push_refs(args):
            if LOOP_BRANCH.match(a):
                raise Block("issue-N branches belong to the nightly loop. Local roles never push to them.")

    # Every role: tags are releases, and a release needs the owner's go-ahead
    # for that release (RELEASING.md). No standing delegation covers it.
    if sub == "tag":
        listing = not positionals(args) or any(f in args for f in ("-l", "--list", "-n", "--contains", "--points-at", "-v", "--verify"))
        if not listing:
            raise Block("creating or deleting a tag is a release step. The owner does it, or says so per release.")
    if sub == "push":
        if "--tags" in args or "--follow-tags" in args or "--mirror" in args:
            raise Block("pushing tags is a release step. The owner does it, or says so per release.")
        for a in push_refs(args):
            if TAG_REF.match(a):
                raise Block("pushing a tag is a release step. The owner does it, or says so per release.")

    if role == "director" and sub in GIT_HISTORY:
        raise Block("the director does not write code or history (git %s). Hand it to the dev role." % sub)
    if role == "tester" and sub in GIT_HISTORY:
        raise Block("the tester makes no git writes (git %s). Report results as a PR comment." % sub)
    if role == "dev" and sub == "push":
        # Forms that push more than the one branch named, or take the
        # destination from configuration the guard does not read.
        if any(a in ("--all", "--branches", "--mirror") for a in args) or any("*" in a for a in push_refs(args)):
            raise Block("the dev role pushes one named branch at a time.")
        for a in push_refs(args):
            if MAIN_REF.match(a):
                raise Block("the dev role does not push to main. Open a pull request.")


def gh_api_is_write(args):
    method = None
    has_fields = False
    i = 0
    while i < len(args):
        a = args[i]
        if a in ("-X", "--method") and i + 1 < len(args):
            method = args[i + 1].upper()
            i += 1
        elif a.startswith("--method="):
            method = a.split("=", 1)[1].upper()
        elif a.startswith("-X") and len(a) > 2:
            method = a[2:].upper()
        elif a in ("--field", "--raw-field", "--input") or a.startswith(("--field=", "--raw-field=", "--input=")):
            has_fields = True
        elif a[:2] in ("-f", "-F"):
            # -f key=value, and the joined form -fkey=value
            has_fields = True
        i += 1
    if method:
        return method != "GET"
    return has_fields


def gh_is_read(args):
    if not args:
        return True
    group = args[0]
    if group in GH_READ_GROUPS:
        return True
    if group == "api":
        return not gh_api_is_write(args[1:])
    if group == "auth":
        return len(args) > 1 and args[1] in ("status", "token")
    verb = args[1] if len(args) > 1 else ""
    return verb in GH_READ_VERBS


def begins_owner_decision(text):
    """True if the visible text starts with "Owner decision". Leading space,
    Markdown marks, punctuation and HTML comments do not hide it."""
    t = text[:65536]
    while t:
        if t.startswith("<!--"):
            end = t.find("-->")
            if end < 0:
                return False
            t = t[end + 3:]
            continue
        m = re.match(r"(?:[^\w<]|_)+|<(?!!--)", t)
        if not m:
            break
        t = t[m.end():]
    return OWNER_DECISION.match(t) is not None


class Context:
    """What a body check needs to know about the whole Bash call."""

    def __init__(self, command="", cwd="", commands=()):
        self.command = command
        self.cwd = cwd or os.getcwd()
        self.commands = list(commands)
        self.changes_dir = any(os.path.basename(argv[0]) in ("cd", "pushd", "popd") for argv in self.commands)


def has_output_redirect(cmd):
    """True if the command line has a > outside quotes."""
    quote, i = None, 0
    while i < len(cmd):
        c = cmd[i]
        if quote == "'":
            if c == "'":
                quote = None
        elif c == "\\":
            i += 1
        elif quote == '"':
            if c == '"':
                quote = None
        elif c in "'\"":
            quote = c
        elif c == ">":
            return True
        i += 1
    return False


# gh commands that may share a Bash call with one that posts a body from a
# file: they write nothing to the local disk.
GH_NO_LOCAL_WRITE = {"issue": {"develop"}, "label": None, "api": None, "pr": {"checkout"}}


def writes_nothing_local(argv):
    prog = os.path.basename(argv[0])
    if prog == "cd":
        return True
    if prog != "gh":
        return False
    args = argv[1:]
    while args and args[0].startswith("-"):
        args = args[2:] if args[0] in ("-R", "--repo") else args[1:]
    if not args or args[0] not in GH_NO_LOCAL_WRITE:
        return False
    # Any later word, not only the next one: a flag can sit between the
    # group and the verb (gh pr --repo x checkout 5).
    barred = GH_NO_LOCAL_WRITE[args[0]]
    return not (barred and any(a in barred for a in args[1:]))


def read_body_file(word, path, ctx):
    """The text of a file a gh command would post. Blocks when the guard
    cannot be sure the text it reads is the text that will be posted."""
    if is_dynamic(word):
        raise Block("the path of this body file is built when the command runs (a variable or a substitution)." + BODY_HELP)
    if path == "-":
        raise Block("this body comes from standard input (a pipe, a redirect or a heredoc)." + BODY_HELP)
    full = os.path.expanduser(path)
    if not os.path.isabs(full):
        if ctx.changes_dir:
            raise Block("this body file has a relative path and the command changes directory." + BODY_HELP)
        full = os.path.join(ctx.cwd, full)
    # The guard reads the file now and gh reads it later. Anything else in
    # the same call could change it in between, under this name or another.
    # Inside $'...' a backslash escapes a quote, which the splitting above
    # does not follow, so a command could hide in what looks like a string.
    if "$'" in ctx.command or '$"' in ctx.command:
        raise Block("this command posts a body from a file and also holds a $'...' or $\"...\" string, which the guard cannot follow." + BODY_HELP + " Run the gh command on its own.")
    if has_output_redirect(ctx.command):
        raise Block("this command posts a body from a file and also redirects output to a file, so it may write the body first." + BODY_HELP + " Run the gh command on its own, with no > redirection.")
    for other in ctx.commands:
        if not writes_nothing_local(other):
            raise Block("this command posts a body from a file and also runs %s, which may write that file first." % os.path.basename(other[0]) + BODY_HELP + " Run the gh command on its own.")
    base = os.path.basename(full)
    if base and len(re.findall(r"(?<![\w.\-])" + re.escape(base) + r"(?![\w.\-])", ctx.command)) > 1:
        raise Block("the body file %s is named more than once in this command, so the command may write it before posting it." % base + BODY_HELP)
    try:
        with open(full, "r", errors="replace") as f:
            return f.read(1 << 20)
    except OSError:
        raise Block("the body file %s cannot be read before the command runs." % path + BODY_HELP)


def json_bodies(value, every=False):
    """Every string under a "body" key, at any depth. With every, all strings."""
    if isinstance(value, dict):
        for k, v in value.items():
            if isinstance(v, str):
                if every or k == "body":
                    yield v
            else:
                for x in json_bodies(v, every):
                    yield x
    elif isinstance(value, list):
        for v in value:
            if isinstance(v, str):
                if every:
                    yield v
            else:
                for x in json_bodies(v, every):
                    yield x


# Inside a GraphQL document a comment body is a string literal, "..." or
# """...""", and may spell a character as an escape.
GRAPHQL_LITERAL = re.compile(r"""["'](?:[^\w]|_)*owner[\W_]*decision""", re.IGNORECASE)


def graphql_unescape(text):
    """Decode the escapes a GraphQL string may hold, so an escaped letter
    cannot hide a word."""
    def code(m):
        try:
            return chr(int(m.group(1) or m.group(2), 16))
        except (ValueError, OverflowError):
            return " "
    text = re.sub(r"\\u(?:\{([0-9a-fA-F]+)\}|([0-9a-fA-F]{4}))", code, text)
    text = re.sub(r"\\[nrtbf]", " ", text)
    return re.sub(r"\\(.)", r"\1", text, flags=re.S)


def graphql_owner_decision(text):
    return GRAPHQL_LITERAL.search(graphql_unescape(text)) is not None


def api_bodies(args, ctx):
    """Yield the text of every field a `gh api` write would send."""
    graphql = any("graphql" in a.lower() for a in args[1:] if not a.startswith("-"))

    def field(word, kv, typed):
        if "=" not in kv:
            if is_dynamic(word):
                raise Block("a field of this gh api call is built when the command runs." + BODY_HELP)
            return
        key, val = kv.split("=", 1)
        is_body = graphql or key == "body" or key.endswith("[body]") or "$" in key or SUBSTITUTION in key
        if typed and val.startswith("@"):
            if is_body:
                yield read_body_file(word, val[1:], ctx)
            return
        if is_dynamic(word):
            if is_body:
                raise Block("the body field of this gh api call is built when the command runs (a variable or a substitution)." + BODY_HELP)
            return
        yield val

    def input_file(word, path):
        text = read_body_file(word, path, ctx)
        try:
            doc = json.loads(text)
        except ValueError:
            raise Block("the --input file %s is not JSON the guard can read." % path + BODY_HELP)
        for body in json_bodies(doc, every=graphql):
            yield body

    i = 0
    while i < len(args):
        a = args[i]
        nxt = args[i + 1] if i + 1 < len(args) else None
        found = ()
        # The word after a flag is checked as that flag's value and then, on
        # the next turn, as a flag of its own: gh may have given it to an
        # earlier flag this parser does not know, such as --jq.
        if a in ("-f", "-F", "--field", "--raw-field") and nxt is not None:
            found = field(nxt, nxt, a in ("-F", "--field"))
        elif a.startswith(("--field=", "--raw-field=")):
            found = field(a, a.split("=", 1)[1], a.startswith("--field="))
        elif a[:2] in ("-f", "-F") and len(a) > 2:
            found = field(a, a[2:], a[1] == "F")
        elif a == "--input" and nxt is not None:
            found = input_file(nxt, nxt)
        elif a.startswith("--input="):
            found = input_file(a, a.split("=", 1)[1])
        for body in found:
            if graphql and graphql_owner_decision(body):
                raise Block('only the owner posts a comment that begins "Owner decision". Draft it in a file for them.')
            yield body
        i += 1


def command_bodies(args, group, verb, ctx):
    """Yield the text of every body, comment and title a gh command other
    than `gh api` would post: --body, --body-file, --comment, --title and
    their short forms, joined or in a cluster (-b text, -btext, -sdb text).
    --comment and -c take text only on `close`: on `pr review` they are a
    switch, and the word after them is not their value."""
    long_text = ("--body", "--title") + (("--comment",) if verb == "close" else ())
    short_text = "bt" + ("c" if verb == "close" else "")
    # -F is a body file on pr and issue commands. Elsewhere (gh workflow run)
    # it is a field, and no comment is posted from there.
    if group in ("pr", "issue"):
        short_text += "F"
    # The word after a flag is checked as that flag's value and then, on the
    # next turn, as a flag of its own. gh may have given it to an earlier flag
    # this parser does not know (--subject --body -F FILE posts FILE), so no
    # word is skipped.

    def inline(word, text):
        if is_dynamic(word):
            raise Block("this body is built when the command runs (a variable or a substitution)." + BODY_HELP)
        return text

    i = 0
    while i < len(args):
        a = args[i]
        nxt = args[i + 1] if i + 1 < len(args) else None
        if a == "--":
            break
        if a in long_text and nxt is not None:
            yield inline(nxt, nxt)
        elif a.startswith(tuple(f + "=" for f in long_text)):
            yield inline(a, a.split("=", 1)[1])
        elif a == "--body-file" and nxt is not None:
            yield read_body_file(nxt, nxt, ctx)
        elif a.startswith("--body-file="):
            yield read_body_file(a, a.split("=", 1)[1], ctx)
        elif re.match(r"^-[A-Za-z]", a) and not a.startswith("--"):
            # A cluster of short flags. The first one that takes text takes
            # the rest of the word, or the next word, as its value.
            for j, ch in enumerate(a[1:]):
                if ch not in short_text:
                    continue
                joined = a[j + 2:]
                if joined:
                    word, val = a, joined
                elif nxt is not None:
                    word, val = nxt, nxt
                else:
                    break
                yield read_body_file(word, val, ctx) if ch == "F" else inline(word, val)
                break
        i += 1


# The owner's "approved", quoted: inside double quotes, straight or curly, or
# on a Markdown blockquote line. "not approved" and "disapproved" do not count.
QUOTED_APPROVED = re.compile(
    r"""(?:"[^"\n]*|\u201c[^\u201d\n]*|^[ \t]*>[^\n]*)(?<![\w-])(?<!not )approved\b""",
    re.IGNORECASE | re.MULTILINE)


def quotes_approved(text):
    return QUOTED_APPROVED.search(text) is not None


def check_bodies(role, argv, args, group, verb, ctx):
    """An "Owner decision" comment is the owner's. The one exception is the
    director, after the owner says "approved" in chat: it may post one whose
    body quotes that word (rule 3 of the owner's rules in DELEGATIONS.md).
    The guard can check that the quote is there. It cannot check that the
    owner said it. A body the guard cannot read is refused for every role."""
    if getattr(argv, "unparsed", False):
        raise Block("the guard cannot follow the quoting of this gh command, most likely a substitution inside quotes." + BODY_HELP)
    bodies = api_bodies(args, ctx) if group == "api" else command_bodies(args, group, verb, ctx)
    for body in bodies:
        if not begins_owner_decision(body or ""):
            continue
        if role != "director":
            raise Block('only the owner posts a comment that begins "Owner decision". Draft it in a file for them.')
        if not quotes_approved(body):
            raise Block('the director posts a comment that begins "Owner decision" only after the owner says "approved" in chat, and the comment quotes the owner\'s words: put them in double quotes, with the word approved in them. Otherwise draft it in a file for the owner.')


def label_values(args, flags):
    out = []
    for i, a in enumerate(args):
        for flag in flags:
            if a == flag and i + 1 < len(args):
                out += args[i + 1].split(",")
            elif a.startswith(flag + "="):
                out += a.split("=", 1)[1].split(",")
    return [x.strip() for x in out]


def check_gh(role, argv, ctx=None):
    ctx = ctx or Context()
    args = argv[1:]
    # Skip gh's own global flags before the command group.
    while args and args[0].startswith("-"):
        args = args[2:] if args[0] in ("-R", "--repo") else args[1:]
    if not args:
        return
    group = args[0]
    verb = args[1] if len(args) > 1 else ""
    rest = args[2:]
    read = gh_is_read(args)

    # Only the owner writes "Owner decision", and the director for the owner.
    if not read:
        check_bodies(role, argv, args, group, verb, ctx)

    # Every role: an alias can hide any other gh command from this check.
    if group == "alias" and verb in ("set", "import", "delete"):
        raise Block("gh aliases are not used by the team: they hide the command from the guard.")

    # Every role: releases and release approvals are the owner's.
    if group == "release" and not read:
        raise Block("changing a release is the owner's step, or theirs to grant per release.")
    if group == "api" and not read and any("pending_deployments" in a for a in args):
        raise Block("approving a deployment is the owner's step, or theirs to grant per release.")

    if role == "tester":
        if read or (group == "pr" and verb == "comment"):
            return
        raise Block("the tester's only gh write is `gh pr comment` with live-check results (gh %s %s)." % (group, verb))

    if role == "dev":
        if group == "pr" and verb == "merge":
            raise Block("the dev role does not merge. The director merges under DELEGATIONS.md.")
        if group == "api" and any("graphql" in a.lower() for a in args[1:] if not a.startswith("-")):
            # Telling a query from a mutation means parsing GraphQL and every
            # way gh takes a field. The dev role does not need the endpoint.
            raise Block("the dev role does not use the GraphQL API: a mutation can merge, label and set milestones. Use the gh commands or the REST API.")
        if group == "api" and not read:
            if any(re.search(r"/pulls/\d+/merge\b", a) for a in args):
                raise Block("the dev role does not merge. The director merges under DELEGATIONS.md.")
            for a in args:
                if re.match(r"^milestone(\[\])?=", a):
                    raise Block("milestones are the director's.")
                m = re.match(r"^labels(\[\])?=(.*)$", a)
                if m and ROUTING_LABEL.match(m.group(2)):
                    raise Block("routing labels (agent:ready, needs:human, risk:*) are the director's.")
                if re.search(r"/labels/(agent(:|%3A)ready|needs(:|%3A)human|risk(:|%3A))", a):
                    raise Block("routing labels (agent:ready, needs:human, risk:*) are the director's.")
        if group in ("issue", "pr") and verb == "edit":
            if any(a in ("-m", "--milestone", "--remove-milestone") or a.startswith("--milestone=") for a in rest):
                raise Block("milestones are the director's.")
            for label in label_values(rest, ("--add-label", "--remove-label")):
                if ROUTING_LABEL.match(label):
                    raise Block("routing labels (agent:ready, needs:human, risk:*) are the director's. You may set agent:in-progress to claim.")


def under(path, root):
    path, root = os.path.realpath(path), os.path.realpath(root)
    return path == root or path.startswith(root.rstrip(os.sep) + os.sep)


def in_git_checkout(path):
    """True if path lies inside a git work tree, wherever that tree is."""
    d = os.path.dirname(path)
    while True:
        if os.path.exists(os.path.join(d, ".git")):
            return True
        parent = os.path.dirname(d)
        if parent == d:
            return False
        d = parent


def is_claude_config(path):
    """True for the files that configure a Claude Code session: settings and
    hooks. A role that could write them could switch this guard off."""
    # Compared without case: macOS file systems usually ignore it.
    parts = [p.casefold() for p in path.split(os.sep)]
    for i, part in enumerate(parts):
        if part != ".claude":
            continue
        after = parts[i + 1:]
        if len(after) == 1 and after[0].endswith(".json"):
            return True
        if after and after[0] in ("hooks", "plugins", "agents", "commands", "skills"):
            return True
    return False


def check_file(role, path, team_dir):
    if not path:
        return
    path = os.path.realpath(os.path.expanduser(path))
    if team_dir:
        for name in TEAM_FIXED:
            if under(path, os.path.join(team_dir, name)):
                raise Block("the running team's own files change by a pull request the owner merges, not in place (%s)." % name)
    if is_claude_config(path):
        raise Block("no role edits Claude Code settings, hooks or plugins. Ask the owner.")
    if role == "director":
        if team_dir and path == os.path.realpath(os.path.join(team_dir, "HANDOFF-director.md")):
            return
        # A file in a checkout is a repository file even when the checkout
        # sits under a temp directory.
        if in_git_checkout(path):
            raise Block("the director does not write code or repository files. It may write its handoff and scratch files.")
        allowed = [tempfile.gettempdir(), "/tmp", "/private/tmp", os.path.expanduser("~/.claude/projects")]
        if os.environ.get("TMPDIR"):
            allowed.append(os.environ["TMPDIR"])
        if any(under(path, a) for a in allowed):
            return
        raise Block("the director does not write code or repository files. It may write its handoff and scratch files.")


def decide(role, event, team_dir):
    tool = event.get("tool_name", "")
    tin = event.get("tool_input") or {}
    if role not in ROLES:
        raise Block("unknown VIADUCT_ROLE %r. Expected one of %s." % (role, ", ".join(ROLES)))
    if tool == "Bash":
        command = tin.get("command", "")
        commands = list(commands_in(command))
        ctx = Context(command, event.get("cwd", ""), commands)
        for argv in commands:
            prog = os.path.basename(argv[0])
            if prog == "git":
                check_git(role, argv)
            elif prog == "gh":
                check_gh(role, argv, ctx)
    elif tool in ("Edit", "Write", "NotebookEdit", "MultiEdit"):
        check_file(role, tin.get("file_path") or tin.get("notebook_path"), team_dir)


def main():
    role = os.environ.get("VIADUCT_ROLE", "")
    if not role:
        return 0
    try:
        event = json.load(sys.stdin)
    except ValueError:
        sys.stderr.write("viaduct team guard: could not read the hook input. Blocking.\n")
        return 2
    try:
        decide(role, event, os.environ.get("VIADUCT_TEAM_DIR", ""))
    except Block as b:
        sys.stderr.write("viaduct team guard (role %s): %s See .claude/team/PROTOCOL.md.\n" % (role, b))
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())

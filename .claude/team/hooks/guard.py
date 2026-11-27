#!/usr/bin/env python3
"""Role limits for the viaduct agent team. Run by guard.sh as a PreToolUse hook.

Reads the hook's JSON on stdin. Exit 0 allows the tool call. Exit 2 blocks it
and the text on stderr is shown to the agent.

This stops a role from doing another role's work by mistake. It reads command
text, so it is not a security boundary: a command hidden in a script file or
built at run time is not seen. The rules are in ../PROTOCOL.md.

Works on Python 3.8 and later (macOS ships 3.9).
"""
import json
import os
import re
import shlex
import sys
import tempfile

ROLES = ("director", "dev", "tester")
ROUTING_LABEL = re.compile(r"^(agent:ready|needs:human|risk:.+)$")
LOOP_BRANCH = re.compile(r"^(?:.*[:/])?issue-\d+$")
MAIN_REF = re.compile(r"^(?:.*[:/])?(main|master)$")
TAG_REF = re.compile(r"^(?:.*:)?(?:refs/tags/.+|v\d[\w.\-]*)$")
OWNER_DECISION = re.compile(r"^\s*owner decision", re.IGNORECASE)

# Files of the running team that no role edits. They change by a pull request
# the owner merges, through a worktree, not in place.
TEAM_FIXED = ("hooks", "backend", ".run", "start", "roles.toml", "DELEGATIONS.md", "PROTOCOL.md", "prompts")

GIT_HISTORY = {"commit", "push", "merge", "rebase", "cherry-pick", "revert", "am"}
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
            segs.append("".join(cur))
            cur = []
            i += 1
        elif c == "`":
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


def argv_of(segment):
    try:
        argv = shlex.split(segment, comments=False)
    except ValueError:
        argv = segment.split()
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
    for a in global_opts:
        if a.startswith("-c") or a == "--config-env" or a.startswith("--config-env="):
            raise Block("the team does not pass git configuration on the command line (-c, --config-env).")
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
        elif a in ("-f", "-F", "--field", "--raw-field", "--input") or a.startswith(("--field=", "--raw-field=", "--input=")):
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


def body_values(args):
    """Yield the text of each body an gh command would post."""
    i = 0
    while i < len(args):
        a = args[i]
        nxt = args[i + 1] if i + 1 < len(args) else None
        if a in ("-b", "--body", "-t", "--title") and nxt is not None:
            yield nxt
        elif a.startswith("--body="):
            yield a.split("=", 1)[1]
        elif a in ("-F", "--body-file") and nxt is not None and "=" not in nxt:
            yield read_start(nxt)
        elif a.startswith("--body-file="):
            yield read_start(a.split("=", 1)[1])
        elif a in ("-f", "-F", "--field", "--raw-field") and nxt is not None and "=" in nxt:
            key, val = nxt.split("=", 1)
            if key == "body":
                yield read_start(val[1:]) if (val.startswith("@") and a in ("-F", "--field")) else val
        i += 1


def read_start(path):
    try:
        with open(os.path.expanduser(path), "r", errors="replace") as f:
            return f.read(200)
    except OSError:
        return ""


def label_values(args, flags):
    out = []
    for i, a in enumerate(args):
        for flag in flags:
            if a == flag and i + 1 < len(args):
                out += args[i + 1].split(",")
            elif a.startswith(flag + "="):
                out += a.split("=", 1)[1].split(",")
    return [x.strip() for x in out]


def check_gh(role, argv):
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

    # Every role: only the owner writes "Owner decision".
    if not read:
        for body in body_values(args):
            if OWNER_DECISION.match(body or ""):
                raise Block('only the owner posts a comment that begins "Owner decision". Draft it in a file for them.')

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
        for argv in commands_in(tin.get("command", "")):
            prog = os.path.basename(argv[0])
            if prog == "git":
                check_git(role, argv)
            elif prog == "gh":
                check_gh(role, argv)
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

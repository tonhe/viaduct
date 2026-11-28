# Agent team launcher

One command per host brings back the local viaduct working set after a reboot: a director and a dev on the Linux host, a tester on the Mac. Each is a fresh Claude Code session with Remote Control on, started in its role.

This is separate from the nightly loop in CI. Nothing here is read by the CI agents.

## Start

```
.claude/team/start
```

It starts every role in `roles.toml` whose `host` matches this machine (`mac` on macOS, `linux` otherwise). A session that is already running is left alone, so it is safe to run again.

```
.claude/team/start --attach director    start, then open the director's session
.claude/team/start --restart            stop this host's roles, then start them
.claude/team/start --backend tmux       use tmux in place of herdr
.claude/team/start --dry-run            print what would happen
```

`VIADUCT_TEAM_BACKEND=tmux` in the environment does the same as `--backend tmux`. `VIADUCT_HERDR_SESSION=name` puts the workspaces in a named herdr session; without it they go in the default one. If no herdr server is running, the launcher starts one headless.

Sessions run with `--dangerously-skip-permissions`. What keeps a role in its lane is the guard hook, the prompts, and `DELEGATIONS.md`.

## Stop

```
.claude/team/start --stop
```

Stops this host's roles. Ask each role to update its handoff first: sessions are not resumed, and `HANDOFF-<role>.md` is what the next one reads.

## What each file is

| File | What it is |
|---|---|
| `roles.toml` | The roles: session name, role, host, directory |
| `start` | The launcher |
| `backend/*.sh` | One file per multiplexer |
| `prompts/<role>.md` | What a role reads first |
| `PROTOCOL.md` | Roles, claim rules, how to stay clear of the nightly loop |
| `DELEGATIONS.md` | What the owner has approved in advance |
| `hooks/guard.sh`, `hooks/guard.py` | The hook that blocks what a role must not do |
| `hooks/session-start.sh` | The hook that tells a session its role again after `/clear`, a compaction or a resume |
| `tests/guard_test.sh` | Tests for the guard hook |
| `tests/start_test.sh` | Tests for the settings `start` writes and for the session-start hook |
| `HANDOFF-<role>.md` | Each role's notes for its next session. Not committed. |
| `.run/` | Files the launcher writes at start. Not committed. |

## How a role is identified

The launcher sets `VIADUCT_ROLE` in the session's environment. The guard reads that, not the session's name. The role's instructions arrive in the launch prompt, which `/clear`, an automatic compaction or a resume takes out of the session's context. The session-start hook puts them back: on those three it tells the session to read its prompt, `PROTOCOL.md`, `DELEGATIONS.md` and its handoff again. Running sessions get the hook on the next `start --restart`. It is loaded through `claude --settings`, from a file the launcher writes into `.run/`, so no settings file in the repository mentions it. A session with no `VIADUCT_ROLE` is never checked.

The guard reads command text. It stops mistakes. It is not a sandbox. It does not see a request made by another program (`curl`, a script file, an MCP tool), and it cannot tell when a file is changed by something the command text does not show.

## Add a role

1. Add a `[[role]]` table to `roles.toml`.
2. Add `prompts/<role>.md`.
3. If the role is new (not director, dev or tester), add it to `ROLES` in `hooks/guard.py` with its limits, and add cases to `tests/guard_test.sh`. An unknown role is blocked from everything.
4. Add its row to `PROTOCOL.md` and its entries to `DELEGATIONS.md`.

## Add a backend

Add `backend/<name>.sh` defining six functions. `start` sources the file and calls nothing else.

| Function | Does |
|---|---|
| `be_check` | Return non-zero with a message on stderr if the multiplexer is not usable |
| `be_exists NAME` | Return 0 if a session called NAME is running |
| `be_start NAME CWD CMD` | Start a session called NAME in CWD and run the shell command CMD in it |
| `be_stop NAME` | Stop that session |
| `be_list` | Print the names of running sessions, one per line |
| `be_attach NAME` | Put the user in that session (may `exec`) |

Keep it to bash 3.2, which is what macOS ships.

## Test

```
.claude/team/tests/guard_test.sh
.claude/team/tests/start_test.sh
```

# herdr backend for .claude/team/start. Sourced, not run.
# One herdr workspace per role, labelled with the role's session name.
#
# VIADUCT_HERDR_SESSION picks a named herdr session. Unset, the default
# session is used. A server that is not running is started headless, so
# `start` works right after a reboot with no herdr window open.

_herdr() {
  if [ -n "${VIADUCT_HERDR_SESSION:-}" ]; then
    herdr --session "$VIADUCT_HERDR_SESSION" "$@"
  else
    herdr "$@"
  fi
}

# _herdr_json WHAT [LABEL]: read one herdr JSON reply on stdin and print
#   pane          the root pane id of a `workspace create` reply
#   labels        every workspace label of a `workspace list` reply
#   ws-id LABEL   the id of the first workspace with that label
_herdr_json() {
  python3 -c '
import json, sys
r = json.load(sys.stdin).get("result", {})
what = sys.argv[1]
if what == "pane":
    print(r["root_pane"]["pane_id"])
elif what == "labels":
    for w in r.get("workspaces", []):
        print(w.get("label", ""))
elif what == "ws-id":
    for w in r.get("workspaces", []):
        if w.get("label") == sys.argv[2]:
            print(w["workspace_id"])
            break
' "$@"
}

be_check() {
  command -v herdr >/dev/null 2>&1 || {
    echo "backend herdr: herdr is not installed (https://herdr.dev), or use --backend tmux" >&2
    return 1
  }
  command -v python3 >/dev/null 2>&1 || {
    echo "backend herdr: python3 is needed to read herdr's JSON replies" >&2
    return 1
  }
  _herdr workspace list >/dev/null 2>&1 && return 0
  # No server: start one headless and wait for its socket.
  if [ -n "${VIADUCT_HERDR_SESSION:-}" ]; then
    nohup herdr --session "$VIADUCT_HERDR_SESSION" server >/dev/null 2>&1 &
  else
    nohup herdr server >/dev/null 2>&1 &
  fi
  local i=0
  while [ "$i" -lt 20 ]; do
    _herdr workspace list >/dev/null 2>&1 && return 0
    sleep 0.5
    i=$((i + 1))
  done
  echo "backend herdr: the herdr server did not start" >&2
  return 1
}

# _herdr_ws NAME: print the id of the workspace labelled NAME, if any.
_herdr_ws() {
  _herdr workspace list 2>/dev/null | _herdr_json ws-id "$1"
}

be_exists() { [ -n "$(_herdr_ws "$1")" ]; }

be_start() { # NAME CWD CMD
  local out pane
  out=$(_herdr workspace create --cwd "$2" --label "$1" --no-focus) || return 1
  pane=$(printf '%s' "$out" | _herdr_json pane)
  [ -n "$pane" ] || { echo "backend herdr: no pane id in the reply for $1" >&2; return 1; }
  _herdr pane run "$pane" "$3" >/dev/null
}

be_stop() {
  local ws
  ws=$(_herdr_ws "$1")
  [ -n "$ws" ] || return 0
  _herdr workspace close "$ws" >/dev/null
}

be_list() {
  _herdr workspace list 2>/dev/null | _herdr_json labels
}

be_attach() {
  local ws
  ws=$(_herdr_ws "$1")
  [ -n "$ws" ] && _herdr workspace focus "$ws" >/dev/null
  if [ -n "${VIADUCT_HERDR_SESSION:-}" ]; then
    exec herdr session attach "$VIADUCT_HERDR_SESSION"
  fi
  exec herdr
}

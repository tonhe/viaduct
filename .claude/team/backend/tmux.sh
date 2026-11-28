# tmux backend for .claude/team/start. Sourced, not run.
# One tmux session per role, named after the role's session name.

be_check() {
  command -v tmux >/dev/null 2>&1 || {
    echo "backend tmux: tmux is not installed" >&2
    return 1
  }
}

be_exists() { tmux has-session -t "=$1" 2>/dev/null; }

be_start() { # NAME CWD CMD
  tmux new-session -d -s "$1" -c "$2" || return 1
  tmux send-keys -t "=$1:" "$3" C-m
}

be_stop() { tmux kill-session -t "=$1"; }

be_list() { tmux list-sessions -F '#{session_name}' 2>/dev/null; }

be_attach() { exec tmux attach-session -t "=$1"; }

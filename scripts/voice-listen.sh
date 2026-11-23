#!/usr/bin/env bash
# voice-listen.sh — listen to pekoe voice (docs/voice.md P1) by ear, one step
# at a time, against a throwaway pekoed (scripts/sandbox.sh).
#
#   scripts/voice-listen.sh            # waits for Enter before each step
#   SILENT=1 scripts/voice-listen.sh   # no sound: a fake backend returns silent WAVs
#   AUTO=1 SILENT=1 scripts/voice-listen.sh   # no Enter, no second terminal (for CI-ish runs)
#   LOG_OUT=/path/voice.log scripts/voice-listen.sh   # keep a copy of voice.log
#
# Three sessions (feat/a, feat/b, feat/c) run scripts/screenshot/fake-agent.sh,
# so no real agent runs. Their states are moved by hand with `pekoe event`
# (fake hooks). Each step prints what you should hear, does it, and shows
# what voice.log recorded. Steps that need the TUI print what to type in a
# second terminal. With AUTO=1 the TUI runs in a tmux server of its own
# (pekoe-vl-<id>) and the keys (^q m, :voice) are sent to it.
#
# Before anything, the script checks it talks only to the sandbox: the
# daemon, config.toml, tmux server and worktrees all under the sandbox's
# temp dir. The real daemon (`tmux -L pekoe`, ~/.local/share/pekoe) and the
# real ~/.config/pekoe/config.toml are never touched.
set -euo pipefail
S=$(cd "$(dirname "$0")" && pwd)

if [ -z "${VOICE_LISTEN_INNER:-}" ]; then
	# an agent's window: its session must not leak into the sandbox's pekoe
	unset PEKOE_SESSION
	VOICE_LISTEN_INNER=1 exec "$S/sandbox.sh" bash "$S/voice-listen.sh" "$@"
fi

AUTO=${AUTO:-}
SILENT=${SILENT:-}
B=$'\e[1m'; D=$'\e[2m'; C=$'\e[36m'; Y=$'\e[33m'; G=$'\e[32m'; RED=$'\e[31m'; R=$'\e[0m'
die() {
	printf '%svoice-listen: %s%s\n' "$RED" "$*" "$R" >&2
	# AUTO: show what the TUI shows, to see why
	if [ -n "${AUTO:-}" ] && [ -n "${TUI_SOCK:-}" ]; then tmux -L "$TUI_SOCK" capture-pane -p 2>/dev/null | sed 's/^/  | /' >&2 || true; fi
	exit 1
}

# --- 0. make sure this is the sandbox, and nothing real is in reach ---------
case "${PEKOE_TMUX_SOCKET:-}" in
pekoe-sb-?*) ;;
*) die "PEKOE_TMUX_SOCKET='${PEKOE_TMUX_SOCKET:-}' is not a sandbox's (pekoe-sb-*); run scripts/voice-listen.sh itself" ;;
esac
id=${PEKOE_TMUX_SOCKET#pekoe-sb-}
SB=/tmp/pekoe-sb.$id
[ -d "$SB" ] || die "no sandbox dir $SB"
[ "${PEKOE_HOME:-}" = "$SB/home" ] || die "PEKOE_HOME='${PEKOE_HOME:-}' is not $SB/home"
[ "${PEKOE_SOCK:-}" = "$SB/pekoed.sock" ] || die "PEKOE_SOCK='${PEKOE_SOCK:-}' is not $SB/pekoed.sock"
[ "${PEKOE_WORKTREES:-}" = "$SB/worktrees" ] || die "PEKOE_WORKTREES='${PEKOE_WORKTREES:-}' is not $SB/worktrees"
[ "$(command -v pekoe)" = "$SB/bin/pekoe" ] || die "pekoe is $(command -v pekoe), not the sandbox's $SB/bin/pekoe"
[ -z "${PEKOE_SESSION:-}" ] || die "PEKOE_SESSION is set"
CONF=$PEKOE_HOME/config/config.toml
REAL_CONF=$HOME/.config/pekoe/config.toml
REAL_DATA=$HOME/.local/share/pekoe
case "$CONF" in "$SB"/*) ;; *) die "config.toml $CONF is outside $SB" ;; esac
[ "$CONF" != "$REAL_CONF" ] || die "config.toml is the real one"
case "$PEKOE_SOCK" in "$REAL_DATA"/*) die "the socket is the real daemon's" ;; esac
[ -S "$PEKOE_SOCK" ] || die "the sandbox's daemon has no socket at $PEKOE_SOCK"
# the daemon answering on that socket is the one sandbox.sh started
pekoe daemon status >/dev/null 2>&1 || die "the sandbox's daemon does not answer"
VLOG=$PEKOE_HOME/share/voice.log
case "$VLOG" in "$SB"/*) ;; *) die "voice.log $VLOG is outside $SB" ;; esac
REAL_SUM=""
[ -f "$REAL_CONF" ] && REAL_SUM=$(cksum <"$REAL_CONF")
printf '%sisolated%s: %s · tmux -L %s · real config and daemon untouched\n' "$G" "$R" "$SB" "$PEKOE_TMUX_SOCKET"

TUI_SOCK=pekoe-vl-$id
cleanup_tui() { [ -n "$AUTO" ] && tmux -L "$TUI_SOCK" kill-server 2>/dev/null || true; }
trap cleanup_tui EXIT

# --- setup: config, a repo, three fake sessions -----------------------------
mkdir -p "$PEKOE_HOME/config" "$PEKOE_HOME/share"
backend_lines=""
if [ -n "$SILENT" ]; then
	# a backend that keeps the contract (docs/voice.md 10.3) and returns
	# silence, about as long as say would talk (16 kHz mono, ~60 ms a byte)
	SBE=$SB/silent-voice.sh
	cat >"$SBE" <<'EOF'
#!/bin/sh
le32() { n=$1; for _ in 1 2 3 4; do printf "\\$(printf %03o $((n % 256)))"; n=$((n / 256)); done; }
le16() { n=$1; for _ in 1 2; do printf "\\$(printf %03o $((n % 256)))"; n=$((n / 256)); done; }
case "$1" in
version) echo 1 ;;
check) exit 0 ;;
voices) echo '[{"name":"tapi","lang":"ja","label":"silence"},{"name":"tapi","lang":"en","label":"silence"},{"name":"reader","lang":"ja","label":"silence"},{"name":"reader","lang":"en","label":"silence"}]' ;;
synth)
	out=""
	while [ $# -gt 0 ]; do [ "$1" = --out ] && out=$2; shift; done
	[ -n "$out" ] || { echo "no --out" >&2; exit 2; }
	len=$(wc -c | tr -d ' ')
	data=$(( (len * 1920 / 2) * 2 ))
	{
		printf RIFF; le32 $((36 + data)); printf WAVEfmt' '
		le32 16; le16 1; le16 1; le32 16000; le32 32000; le16 2; le16 16
		printf data; le32 "$data"
		head -c "$data" /dev/zero
	} >"$out"
	;;
*) echo "unknown command $1" >&2; exit 2 ;;
esac
EOF
	chmod 755 "$SBE"
	backend_lines=$(printf 'backend = "silent"\nfallback = ""\n[voice.backends]\nsilent = "%s"\n' "$SBE")
fi
cat >"$CONF" <<EOF
[tips]
enable = false
[mascot]
splash = false
[agents.claude]   # the fake agent stands in for claude: no real agent runs
command = "$S/screenshot/fake-agent.sh"
prompt = "arg"
hooks = true
[voice]
enable = true
lang = "ja"
$backend_lines
EOF
# the first-run tutorial would cover the TUI (and voice.focus says "" under it)
printf '{"seen_at":"2026-01-01T00:00:00Z"}\n' >"$PEKOE_HOME/share/tutorial.json"
pekoe daemon restart >&2

REPO=$SB/repo
mkdir -p "$REPO"
cd "$REPO"
git init -q -b main
printf '# voice-listen\n' >README.md
git add -A && git -c user.name=demo -c user.email=demo@example.com commit -qm init
for b in feat/a feat/b feat/c; do pekoe --json new "$b" >/dev/null; done

# wait until all three report their fake agent's SessionStart
for _ in $(seq 1 60); do
	n=$(pekoe --json ls | grep -o '"state": *"[a-z]*"' | grep -cv 'creating\|queued' || true)
	[ "$n" -ge 3 ] && break
	sleep 0.5
done
pekoe ls

# ev <slug> <Hook> [json-fields]: a fake hook from that session's agent
ev() {
	printf '{"session_id":"vl-%s","cwd":"%s","hook_event_name":"%s"%s}' "$1" "$PEKOE_WORKTREES" "$2" "${3:+,$3}" |
		PEKOE_SESSION=$1 pekoe event "$2"
}
log_lines() { [ -f "$VLOG" ] && wc -l <"$VLOG" | tr -d ' ' || echo 0; }
# show_log <from-line> <want>: wait (12 s at most) for <want> new lines in
# voice.log and print them
show_log() {
	local from=$1 want=$2 i
	for i in $(seq 1 48); do
		[ $(($(log_lines) - from)) -ge "$want" ] && break
		sleep 0.25
	done
	printf '%svoice.log:%s\n' "$D" "$R"
	if [ "$(log_lines)" -gt "$from" ]; then
		tail -n +$((from + 1)) "$VLOG" | sed 's/^/  /'
	else
		echo "  (nothing new)"
	fi
}
step_n=0
step() {
	step_n=$((step_n + 1))
	printf '\n%s━━ %d. %s%s\n' "$B" "$step_n" "$1" "$R"
}
hear() { printf '%s聞こえるはず:%s %s\n' "$Y" "$R" "$*"; }
do_() { printf '%s%s%s\n' "$C" "$*" "$R"; }
pause() {
	if [ -n "$AUTO" ]; then return; fi
	printf '%s[Enter] %s%s' "$D" "${1:-次へ}" "$R"
	read -r _ </dev/tty
}
voice_json() { pekoe --json voice; }
field() { voice_json | sed -n "s/.*\"$1\": *\"\\([^\"]*\\)\".*/\\1/p" | head -1; }

echo
pekoe voice
printf '%s使う backend: %s%s\n' "$D" "$([ -n "$SILENT" ] && echo 'silent(無音の偽物)' || echo 'say(同梱)')" "$R"
pause "始める"

# --- 1. wait -----------------------------------------------------------------
step "確認待ちを喋る"
hear "「b、確認待ちだよ」"
do_ "feat-b が許可を求める(Notification permission_prompt)"
pause "送る"
L=$(log_lines)
ev feat-b Notification '"notification_type":"permission_prompt","message":"needs permission"'
show_log "$L" 1
pause

# --- 2. two dones merge --------------------------------------------------------
step "2 つの done がまとまる"
hear "「bとcが終わったよ」(1 回だけ)。その 5 秒ほど後に「みんな手が空いたよ」"
do_ "feat-b と feat-c をいったん running にし(喋らない)、続けて 2 つとも Stop"
pause "送る"
L=$(log_lines)
ev feat-b UserPromptSubmit '"prompt":"go"'
ev feat-c UserPromptSubmit '"prompt":"go"'
sleep 1
ev feat-b Stop
ev feat-c Stop
sleep 6
show_log "$L" 3
printf '%sdone が 2 件: 片方が spoken で「bとcが終わったよ」、もう片方が merged ならまとまっている%s\n' "$D" "$R"
pause

# --- 3. the TUI and the session you look at ---------------------------------------
step "TUI で見ているセッションは喋らない"
if [ -n "$AUTO" ]; then
	do_ "TUI を tmux -L $TUI_SOCK で開く(feat/a を選ぶ)"
	tmux -L "$TUI_SOCK" -f /dev/null new-session -d -x 120 -y 40 \
		"env PEKOE_HOME='$PEKOE_HOME' PEKOE_SOCK='$PEKOE_SOCK' PEKOE_WORKTREES='$PEKOE_WORKTREES' PEKOE_TMUX_SOCKET='$PEKOE_TMUX_SOCKET' PATH='$PATH' pekoe"
	sleep 3
else
	printf '別の端末で、次を貼り付けて TUI を開き、一覧で %sfeat/a%s を選ぶ(右に feat/a の画面が出る)。選んだら、ここに戻って Enter:\n\n' "$B" "$R"
	cat <<EOF
  export PEKOE_HOME='$PEKOE_HOME' PEKOE_SOCK='$PEKOE_SOCK' PEKOE_WORKTREES='$PEKOE_WORKTREES' PEKOE_TMUX_SOCKET='$PEKOE_TMUX_SOCKET' PATH='$SB/bin':"\$PATH"
  unset PEKOE_SESSION TMUX
  pekoe

EOF
	pause "TUI で feat/a を選んだ"
fi
# what the TUI shows is what voice.focus says
for _ in $(seq 1 20); do
	f=$(field focus)
	[ "$f" = feat-a ] && break
	if [ -n "$AUTO" ] && [ "$f" != feat-a ]; then
		tmux -L "$TUI_SOCK" send-keys j 2>/dev/null || true
		sleep 0.5
		continue
	fi
	printf '%sTUI が見ているのは "%s"(feat-a ではない)。TUI で feat/a を選んでから Enter%s\n' "$RED" "$f" "$R"
	pause "選び直した"
done
[ "$(field focus)" = feat-a ] || die "the TUI does not show feat-a (voice.focus: '$(field focus)')"
printf '%sTUI が見ているセッション: feat-a%s\n' "$G" "$R"
hear "何も(feat-a は TUI で見ているので喋らない)"
do_ "feat-a が許可を求める"
if [ -z "$AUTO" ]; then
	printf '%s喋らないのは TUI の端末にフォーカスがある間だけ。Enter の後 5 秒待ってから送るので、その間に TUI の端末をクリックして前に出す%s\n' "$D" "$R"
	pause "送る(押したら TUI の端末へ)"
	sleep 5
fi
L=$(log_lines)
ev feat-a Notification '"notification_type":"permission_prompt","message":"x"'
sleep 3
show_log "$L" 0
printf '%s見ているセッションの wait は積まれないので、voice.log にも何も増えなければよい%s\n' "$D" "$R"
pause

hear "「a、確認待ちだよ」(端末にフォーカスが無ければ、見ているセッションでも喋る)"
if [ -n "$AUTO" ]; then
	do_ "TUI の端末からフォーカスを外す(focus-out を送る)"
	tmux -L "$TUI_SOCK" send-keys -l $'\e[O'
	sleep 1
else
	do_ "TUI の端末からフォーカスを外し(このウィンドウなど、別のウィンドウを前に)、Enter"
	pause "フォーカスを外した"
fi
L=$(log_lines)
ev feat-a UserPromptSubmit '"prompt":"go"'
sleep 1
ev feat-a Notification '"notification_type":"permission_prompt","message":"x"'
show_log "$L" 1
# feat-a waits now: the TUI opened its permission modal, and a modal takes
# every key, ^q m too. esc ("later") closes it and keeps feat-a waiting.
if [ -n "$AUTO" ]; then
	tmux -L "$TUI_SOCK" send-keys -l $'\e[I'
	sleep 0.5
	tmux -L "$TUI_SOCK" send-keys Escape
	sleep 0.5
else
	do_ "TUI に feat/a の許可のモーダル(Permission)が出ていたら esc(later)で閉じる。開いたままだと次の ^q m が効かない"
fi
pause

# --- 4. ^q m while a long sentence is said; nothing while muted ---------------
step "長い文の途中で ^q m で止まる・ミュート中は喋らない"
LONG="これはミュートを確かめるための長い文です。途中で止まるはずです。止まらなければ失敗です。もう少し続けます。まだ続いています。"
hear "長い文が始まり、^q m を押したところで止まる"
if [ -z "$AUTO" ]; then
	do_ "Enter の後に長い文が鳴り始めたら、TUI の端末で ^q m(ctrl+q の後に m)。最下行に 'muted ^q m'、メッセージ行に 'voice muted · ^q m' が出る"
	pause "鳴らす"
fi
L=$(log_lines)
pekoe voice test "$LONG"
# voice.log says spoken when the sound starts; a stop in the middle is not
# logged, so whether it stopped is seen in what pekoe voice says is sounding
speaking_text() { voice_json | tr -d '\n' | sed -n 's/.*"speaking": *{[^}]*"text": *"\([^"]*\)".*/\1/p'; }
if [ -n "$(speaking_text)" ]; then
	printf '%s鳴っている: %s%s\n' "$D" "$(speaking_text)" "$R"
else
	printf '%s鳴っていない(もう終わった?)。長い文の途中で止める確かめにならないので、やり直すなら pekoe voice test を手で%s\n' "$RED" "$R"
fi
if [ -n "$AUTO" ]; then
	sleep 1
	do_ "TUI に ^q m を送る"
	tmux -L "$TUI_SOCK" send-keys C-q m
	sleep 1
else
	pause "^q m を押した"
fi
for _ in $(seq 1 10); do
	[ -n "$(field muted_until)" ] && break
	printf '%sまだミュートになっていない。TUI で ^q m を押してから Enter%s\n' "$RED" "$R"
	pause "押した"
	[ -n "$AUTO" ] && sleep 1
done
[ -n "$(field muted_until)" ] || die "voice is not muted"
printf '%sミュート中: %s%s\n' "$G" "$(field muted_until)" "$R"
sp=$(speaking_text)
if [ -z "$sp" ]; then
	printf '%s鳴っているもの: なし(止まった)%s\n' "$G" "$R"
else
	printf '%sまだ鳴っている: %s%s\n' "$RED" "$sp" "$R"
fi
show_log "$L" 1

hear "何も(ミュート中なので、c が確認待ちになっても喋らない)"
do_ "feat-c を running → 確認待ちに"
pause "送る"
L=$(log_lines)
ev feat-c UserPromptSubmit '"prompt":"go"'
sleep 1
ev feat-c Notification '"notification_type":"permission_prompt","message":"needs permission"'
show_log "$L" 1
printf '%sc の wait が muted なら喋っていない%s\n' "$D" "$R"

if [ -n "$AUTO" ]; then
	do_ "TUI に esc(feat/c の許可のモーダルを閉じる)と ^q m を送ってミュートを解く"
	tmux -L "$TUI_SOCK" send-keys Escape
	sleep 0.5
	tmux -L "$TUI_SOCK" send-keys C-q m
	sleep 1
else
	do_ "TUI に feat/c の許可のモーダルが出ていたら esc で閉じてから、もう一度 ^q m。メッセージ行に 'voice on · ^q m' が出る"
	pause "^q m を押した"
fi
for _ in $(seq 1 10); do
	[ -z "$(field muted_until)" ] && break
	printf '%sまだミュート中。TUI で ^q m を押してから Enter%s\n' "$RED" "$R"
	pause "押した"
	[ -n "$AUTO" ] && sleep 1
done
[ -z "$(field muted_until)" ] || die "voice is still muted"
printf '%sミュートを解いた%s\n' "$G" "$R"
pause

# --- 5. look back: :voice and pekoe voice log -----------------------------------
step ":voice と pekoe voice log で見返す"
if [ -n "$AUTO" ]; then
	# one key at a time: sent in one write, the TUI takes them as a paste
	for k in : v o i c e Enter; do
		tmux -L "$TUI_SOCK" send-keys "$k"
		sleep 0.2
	done
	sleep 1
	do_ "TUI の :voice(tmux capture-pane):"
	tmux -L "$TUI_SOCK" capture-pane -p | sed 's/^/  /'
	tmux -L "$TUI_SOCK" send-keys Escape
else
	do_ "TUI で :voice(: の後に voice と Enter)。上の発話が spoken / merged / muted で並ぶ。esc で閉じる"
fi
do_ "pekoe voice log(新しい方から):"
pekoe voice log -n 20 | sed 's/^/  /'
if [ -n "${LOG_OUT:-}" ]; then
	cp "$VLOG" "$LOG_OUT"
	printf '%svoice.log を %s に残した%s\n' "$D" "$LOG_OUT" "$R"
fi
pause

# --- 6. clean up ------------------------------------------------------------
step "片付け"
if [ -n "$REAL_SUM" ] && [ "$(cksum <"$REAL_CONF")" != "$REAL_SUM" ]; then
	printf '%s本物の %s が変わっている(このスクリプトは書いていない。別の何かが書いた)%s\n' "$RED" "$REAL_CONF" "$R"
fi
if [ -z "$AUTO" ]; then
	do_ "TUI の端末で q を押して閉じてから Enter。この後 sandbox.sh がデーモン・tmux サーバー・一時ディレクトリを片付ける"
	pause "TUI を閉じた"
fi
printf '%s終わり。sandbox を片付ける%s\n' "$G" "$R"

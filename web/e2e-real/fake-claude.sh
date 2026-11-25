#!/bin/sh
# 本物の serve の E2E のテスト用の実行エンジン。serve の PATH の先頭に claude の名前で置く。
# 対話の欄の起動（--permission-prompt-tool つき）だけに応じ、ほかの呼び出しは失敗で終える。
# 返事の文には「起動の通し番号・会話の指定・そのプロセスが受けた発言の通し番号」を入れ、画面から
# 同じプロセスか起動し直したかを見分けられるようにする。場面は発言の中の語で選ぶ。
case " $* " in *" --permission-prompt-tool "*) ;; *) exit 1 ;; esac
mode=--session-id
session=
prev=
for a in "$@"; do
  [ "$a" = "--resume" ] && mode=--resume
  case "$prev" in --session-id|--resume) session=$a ;; esac
  prev=$a
done
# 全文の口が読むセッションのファイルは、準備の段が渡す一時の設定のフォルダに書く。
transcripts="${CLAUDE_CONFIG_DIR:?}/projects/e2e-real"
mkdir -p "$transcripts"
n=1
while ! mkdir "$FAKE_CLAUDE_LOG/start.$n" 2>/dev/null; do n=$((n+1)); done
echo $$ > "$FAKE_CLAUDE_LOG/start.$n/pid"
m=0
total=0
# answered は確認への答えの行を待ち、受けた行を残して、返事を終える。
answered() {
  IFS= read -r reply || exit 0
  printf '%s\n' "$reply" >> "$FAKE_CLAUDE_LOG/start.$n/answers"
  speech "答えを受けた"
  total=$((total+1))
  printf '{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0.%s}\n' "$total"
}
init() { printf '%s\n' '{"type":"system","subtype":"init","session_id":"s"}'; }
speech() { printf '{"type":"assistant","message":{"content":[{"type":"text","text":"%s"}]}}\n' "$1"; }
while IFS= read -r line; do
  m=$((m+1))
  init
  speech "起動$n $mode 受信$m"
  # 受けた発言の行と返事の文を、会話のファイルへ足す。
  case "$line" in
    *全文を残さない*) ;;
    *)
      printf '%s\n' "$line" >> "$transcripts/$session.jsonl"
      printf '{"type":"assistant","message":{"content":[{"type":"text","text":"起動%s %s 受信%s"}]}}\n' "$n" "$mode" "$m" >> "$transcripts/$session.jsonl" ;;
  esac
  case "$line" in
    *子を残して待つ*)
      sleep 300 &
      echo $! > "$FAKE_CLAUDE_LOG/start.$n/child" ;;
    *途中で落ちる*) exit 1 ;;
    *許可を求める*)
      printf '%s\n' '{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"rm -rf build","description":"d"}}}'
      answered ;;
    *問いを出す*)
      printf '%s\n' '{"type":"control_request","request_id":"q1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","requires_user_interaction":true,"input":{"questions":[{"question":"どちらにする？","header":"h","options":[{"label":"案A","description":"a"},{"label":"案B","description":"b"}],"multiSelect":false}]}}}'
      answered ;;
    *)
      printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t'$m'","name":"Bash","input":{"command":"flywheel status"}}]}}'
      printf '%s\n' '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t'$m'","content":"ok"}]}}'
      total=$((total+1))
      printf '{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0.%s}\n' "$total" ;;
  esac
done

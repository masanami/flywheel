#!/bin/sh
# 本物の serve の E2E のテスト用の実行エンジン。serve の PATH の先頭に claude の名前で置く。
# 対話の欄の起動（--permission-prompt-tool つき）だけに応じ、ほかの呼び出しは失敗で終える。
# 返事の文には「起動の通し番号・会話の指定・そのプロセスが受けた発言の通し番号」を入れ、画面から
# 同じプロセスか起動し直したかを見分けられるようにする。場面は発言の中の語で選ぶ。
case " $* " in *" --permission-prompt-tool "*) ;; *) exit 1 ;; esac
mode=--session-id
for a in "$@"; do [ "$a" = "--resume" ] && mode=--resume; done
n=1
while ! mkdir "$FAKE_CLAUDE_LOG/start.$n" 2>/dev/null; do n=$((n+1)); done
echo $$ > "$FAKE_CLAUDE_LOG/start.$n/pid"
m=0
total=0
init() { printf '%s\n' '{"type":"system","subtype":"init","session_id":"s"}'; }
speech() { printf '{"type":"assistant","message":{"content":[{"type":"text","text":"%s"}]}}\n' "$1"; }
while IFS= read -r line; do
  m=$((m+1))
  init
  speech "起動$n $mode 受信$m"
  case "$line" in
    *子を残して待つ*)
      sleep 300 &
      echo $! > "$FAKE_CLAUDE_LOG/start.$n/child" ;;
    *途中で落ちる*) exit 1 ;;
    *許可を求める*)
      printf '%s\n' '{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"rm -rf build","description":"d"}}}' ;;
    *)
      printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t'$m'","name":"Bash","input":{"command":"flywheel status"}}]}}'
      printf '%s\n' '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t'$m'","content":"ok"}]}}'
      total=$((total+1))
      printf '{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0.%s}\n' "$total" ;;
  esac
done

# 対話の実行（`claude -p --output-format stream-json`）の出力の写し

実物の実行エンジン（claude 2.1.330）を 1 回ずつ起動して取った出力。形は実物のまま、会話に関係しない長い項目（`init` の道具・スキル・プラグインの一覧など）と環境の固有値は省いてある。

- 起動の引数: `-p --session-id <UUID> --output-format stream-json --verbose --max-turns <n> --max-budget-usd <額> --allowedTools Read Grep Glob Bash --permission-mode default`。2 通目は `--session-id` の代わりに `--resume <UUID>`。`--verbose` が無いと `stream-json` の出力は始まらない。発言の途中の断片を流す `--include-partial-messages` は付けていない。
- `chat_stream_success.jsonl`: 1 通目。道具（`Bash`）を 2 回実行し（1 回は成功、1 回は終了コード 127 で失敗）、最後に文で返す。
  - 会話の始まりの行: `type: system`・`subtype: init`（`session_id` を持つ）。
  - 発言: `type: assistant` の `message.content[]` の `type: text`。
  - コマンドの実行: `type: assistant` の `content[]` の `type: tool_use`（`name`・`input.command`・`id`）。その結果は `type: user` の `content[]` の `type: tool_result`（`tool_use_id`・`is_error`）。
  - 終わりの行: `type: result`（`subtype: success`・`is_error`・`num_turns`・`stop_reason`・`terminal_reason`・`total_cost_usd`）。
- `chat_stream_resume.jsonl`: 2 通目（`--resume`）。続きでも先頭に `system` / `init` の行が出る。
- `chat_stream_session_lost.jsonl`・`chat_stream_session_lost.stderr.txt`: 存在しない会話の識別を `--resume` したときの出力（終了コード 1）。標準出力は終わりの行 1 行だけ（`subtype: error_during_execution`・`is_error: true`・`errors` に `No conversation found with session ID: <UUID>`）、標準エラーにも同じ文言が出る。

## 会話の単位の起動（`--input-format stream-json` と `--permission-prompts host`）の出力の写し（claude 2.1.332）

対話の欄の作り替え（機能仕様 `docs/features/chat-session.md`）の土台として、実物の実行エンジンを 1 回ずつ起動して取った出力。会話の始まりの行は主な項目だけに絞り、`thinking` の中身と署名、`rate_limit_event`、`thinking_tokens` の行は省き、400 文字を超える文字列は切ってある。ホームのパス・ユーザー名・ワークスペースの名前は `/home/user`・`user`・`/work/example-workspace` に置き換えてある。取った場所は一時のワークスペース、モデルは `haiku`。

- 起動の引数（実物で受け付けられた形）: `claude -p --input-format stream-json --output-format stream-json --verbose --permission-prompts host --permission-prompt-tool stdio --model haiku --max-turns 6 (--session-id <UUID> | --resume <UUID>)`。許可の確認を実際に出すために `--setting-sources project --permission-mode default` を足した（利用者の設定で許可済みの操作は確認が出ない）。`--permission-prompts host` は `--permission-prompt-tool stdio` と併用できた。
- 標準入力の形は `chat_host_stdin.jsonl`（発言・許可・断り・問いへの答え）。発言は `{"type":"user","message":{"role":"user","content":"<文>"}}` の 1 行。確認への答えは、出力の `control_request` の `request_id` を使った `control_response`。
- `chat_host_two_replies.jsonl`: 同じプロセスの 2 回の返事（1 通目は Bash、2 通目は文だけ）。終わりの行（`type: result`）の `total_cost_usd` は 1 通目 0.00442549・2 通目 0.00477869。2 通目の増分（約 0.0004）は返事 1 回分として妥当で、累計として読める。
- `chat_host_resume_after_restart.jsonl`: 上の会話を、プロセスを終えた後に `--resume` で続けた返事。`total_cost_usd` は 0.00527537（前の累計 0.00477869 より大きい）で、`--resume` の後も累計が続く。
- `chat_host_permission_allow.jsonl`・`chat_host_permission_deny.jsonl`: 許可の確認（`type: control_request`・`request.subtype: can_use_tool`・`tool_name: Bash`・`tool_use_id`）。許可の確認の行は、対応する `tool_use` の行の後に出る。`requires_user_interaction` は付かない。断ると `tool_result` が `is_error: true`（内容は断りの文）で返る。
- `chat_host_question.jsonl`: AskUserQuestion の確認。`requires_user_interaction: true`・`input.questions[]`（`question`・`header`・`options[]`・`multiSelect`）。答えは `updatedInput.answers`（質問の文 → 選んだ選択肢の `label`）に入れて返す。
- `chat_host_plan.jsonl`: ExitPlanMode の確認。これも `requires_user_interaction: true`。AskUserQuestion 以外にも真になる確認がある。
- `chat_host_stopped.jsonl`・`chat_host_resume_after_stopped.jsonl`: 1 通目の後、2 通目の長い返事の文が出た直後にプロセスを止め（終わりの行は届かない）、`--resume` で続けた返事。`total_cost_usd` は 1 通目 0.00021242、止めた後の返事 0.00082942。増分は返事 1 回分で、止めた返事（約 1000 トークン）の費用は含まれていない。
- セッションのファイルの置き場所: `~/.claude/projects/<作業フォルダの絶対パスの / を - に替えた名前>/<セッションの識別>.jsonl`。`/private/tmp/claude-501/<名前>` で起動すると `-private-tmp-claude-501-<名前>`（macOS では実パスに解決された形）。
- 古い会話のファイルの掃除: 設定 `cleanupPeriodDays` の既定は 30 日（実物の設定の説明文）。最小は 1。

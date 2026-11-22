# 対話の実行（`claude -p --output-format stream-json`）の出力の写し

実物の実行エンジン（claude 2.1.330）を 1 回ずつ起動して取った出力。形は実物のまま、会話に関係しない長い項目（`init` の道具・スキル・プラグインの一覧など）と環境の固有値は省いてある。

- 起動の引数: `-p --session-id <UUID> --output-format stream-json --verbose --max-turns <n> --max-budget-usd <額> --allowedTools Read Grep Glob Bash --permission-mode default`。2 通目は `--session-id` の代わりに `--resume <UUID>`。`--verbose` が無いと `stream-json` の出力は始まらない。発言の途中の断片を流す `--include-partial-messages` は付けていない。
- `chat_stream_success.jsonl`: 1 通目。道具（`Bash`）を 2 回実行し（1 回は成功、1 回は終了コード 127 で失敗）、最後に文で返す。
  - 会話の始まりの行: `type: system`・`subtype: init`（`session_id` を持つ）。
  - 発言: `type: assistant` の `message.content[]` の `type: text`。
  - コマンドの実行: `type: assistant` の `content[]` の `type: tool_use`（`name`・`input.command`・`id`）。その結果は `type: user` の `content[]` の `type: tool_result`（`tool_use_id`・`is_error`）。
  - 終わりの行: `type: result`（`subtype: success`・`is_error`・`num_turns`・`stop_reason`・`terminal_reason`・`total_cost_usd`）。
- `chat_stream_resume.jsonl`: 2 通目（`--resume`）。続きでも先頭に `system` / `init` の行が出る。

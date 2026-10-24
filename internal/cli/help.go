package cli

// usageText は `flywheel --help` / `-h` / `help` が標準出力へ出す使い方の説明。
// コマンドと共通フラグの一覧は docs/features/m1-core.md の §IF / API を正本とする。
const usageText = `flywheel - flywheel の core を操作する CLI

使い方:
  flywheel <コマンド> [引数] [フラグ]

共通フラグ:
  --workspace <dir>   ワークスペースのディレクトリ（既定: FLYWHEEL_WORKSPACE → カレントディレクトリ）
  --json              出力を JSON にする

コマンド:
  init
  create --title <t> [--description <d>] [--done-criteria <c>] [--urgency <高|中|低>]
  show <ID>
  list [--status <状態>]
  edit <ID> [--title <t>] [--description <d>] [--done-criteria <c>] [--urgency <高|中|低>]
            [--headline <h>] [--context <c>] [--issues <json>] [--expected <json>] [--actual <json>]
            [--non-goals <json>] [--figure <mermaid>]   （上の 7 つは空文字を渡すとその欄を NULL にする）
  classify (<ID> --priority <P0|P1|P2> | --auto [<C-ID>])
  plan (<ID> (--file <path> | --stdin) | --auto [<C-ID>])
  submit <ID>
  verify (<ID> --result <met|not_met|uncertain> [--question <q>] | --auto [<C-ID>])
  hold <ID> [--question <q>]
  answer <ID> --answer <a> [--relayed --session <s> --quote <q>]
  approve <ID> [--hold-release] [--relayed --session <s> --quote <q>]
  reject <ID> --reason <r> [--field <issues|expected|non_goals|figure>]... [--relayed --session <s> --quote <q>]
  budget <C-ID> --impl-usd <額> [--review-usd <額>] [--relayed --session <s> --quote <q>]
  op add <C-ID> --kind <release|delete|external_send|other> --summary <s> [--ref <r>]
  op exec [<OP-ID>]
  dep add <C-ID> <依存先の C-ID>...
  dep rm <C-ID> <依存先の C-ID>...
  status
  log [<ID>]
  ingest [--source <id>]
  mark-read <C-ID>
  runs [<C-ID>] [--open]
  metrics [--since <YYYY-MM-DD>] [--until <YYYY-MM-DD>]
  cycle [--trigger <t>]
  run [<C-ID>]
  slot clear <SL-ID>
  serve [--port <n>] [--fleet <path>]

--relayed は、人間と会話しているエージェントが会話で受けた操作を記録する口（端末での確認は行わない。
--session は会話の識別、--quote は発言の要旨で、どちらも必須）。

詳細は docs/features/m1-core.md・docs/features/m3-invoker-delegation.md・docs/features/m4.5-learning-and-self-improvement.md を参照。
`

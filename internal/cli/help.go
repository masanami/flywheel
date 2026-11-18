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
  show <ID> [--plan]   （--plan＝最新の計画の方針・問いと推奨・タスクのツリー・予算の上限・統合先を使うかを出す）
  list [--status <状態>]
  edit <ID> [--title <t>] [--description <d>] [--done-criteria <c>] [--urgency <高|中|低>]
            [--headline <h>] [--context <c>] [--expected <json>] [--actual <json>]
            [--non-goals <json>] [--figure <mermaid>]   （上の 6 つは空文字を渡すとその欄を NULL にする）
  classify (<ID> --priority <P0|P1|P2> | --auto [<C-ID>])
  plan (<ID> (--file <path> | --stdin) | --auto [<C-ID>])
  submit <ID>
  verify (<ID> --result <met|not_met|uncertain> [--question <q>] | --auto [<C-ID>])
  hold <ID> [--question <q>] [--kind <human|parent_direct>]   （parent_direct＝対話のセッションの親が直接行う課題の保留）
  complete-direct <ID> --artifact <参照>... [--note <s>]   （親が直接行った作業を成果物の参照つきで記録し、完了確認待ちへ進める）
  answer <ID> (--answer <a> [--replan] | (--choice <Q番号>=<選択肢の番号> | --text <Q番号>=<文>)...) [--relayed --session <s> --quote <q>]
            （--choice・--text＝計画の承認待ちの問いへの答え。推奨と違う選択肢か文の答えがあれば、課題を分類済へ戻して計画を作り直す）
            （--replan＝範囲・方針・達成条件が変わる回答の印。委譲を再開せず、計画を作り直して計画の承認待ちへ戻す。着手中・検証中の保留だけ）
  approve <ID> [--hold-release | --reopen] [--relayed --session <s> --quote <q>]
            （--reopen＝OP-ID だけ。検証の後に動いた head が取り込みだけとも中身の変更とも確かめられず人間待ちの release について、
              承認せず課題を完了から着手中へ戻して検証し直す）
  reject <ID> --reason <r> [--field <issues|expected|non_goals|figure>]... [--relayed --session <s> --quote <q>]
  withdraw <C-ID> --reason <r> [--relayed --session <s> --quote <q>]
  budget <C-ID> --impl-usd <額> [--review-usd <額>] [--relayed --session <s> --quote <q>]
  learn [<C-ID>]
  improve
  op add <C-ID> --kind <release|delete|external_send|other> --summary <s> [--ref <r>]
  op exec [<OP-ID>]
  dep add <C-ID> <依存先の C-ID>...
  dep rm <C-ID> <依存先の C-ID>...
  bind <C-ID> <owner/repo#番号>   （取り込み元の Issue の無い課題を、open の GitHub Issue に紐づける。宣言の取り込み元のリポジトリだけ）
  unbind <C-ID>   （紐づけを外す）
  followup list [--status <open|done|dropped|all>] [--challenge <C-ID>]   （フォローアップの一覧。既定は未だけ）
  status
  version   （動いているバイナリのコミットとストアのスキーマの版）
  log [<ID>]
  ingest [<指定>...] [--source <id>]   （<指定> は <owner>/<repo>#<番号> か番号。選んだ Issue だけを課題にする）
  mark-read <C-ID>
  runs [<C-ID>] [--open]
  metrics [--since <YYYY-MM-DD>] [--until <YYYY-MM-DD>] [--compare <C-ID>]
  cycle [--trigger <t>]
  run [<C-ID>]
  slot clear <SL-ID>
  export --out <path>
  import --in <path>
  serve [--port <n>] [--fleet <path>]

--relayed は、人間と会話しているエージェントが会話で受けた操作を記録する口（端末での確認は行わない。
--session は会話の識別、--quote は発言の要旨で、どちらも必須）。

詳細は docs/features/m1-core.md・docs/features/m3-invoker-delegation.md・docs/features/m4.5-learning-and-self-improvement.md を参照。
`

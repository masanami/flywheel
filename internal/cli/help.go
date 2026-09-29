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
  classify (<ID> --priority <P0|P1|P2> | --auto [<C-ID>])
  plan (<ID> (--file <path> | --stdin) | --auto [<C-ID>])
  submit <ID>
  verify <ID> --result <met|not_met|uncertain> [--question <q>]
  hold <ID> [--question <q>]
  answer <ID> --answer <a>
  approve <ID> [--hold-release]
  reject <ID> --reason <r>
  op add <C-ID> --kind <release|delete|external_send|other> --summary <s> [--ref <r>]
  status
  log [<ID>]
  ingest [--source <id>]
  mark-read <C-ID>
  runs [<C-ID>] [--open]
  cycle [--trigger <t>]

詳細は docs/features/m1-core.md・docs/features/m3-invoker-delegation.md を参照。
`

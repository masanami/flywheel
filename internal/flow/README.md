# internal/flow

claude-harness の「フロー（YAML）と、それを進める仕組み」を flywheel へ写したもの。

## 写した元

- リポジトリ: [masanami/claude-harness](https://github.com/masanami/claude-harness)
- コミット: `b4d6154`（main）
- 範囲: そのコミットの `runtime/`

## assets/

`runtime/workflows/` の `ticket.yaml`・`reflect.yaml` と、その 2 つが使う `prompts/`・`schemas/`・`scripts/` のファイルを、中身を変えずに `assets/files/workflows/` へ写して `go:embed` したもの。`conflict-predict-issue.yaml`・`list-tests.yaml` とその 2 つだけが使うファイルは写していない。プラグインのスクリプト・エージェント定義も埋め込まない。文書の `prompts/README.md` も写していない。

- `assets/SHA256SUMS`: 写した元のファイルごとの SHA-256（`assets/files/` からのパス）。試験が埋め込んだ資材と過不足なく突き合わせる。資材のファイルを足す・消す・直すときは、この一覧も更新する。
- 展開先は `<ワークスペース>/.flywheel/flow/runtime/<版>/`（版は flywheel のバイナリのコミット）。置き場は `--workspace` の値だけから決め、環境変数は読まない。中身の印（`.bundle-sha256`）で確かめ、違えば上書きしない。

## 入口（flow.go・contract.go・detach.go・plugin.go）

`flywheel flow start|status|resume|cancel` の本体と、切り離して進める `__run-detached`、接続契約（版 1）の JSON の組み立て。写した元は `runtime/internal/cli` の `cli.go`・`contract.go`・`detach.go`・`version.go`。

- 置き場は `--workspace` から直接決める: 実行のフォルダは `<ws>/.flywheel/flow/runs/<実行の識別>/`、資材は `<ws>/.flywheel/flow/runtime/<版>/`。`HARNESS_STATE_DIR`・`HARNESS_DATA_DIR`・`XDG_STATE_HOME`・`XDG_DATA_HOME` は読まない。
- 定義の上書き（`.claude-harness/overrides/`）は読まず、埋め込んだ定義を読む。
- `claude-harness-run --plugin-root` でプラグインの場所を解決し、`plugin.json` の版が `4.9.0` 以上 `6.0.0` 未満でなければ、実行のフォルダを作らずに始めない。
- 写さなかったもの: 上書きの読み込み・`graph`・`predict-conflicts`・`setup`・端末の判定・人向けの引数の解釈と使い方・`approve`・`runs`。契約の `steps`・`flow` の欄は後続のタスクが足す。
- 写した元から変えた振る舞い: 契約の呼び出しはいつも `review` を受け付ける（`--accept-kind` を持たない）。切り離した runner の引き継ぎに失敗した `start`・`resume` は 0 でない終了コード。プラグインの場所・版を確かめられない `start` は、標準出力に何も出さず 0 でない終了コード。契約の `requested_action.text` のコマンドの案内は `flywheel flow resume …`。この版の flywheel と run を始めた版の定義の食い違い（写した元の終了コード 5 の仕組み）は持たない（記録した定義を開き直せなければ誤り）。

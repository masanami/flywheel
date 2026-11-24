# internal/flow

claude-harness の「フロー（YAML）と、それを進める仕組み」を flywheel へ写したもの。

## 写した元

- リポジトリ: [masanami/claude-harness](https://github.com/masanami/claude-harness)
- コミット: `b4d6154`（main）
- 範囲: そのコミットの `runtime/`

## assets/

`runtime/workflows/` の `ticket.yaml`・`reflect.yaml` と、その 2 つが使う `prompts/`・`schemas/`・`scripts/` のファイルを、中身を変えずに `assets/files/workflows/` へ写して `go:embed` したもの。`conflict-predict-issue.yaml`・`list-tests.yaml` とその 2 つだけが使うファイルは写していない。プラグインのスクリプト・エージェント定義も埋め込まない。文書の `prompts/README.md` も写していない。

- `assets/SHA256SUMS`: 写した元のファイルごとの SHA-256（`assets/files/` からのパス）。試験が埋め込んだ資材と過不足なく突き合わせる。資材のファイルを足す・消す・直すときは、この一覧も更新する。
- 展開先は `<ワークスペース>/.flywheel/flow/runtime/<版>/`（版は flywheel のバイナリのコミット。版を得られない・未コミットの変更つきのビルドでは、中身のハッシュの 12 文字を版に足す）。置き場は `--workspace` の値だけから決め、環境変数は読まない。中身の印（`.bundle-sha256`）で確かめ、違えば上書きしない。

## 入口（flow.go・contract.go・detach.go・plugin.go）

`flywheel flow start|status|resume|cancel` の本体と、切り離して進める `__run-detached`、接続契約（版 1）の JSON の組み立て。写した元は `runtime/internal/cli` の `cli.go`・`contract.go`・`detach.go`・`version.go`。

- 置き場は `--workspace` から直接決める: 実行のフォルダは `<ws>/.flywheel/flow/runs/<実行の識別>/`、資材は `<ws>/.flywheel/flow/runtime/<版>/`。`HARNESS_STATE_DIR`・`HARNESS_DATA_DIR`・`XDG_STATE_HOME`・`XDG_DATA_HOME` は読まない。
- 定義の上書き（`.claude-harness/overrides/`）は読まず、埋め込んだ定義を読む。
- `claude-harness-run --plugin-root` でプラグインの場所を解決し、`plugin.json` の版が `4.9.0` 以上 `6.0.0` 未満でなければ、実行のフォルダを作らずに始めない。
- 写さなかったもの: 上書きの読み込み・`graph`・`predict-conflicts`・`setup`・端末の判定・人向けの引数の解釈と使い方・`approve`・`runs`。契約の `flow`・`steps` は任意の欄で、契約の版は 1 のまま（下の「ステップの一覧の出力」）。
- 写した元から変えた振る舞い: 契約の呼び出しはいつも `review` を受け付ける（`--accept-kind` を持たない）。切り離した runner の引き継ぎに失敗した `start`・`resume` は 0 でない終了コード。プラグインの場所・版を確かめられない `start` は、標準出力に何も出さず 0 でない終了コード。契約の `requested_action.text` のコマンドの案内は `flywheel flow resume …`。この版の flywheel と run を始めた版の定義の食い違い（写した元の終了コード 5 の仕組み）は持たない（記録した定義を開き直せなければ誤り）。

## 写した後に振る舞いを変えた点

写した元（`b4d6154`）から flywheel の側で振る舞いを変えた点を、次の 7 つの見出しで列挙する。ここに無い直しは、lint と整形に合わせたものだけで、振る舞いは写した元のまま。写した後は、flywheel の側の写しが正本で、claude-harness の側の直しは、要るものを選んで手で取り込む（自動では追わない）。

### 置き場

- 実行の記録は `<ワークスペース>/.flywheel/flow/runs/<実行の識別>/`、展開した資材は `<ワークスペース>/.flywheel/flow/runtime/<版>/`（版は flywheel のバイナリのコミット。版を得られない・未コミットの変更つきのビルドでは、中身のハッシュの 12 文字を版に足す）。どちらも `--workspace` の値から直接決める。
- 写した元が読んでいた `HARNESS_STATE_DIR`・`HARNESS_DATA_DIR`・`XDG_STATE_HOME`・`XDG_DATA_HOME` は読まない。
- `.flywheel/flow/` は Git で追跡しない。`flywheel init` が `.flywheel/.gitignore` に `flow/` を入れ、`flywheel flow start` は、`.flywheel/.gitignore` が在って `flow/` の行が無ければ足す（無いファイルは作らない）。
- 記録の正本はこのフォルダの追記ファイルで、ストア（版 39 の `contract_run_step` と `contract_run.flow`）は、状態を読むたびに写した写し。

### 引数の受け方

- 呼び出しは `flywheel flow start|status|resume|cancel`（内部向け）。切り離して進める入口は `flywheel flow __run-detached <実行の識別>` で、コマンドの一覧に出さない。人向けの引数の解釈と使い方は写していない。
- すべてのコマンドが `--workspace <ワークスペース>` を受ける（写した元は作業ディレクトリと環境変数から決めていた）。
- `start`・`resume` は `--permission-mode <default|acceptEdits|auto>` と `--delegated-run <run の ID>` が必須。どちらかが無い・権限モードが 3 つの値の外なら、使い方の誤り。`status`・`cancel` は受けない。
- `start`・`resume` は `--model`・`--effort` を受けない（使い方の誤り。実行のフォルダを作らない）。モデルと effort は埋め込んだフローの定義だけで決まる。
- 契約の呼び出しはいつも `review` を受け付ける（`--accept-kind` を持たない）。
- 切り離した進める側の引き継ぎに失敗した `start`・`resume` は 0 でない終了コード。引き継ぎは 30 秒待つ。

### 上書き

- 写した元の定義の上書き（`.claude-harness/overrides/`）は読まない。埋め込んだ定義だけを読む。組み込みのフローで上書きを使えるようにするのは、この区切りの範囲外。
- 写した元の `graph`・`predict-conflicts`・`setup`・`approve`・`runs` も写していない。

### プラグインの場所と版

- 作業ツリーの作成と片付け・CI 待ち・CI の起動・品質の検査のスクリプトと、エージェントの定義は、入っている claude-harness のプラグインのものを使う。場所は `claude-harness-run --plugin-root` で解決し、スクリプトはその下の `scripts`、エージェントは `agents`。
- `<場所>/.claude-plugin/plugin.json` の版が `4.9.0` 以上 `6.0.0` 未満でなければ、実行のフォルダを作らずに始めない（版と範囲を書いた誤り）。範囲は `plugin.go` の定数。
- 場所・版を確かめられない `start` は、標準出力に何も出さず、0 でない終了コード。
- 前提: claude-harness のプラグインと `claude-harness-run` が入っていること。
- `HARNESS_*` の環境変数名は変えない。

### モデルの変数

- フローの YAML の最上位に `models`（変数の名前 → 既定の値。値は空でない文字列）を置け、`llm` のステップの任意のキー `model` に `$models.<名前>` だけを書ける。モデルの名前を直に書く・定義していない変数を指す・`llm` 以外に書く・`models` の値が空か文字列でない、は定義の検査の誤り。
- 変数の値は定義の値だけで決まり、`flow start` の引数では差し替えない。
- 解決した値（定義の値）は、実行の始まりの記録に持ち、`resume` の後も同じ値を使う。変数を指したステップは、その値を `--model <値>` に付けて claude を起動する。指さないステップは、いまと同じ（指定しない）。
- 使ったモデルは、claude の結果の `modelUsage` の名前を、ステップが終わったときに記録する。
- 写した 2 つのフロー（`ticket`・`reflect`）の定義には、変数もステップからの参照も書かない。

### effort の変数

- フローの YAML の最上位に `efforts`（変数の名前 → effort。値は `low`・`medium`・`high`・`xhigh`・`max` の 5 つの文字列だけ）を置け、`llm` のステップの任意のキー `effort` に `$efforts.<名前>` だけを書ける。値を直に書く・定義していない変数を指す・`llm` 以外に書く・`efforts` の値が 5 つの外か文字列でない、は定義の検査の誤り。
- 解決した値は、実行の始まりの記録に持ち、`resume` の後も同じ値を使う。変数を指したステップは、その値を `--effort <値>` に付けて claude を起動する。指さない `llm` のステップには付けない。
- 契約の出力（版は 1 のまま）の `steps` の要素に `effort` を持つ。変数を指したステップは解決した値、指さないステップは `null`。

### ステップの一覧の出力

- 契約（版 1）の出力に、任意の欄 `flow`（文字列）と `steps`（配列）を足した。版は 1 のまま。
- `steps` の要素は、`seq`（ステップが始まった記録の通し番号。1 以上）・`step`・`kind`・`attempt`・`state`（`running`・`finished`・`cancelled`・`interrupted`）・`outcome`・始まりと終わりの時刻・`cost_usd`・`cost_unknown`・`model`（変数を指したステップの解決値）・`models_used` など。対象は、実行のただ 1 つの単位（`main`）の、全部の回りのステップ。
- `cost_usd` は、ステップが終わった記録の「実行の累計へ加えた額」（終わっていない・claude を呼ばないステップは `null`）。合計は、契約の `cost_usd` と一致する。`cost_unknown` は、費用を得られず、渡した上限額を使ったものとして数えたとき。
- 読む側（core）は、`steps` が無い・配列でない・要素のどれか 1 つでも形が合わないときは、`steps` の全体を「無い」として扱い、出力の全体は不正にしない。

### 子の目印

- core は `flow start`・`resume` に `--delegated-run <run の ID>` を付ける。進める側へは、権限モードを `HARNESS_CLAUDE_PERMISSION_MODE`、run の ID を内部の環境変数（`FLYWHEEL_FLOW_DELEGATED_RUN`）で引き継ぐ。
- 進める側は、claude を起動するときだけ、子の環境に `FLYWHEEL_DELEGATED_RUN=<run の ID>` を足す。進める側のプロセス自身の環境と、つなぎのスクリプト・プラグインのスクリプトの環境には、目印を置かない（置くと、進める側が自分の起動で拒否される）。
- `flywheel flow` も、ほかのコマンドと同じに、目印のある環境からは実行できない。claude の子から `flywheel flow resume` を呼んで、自分のレビュー待ちに答えることはできない。

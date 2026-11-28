# internal/flow

claude-harness の「フロー（YAML）と、それを進める仕組み」を flywheel へ写したもの。

## 写した元

- リポジトリ: [masanami/claude-harness](https://github.com/masanami/claude-harness)
- コミット: `b4d6154`（main）
- 範囲: そのコミットの `runtime/`

## assets/

`runtime/workflows/` の `ticket.yaml`・`reflect.yaml` と、その 2 つが使う `prompts/`・`schemas/`・`scripts/` のファイルを `assets/files/workflows/` へ写して `go:embed` したもの。`conflict-predict-issue.yaml`・`list-tests.yaml` とその 2 つだけが使うファイルは写していない。プラグインのスクリプト・エージェント定義も埋め込まない。文書の `prompts/README.md` も写していない。写した後に変えた点は、下の「写した後に振る舞いを変えた点」にある。なお `spec-draft.yaml`・`prompts/spec-draft-write.md`・`prompts/spec-draft-critique.md`・`schemas/spec-draft-write.json`・`schemas/spec-draft-critique.json` は写した元の無い flywheel 独自の資材（`docs/features/flow-port.md` の「下書きづくりのフロー `spec-draft` と、そのための直し」）。また `ticket-light.yaml`・`prompts/ticket-light-*.md`・`scripts/ticket-light-branch.sh`・`schemas/ticket-light-branch.json` は写した元の無い flywheel 独自の資材で、`ticket.yaml` との差分は `docs/features/flow-port.md` の「簡略なフローの定義」の表のとおり。

- `assets/SHA256SUMS`: 写した元のファイルごとの SHA-256（`assets/files/` からのパス）。試験が埋め込んだ資材と過不足なく突き合わせる。資材のファイルを足す・消す・直すときは、この一覧も更新する。
- 展開先は `<ワークスペース>/.flywheel/flow/runtime/<版>/`（版は flywheel のバイナリのコミット。版を得られない・未コミットの変更つきのビルドでは、中身のハッシュの 12 文字を版に足す）。置き場は `--workspace` の値だけから決め、環境変数は読まない。中身の印（`.bundle-sha256`）で確かめ、違えば上書きしない。

## 入口（flow.go・contract.go・detach.go・plugin.go）

`flywheel flow start|status|resume|cancel` の本体と、切り離して進める `__run-detached`、接続契約（版 1）の JSON の組み立て。写した元は `runtime/internal/cli` の `cli.go`・`contract.go`・`detach.go`・`version.go`。

- 置き場は `--workspace` から直接決める: 実行のフォルダは `<ws>/.flywheel/flow/runs/<実行の識別>/`、資材は `<ws>/.flywheel/flow/runtime/<版>/`。`HARNESS_STATE_DIR`・`HARNESS_DATA_DIR`・`XDG_STATE_HOME`・`XDG_DATA_HOME` は読まない。
- 定義の上書き（`.claude-harness/overrides/`）は読まない。定義は、ワークスペースの `.flywheel/flows/<名前>.yaml` があればそれ、無ければ埋め込んだものを読む（下の「ワークスペースの定義」）。
- `claude-harness-run --plugin-root` でプラグインの場所を解決し、`plugin.json` の版が `4.9.0` 以上 `6.0.0` 未満でなければ、実行のフォルダを作らずに始めない。
- 写さなかったもの: 上書きの読み込み・`graph`・`predict-conflicts`・`setup`・端末の判定・人向けの引数の解釈と使い方・`approve`・`runs`。契約の `flow`・`steps` は任意の欄で、契約の版は 1 のまま（下の「ステップの一覧の出力」）。
- 写した元から変えた振る舞い: 契約の呼び出しはいつも `review` を受け付ける（`--accept-kind` を持たない）。切り離した runner の引き継ぎに失敗した `start`・`resume` は 0 でない終了コード。プラグインの場所・版を確かめられない `start` は、標準出力に何も出さず 0 でない終了コード。契約の `requested_action.text` のコマンドの案内は `flywheel flow resume …`。この版の flywheel と run を始めた版の定義の食い違い（写した元の終了コード 5 の仕組み）は持たない（記録した定義を開き直せなければ誤り）。

## 写した後に振る舞いを変えた点

写した元（`b4d6154`）から flywheel の側で振る舞いを変えた点を、次の見出しで列挙する。ここに無い直しは、lint と整形に合わせたものだけで、振る舞いは写した元のまま。写した後は、flywheel の側の写しが正本で、claude-harness の側の直しは、要るものを選んで手で取り込む（自動では追わない）。

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

- 写した元の定義の上書き（`.claude-harness/overrides/`）は読まない。組み込みとの差分だけを書く上書きは受けず、変えたいときは定義を丸ごとワークスペースに置く（下の「ワークスペースの定義」）。
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
- 写した 2 つのフロー（`ticket`・`reflect`）の定義は、`models` に `main: opus`、`efforts` に `main: xhigh` を持つ。`ticket` の実装（`implement`）と直し（`fix`）を除く `llm` のステップと、`reflect` のすべての `llm` のステップが `$models.main`・`$efforts.main` を指す（`implement`・`fix` は指さない）。Codex のレビューは下の「標準のフロー ticket の値と Codex のレビュー・efforts・`--model`（S2）」。`ticket-fix.md`・`ticket-respond.md` は Codex を起動しない。

### effort の変数

- フローの YAML の最上位に `efforts`（変数の名前 → effort。値は `low`・`medium`・`high`・`xhigh`・`max` の 5 つの文字列だけ）を置け、`llm` のステップの任意のキー `effort` に `$efforts.<名前>` だけを書ける。値を直に書く・定義していない変数を指す・`llm` 以外に書く・`efforts` の値が 5 つの外か文字列でない、は定義の検査の誤り。
- 解決した値は、実行の始まりの記録に持ち、`resume` の後も同じ値を使う。変数を指したステップは、その値を `--effort <値>` に付けて claude を起動する。指さない `llm` のステップには付けない。
- 契約の出力（版は 1 のまま）の `steps` の要素に `effort` を持つ。変数を指したステップは解決した値、指さないステップは `null`。

### 下書きづくりのフロー spec-draft に合わせた直し（要求から計画まで S2b-1）

- **`facts` に配列の欄を載せる**: 写した元の `facts` は、出力の型が文字列の欄だけ。配列の欄も宣言できるようにし、値は `encoding/json` の既定の直列化（空白なし・要素とキーは出力の順）の文字列にして載せる。配列を宣言した欄が `null`・欠けている・配列でないときは値なしにする（前の値も残さない）。オブジェクト・数値・真偽の欄は、定義の読み込みで誤り。契約の JSON の形（名前 → 文字列）と版は変えない。
- **`llm` のステップのキー `tools`**: 道具の名前の並び（1 件以上）。書いたステップだけ、claude の起動の引数に `--tools <名前をカンマでつないだもの>` を足す。
- **`flow start --budget-usd <額>`**: 正の数。フロー `spec-draft` のときだけ受け付け（ほかのフローでは使い方の誤り）、渡した額をその実行の `limits.budget_usd` にする。

### ステップの一覧の出力

- 契約（版 1）の出力に、任意の欄 `flow`（文字列）と `steps`（配列）を足した。版は 1 のまま。
- `steps` の要素は、`seq`（ステップが始まった記録の通し番号。1 以上）・`step`・`kind`・`attempt`・`state`（`running`・`finished`・`cancelled`・`interrupted`）・`outcome`・始まりと終わりの時刻・`cost_usd`・`cost_unknown`・`model`（変数を指したステップの解決値）・`models_used` など。対象は、実行のただ 1 つの単位（`main`）の、全部の回りのステップ。
- `cost_usd` は、ステップが終わった記録の「実行の累計へ加えた額」（終わっていない・claude を呼ばないステップは `null`）。合計は、契約の `cost_usd` と一致する。`cost_unknown` は、費用を得られず、渡した上限額を使ったものとして数えたとき。
- 読む側（core）は、`steps` が無い・配列でない・要素のどれか 1 つでも形が合わないときは、`steps` の全体を「無い」として扱い、出力の全体は不正にしない。

### 標準のフロー ticket の値と Codex のレビュー・efforts・`--model`（S2）

- `ticket` の定義の実装のステップ（`implement`）の `with` に `codex_review: "on"` を書く。実装の指示文（`prompts/ticket-implement.md`）は、`on` のとき、実装が済んだら `claude-harness-run codex-review-runner` を `/self-review` と並べて裏で起動し、終わるまで出力を返さない。直さなかった Codex の指摘は `claim` の先頭に `[Codex] ` を付けて `residual_findings` へ載せ、完了しなかったときは `unverified` に書く（これだけでは `failure` にしない。Codex が使えなくても止めない）。出力の型（`schemas/implement-result.json`）に `codex_review`（`off`・`complete`・`partial`・`failed`・`skipped`）を足した。フローを進める仕組みに並列のステップは足していない。Codex の費用は、ステップの費用・予算に数えない。
- モデルと effort の値は、フローの定義に書いて固定する（`ticket`・`reflect`・`ticket-light`）。`feature-implementer` のステップには `model`・`effort` を書かない。
- `efforts` の変数（上の「effort の変数」）を足した。
- `flow start` の `--model` と、宣言の `models` を外した（上の「引数の受け方」）。フローの定義の中の変数 `models`・`efforts` は残す。

### 子の目印

- core は `flow start`・`resume` に `--delegated-run <run の ID>` を付ける。進める側へは、権限モードを `HARNESS_CLAUDE_PERMISSION_MODE`、run の ID を内部の環境変数（`FLYWHEEL_FLOW_DELEGATED_RUN`）で引き継ぐ。
- 進める側は、claude を起動するときだけ、子の環境に `FLYWHEEL_DELEGATED_RUN=<run の ID>` を足す。進める側のプロセス自身の環境と、つなぎのスクリプト・プラグインのスクリプトの環境には、目印を置かない（置くと、進める側が自分の起動で拒否される）。
- `flywheel flow` も、ほかのコマンドと同じに、目印のある環境からは実行できない。claude の子から `flywheel flow resume` を呼んで、自分のレビュー待ちに答えることはできない。

### レビューのステップ（フローのカスタマイズ S1）

- `ticket`・`ticket-light` のセルフレビューを、実装のステップの指示文から外し、レビューのステップ `self-review`（指示文 `prompts/ticket-self-review.md`・出力の型 `schemas/self-review-result.json`）と直すステップ `self-review-fix` にした。`self-review` の結果は `findings`（直すべき指摘あり。`self-review-fix` へ戻り、回数は `limits.self_review` で数える。尽きたら残った指摘を PR に載せて `commit` へ）・`clean`（直すべき指摘なし。`commit` へ）・`incomplete`（最後まで行えなかった。`self_review_incomplete` で止まる）。`self-review-route` と、実装の出力の `review_incomplete`・`self_review`・`codex_review`・`residual_findings`、直し・レビュー対応の出力の `self_review` を外した。直し（`fix`）の後も `self-review` を通る。
- レビュアーの構成・境目・Codex は `self-review` の `with` に書く（`reviewers`・`verifier`・`fix_threshold`・`codex_review`）。`ticket` はレビュアー 2 体と `claude-harness:finding-verifier`・上限 3・Codex を使う、`ticket-light` はレビュアー 2 体・疑う役なし（`none`）・上限 1・Codex を使わない。Codex のレビューは 1 回目の `self-review` だけが起動する（上の「標準のフロー ticket の値と Codex のレビュー」の実装のステップの手順を移した）。
- `self-review-result.json` は `if`/`then` で組み合わせを縛る: `outcome` が `findings` なら `findings` は 1 件以上で `self_review` は `complete`、`clean` なら `findings` は 0 件で `complete`、`incomplete` なら `self_review` も `incomplete`。食い違った出力は型に合わない出力（`invalid_output`）になる。
- 契約の `steps` の要素に `findings_count` を足した: 終わったステップの出力が最上位に配列 `findings` を持てばその件数（レビューのステップの指摘の数）、持たなければ `null`。core はステップの記録に写し、タスクの詳細（`show` のテキスト・`--json`・画面のステップの一覧）に出す。
- `facts.self_review` は `$steps.self-review.self_review` から出す。PR の残指摘は `$steps.self-review.residual_findings`、レビューの未検証は pull-request の新しい `with` の `review_unverified`（実装の `unverified` の後ろに並べて載せる）から渡す。

### ワークスペースの定義（フローのカスタマイズ S1）

仕様は `docs/features/flow-customization.md`。実装は `definition.go`（探し方・検査・写し）と `check.go`（写す口・確かめる口）。

- **置き場**: ワークスペースの `.flywheel/flows/<名前>.yaml` に、定義を丸ごと置ける（名前は `^[a-z][a-z0-9-]*$`）。`flow start <名前>` は、置いてあればそれで始め（組み込みと同じ名前なら置き換え、無い名前なら新しいフロー）、無ければ組み込みで始める。
- **指示文**: ワークスペースの定義が指す `prompts/<x>.md` は、`.flywheel/flows/prompts/<x>.md` があればそれ、無ければ組み込みのもの。出力の型（`schemas/`）とつなぎのスクリプト（`scripts/`）は組み込みだけで、`.flywheel/flows/schemas/`・`.flywheel/flows/scripts/` があれば `start` も `check` も誤りにする。組み込みの定義は組み込みの指示文だけを使う（ワークスペースの `prompts/` を見ない）。定義の YAML・指示文・`.flywheel/flows`・`prompts/` がシンボリックリンクか通常のファイル（ディレクトリ）でなければ、置き場の外を読まずに `start` も `check` も誤りにする（指示文は組み込みへも落ちない）。
- **検査**: 定義の YAML・使う指示文・組み込みの `schemas/` と `scripts/` を 1 つのディレクトリ（写し）に並べ、組み込みと同じ読み込みと検査（`workflow.LoadWithOverrides`。上書きは重ねない）にかける。通らなければ実行を始めず、契約の `failed` を返す（理由は元のファイルのパスと行）。組み込みの定義へ落として動かすことはしない。定義が無い・検査を通らない・入力が合わないために実行を作らない `start` は、契約の `failed` の JSON と終了コード 0 のまま、同じ理由を標準エラーへ 1 行で出す（複数行の理由は `; ` でつなぐ）。
- **実行の写し**: 検査を通った写しを、実行のフォルダの `definition/` へ移し、その写しを定義の場所（`workflow.path`・`workflow_dir`）として始める。`resume`・`__run-detached` は写しを読むので、始めた後にワークスペースのファイルを変えても実行は変わらない。開き直すときは、YAML の sha256 の照合（いまと同じ）に加えて、写しの YAML とワークスペースから写した指示文が、始めたときの定義のハッシュのままかを確かめる。ワークスペースの定義で始めた実行は `embedded` を偽にする。組み込みの定義の実行は、いまと同じ（展開した資材を直に読む）。写した指示文は所有者が書ける権限（0644）で作る。定義の YAML・`schemas/`・`scripts/` の写しと展開した元（`runtime/<版>/`）は読み取り専用。再開の前の照合が見るのは写しの YAML とワークスペースから写した指示文だけで、組み込みから写した指示文の書き換えは見ない。
- **定義のハッシュ**: ワークスペースの定義は、YAML の中身と、ワークスペースから写した指示文（パスと中身。パスの昇順）をまとめた sha256（`definitionHash`）。組み込みは YAML の sha256（`workflow.hash` と同じ）。
- **記録と契約**: 実行の始まりの記録（`run_started`）に `definition`（`source`＝`workspace`・`builtin`、`sha256`、ワークスペースの定義では元のパスと写した指示文の一覧）を残す。契約（版 1 のまま）の JSON に任意の欄 `definition`（`source`・`sha256`）を足し、実行を読めた `start`・`status`・`resume`・`cancel` の出力に必ず載せる。記録の無い（この版より前に始めた）実行は `builtin` と YAML の sha256 を出す。
- **写す口**: `flywheel flow export <名前>` は、組み込みの定義の YAML だけを `.flywheel/flows/<名前>.yaml` へ書き出し、その絶対パスを標準出力へ出す（指示文は書き出さない）。書き出す先に既にあれば上書きせず、組み込みに無い名前と合わせて 0 でない終了コード。`.flywheel/flows/` が無ければ作る。
- **確かめる口**: `flywheel flow check [--json] [<名前>...]` は、名前が無ければ `.flywheel/flows/*.yaml` をすべて、あればその名前だけを検査する（置いていない名前は組み込みを検査し、組み込みにも無ければ通らない）。すべて通れば 0、1 つでも通らなければ 1。テキストは定義ごとに 1 行（名前・種別・通ったか・ハッシュの先頭 12 文字）と「ファイル:行: 理由」。`--json` は `{"definitions":[{"name","source","kind"（replace・new・builtin）,"path","sha256","ok","problems":[{"file","line","message"}],"description"}]}`（`description` は通った定義の説明。通らなければ空）。定義の `description` は 500 文字以内で、超えると検査を通らない（組み込みにも同じ検査）。core は、組み込みのほかの名前のワークスペースの定義では、この説明を計画を作る判断点の入力の操作の行に目安として添える。検査はプラグインのスクリプトとエージェントの実在も確かめるので、`start` と同じくプラグインの場所を解決できないと検査できない（0 でない終了コード）。

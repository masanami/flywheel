# M2: GitHub Issue の取り込み adapter

- 状態: **draft（決定待ち）**。論点は §決定事項の記録 に「選択肢・推奨・根拠」で置き、**推奨を仮に採って**本文を書いている（【仮採用】）。人間が決める論点（QH1〜QH7）と親が決めてよい論点（QP1〜QP11）が決まった時点で【決定】へ置き換える
- 設計の正本: [docs/architecture.md](../architecture.md)（§6 `source_binding`・§7 の 0. 観測・取り込み・§13 外部連携・§16 Q4／Q5・§17 M2）
- 前段: [M1 の機能仕様](m1-core.md)（ストア・状態機械・承認・作業ログ・CLI 共通規約。**ここで【決定】済みの事項はすべて前提にし、再検討しない**）
- 経緯: [flywheel#49](https://github.com/masanami/flywheel/issues/49)・[claude-flywheel#140](https://github.com/masanami/claude-flywheel/issues/140)（上流の close の取り込み）
- 確度の表記: **【決定】** オーナーまたは親が決めた事項（日付と決めた人を併記）／**【仮採用】** 未決の論点の推奨を仮に採ったもの（§決定事項の記録 の番号を併記）／**【仮定】** 起草者が軽微・可逆な判断として置いたもの

## 概要

GitHub Issue を課題としてストアへ取り込む adapter を、LLM を一切使わない形で作る。取り込み元の宣言を読み、Issue を取得し、assignee のポリシーで絞り、fingerprint で冪等に作成・更新し、**上流の close を検出して課題の状態との食い違いを示す**。入口は CLI の `flywheel ingest` で、M1 の core の公開 API を通してだけ状態を変える（設計書 §3 P2）。

## 背景・目的

- 現行（claude-flywheel の `ingest-challenges` スキル）は、Issue 一覧の取得・assignee での絞り込み・fingerprint の照合・取り込み解除・アーカイブとの照合という**決定的な処理を、毎周 LLM が散文を読んで実行している**（設計書 §1.1 の課題 1、§1.2 の手順 0 は 15.1 KB）。M2 はこれを「LLM を介さない最初の実例」にする（設計書 §13）。
- 現行は open の Issue しか候補にせず、**取り込んだ Issue が close されても台帳に反映する経路が無い**（claude-flywheel#140）。M2 の完了の目安は「LLM を介さずに取り込みと上流 close の検出ができる」（設計書 §17）。
- M2 のストアは M1 のストアを拡張する。課題は M1 の状態機械にそのまま乗り、M3 の J1 分類（未分類の課題が自ポジションのものか）へ渡る。

## ユーザーストーリー

- **オーナー（人間）** として、自分が assign した GitHub Issue が、コマンド 1 つで課題として取り込まれてほしい。上流で close された課題がどれかを `status` で知りたい。
- **cron と対話セッションの Claude**（将来の `flywheel cycle` を含む）として、`flywheel ingest --json` を何度実行しても、上流が変わっていなければストアが変わらないことを当てにしたい。
- **M5 の移行を書く開発者** として、現行の台帳の取り込み元マーカー（`<!-- fp:2:<12桁> -->`）の値を、そのまま新しいストアへ持ち込めてほしい。

## 機能要件

用語: **上流**＝取り込み元の GitHub Issue。**取り込み元**＝宣言ファイルの 1 ブロック（id・対象リポジトリ・ポリシー）。**対応**＝課題と上流の Issue の 1 対 1 の結び付き（`source_binding`）。**食い違い**＝完了していない課題の上流が close・消失した、または課題が取り込み元のポリシーに合わなくなった状態（§クリティカル設計決定 3）。**偽の `gh`**＝テストで PATH の先頭に置く、固定の応答を返す実行ファイル。

### 取り込み元の宣言【仮採用 QP1】

- [ ] 取り込み元の宣言は、ワークスペースの `.flywheel/sources.json` に置く（JSON。Git で追跡する設定ファイル＝設計書 §5【仮定】・§10）
- [ ] 宣言は、取り込み元ごとに `id`・`type`・`repos`・`assignee_policy`・`self_assignees`・`urgency_labels` を持つ（形は §IF / API）
- [ ] `type` は `github-issue` だけの閉集合
- [ ] `assignee_policy` は `self-only | exclude-others` の閉集合。省略時は `exclude-others`（現行と同じ）
- [ ] `id` は `^[a-z0-9][a-z0-9-]*$` に一致し、宣言の中で重複しない
- [ ] `repos` は 1 件以上で、各要素は `<owner>/<name>` の形。同じリポジトリは宣言全体で 1 つの取り込み元にだけ書ける
- [ ] `urgency_labels` の値は `高 | 中 | 低` の閉集合
- [ ] 宣言に未知のキーがある・型が違う・上の規則に反するときは、何も取得・変更せずに `config_invalid` で終わる（fail-closed）
- [ ] 宣言ファイルが無ければ、何も取得・変更せずに `config_not_found` で終わる【仮採用 QP4】
- [ ] 宣言ファイルとストア・作業ログのどこにも、トークン・資格情報を書かない。認証は `gh` の認証に委ねる（現行と同じ）

### 上流からの取得【仮採用 QP2・QP7】

- [ ] 取得は、PATH 上の `gh` を子プロセスとして起動し、`gh api` で GitHub の REST API を呼んで行う。`gh` の絶対パスをコードに埋めない（D13）
- [ ] リポジトリごとに、open の Issue を**全ページ**取得する（件数の上限で打ち切らない。現行の `--limit 200` の打ち切りを持ち込まない）
- [ ] REST の Issue 一覧に含まれる Pull Request（`pull_request` キーを持つ要素）は取り込まない
- [ ] 一覧の取得に失敗したリポジトリは、そのリポジトリについて何も変更せず、結果に失敗として示す。他のリポジトリの処理は続ける（部分成功。現行と同じ）
- [ ] 取得（ネットワークの待ち）の間は、ストアの書き込みロックを保持しない
- [ ] `gh` の 1 回の呼び出しには時間の上限（60 秒【仮定】）を置き、超えたらその呼び出しを失敗として扱う
- [ ] `gh` が PATH に無いときは、何も変更せずに `upstream_unavailable` で終わる【仮採用 QP4】

### 取り込みの対象（assignee のポリシー）【仮採用 QH4・QP8】

- [ ] `self_assignees` の login と Issue の assignee の login は、大文字小文字を無視し、先頭の `@` を除いて比較する
- [ ] `self-only` は、assignee が 1 人以上いて、全員が `self_assignees` に含まれる Issue だけを対象にする
- [ ] `exclude-others` は、assignee がいない Issue と、全員が `self_assignees` に含まれる Issue を対象にする
- [ ] `self_assignees` を省略した取り込み元は、`gh api user` の login を `self_assignees` とみなす
- [ ] `self_assignees` を省略し、その解決に失敗したときは、`self-only` の取り込み元は新しい Issue を何も対象にしない（現行と同じ安全側）
- [ ] `self_assignees` を省略し、その解決に失敗したときは、`exclude-others` の取り込み元は新しい Issue のうち assignee がいないものだけを対象にする（現行と同じ安全側）
- [ ] `self_assignees` を省略し、その解決に失敗したときは、対応済みの課題のポリシーの再判定をしない（取り込みの安全側＝取り込まないと、対象から外す判定の安全側＝外さないは向きが逆のため。§ポリシーに合わなくなった課題 の規則より優先する）
- [ ] ポリシーに合わない新しい Issue は課題にせず、結果に「対象外（assignee）」として数える
- [ ] 関連度（自ポジションのものか）の判定はしない。ポリシーに合う Issue はすべて取り込む【仮採用 QH7】

関連度の判定は M3 の J1（分類）が担う（M2 の範囲外）。

### 冪等な作成と更新【仮採用 QH1・QH2・QH3】

- [ ] 上流の Issue は外部キー `<owner>/<name>#<番号>` で識別する。1 つの外部キーに対応する課題はストアの中で高々 1 つ
- [ ] 対応の無い、ポリシーに合う Issue からは、状態 `未分類` の課題を 1 つ作り、対応を記録する
- [ ] 取り込みで作る課題のタイトルは、Issue のタイトルである
- [ ] 取り込みで作る課題の説明は、Issue の本文そのままである（要約しない）
- [ ] 取り込みで作る課題の完了条件は、空である
- [ ] 取り込みで作る課題の緊急度は、`urgency_labels` に一致するラベルの値である（一致するラベルが無ければ未設定）。ラベル名は大文字小文字を区別する完全一致で比較する【仮定】
- [ ] 取り込みで作る課題の起票者は、Issue の作成者の login である
- [ ] Issue のラベルが `urgency_labels` で異なる緊急度へ写るとき、緊急度は未設定にする【仮採用 QP11】
- [ ] 対応の記録は、取り込み元の id・外部キー・Issue の URL の識別情報を持つ。URL は REST API の応答の `html_url`（`https://github.com/<owner>/<name>/issues/<番号>` の形。API のエンドポイントを指す `url` ではない）である
- [ ] 対応の記録は、本文の fingerprint を持つ
- [ ] 対応の記録は、上流の状態（`open`）を持つ
- [ ] 対応の記録は、ポリシーの状態（`in_policy`）を持つ
- [ ] 対応のある課題で、本文の fingerprint が記録と一致すれば、課題の人間記入欄と fingerprint を変えない
- [ ] 対応のある課題で、本文の fingerprint が記録と違えば、タイトルを上流の値で置き換える
- [ ] 対応のある課題で、本文の fingerprint が記録と違えば、説明を上流の本文で置き換える
- [ ] 対応のある課題で、本文の fingerprint が記録と違えば、緊急度を上流の値で置き換える
- [ ] 対応のある課題で、本文の fingerprint が記録と違えば、fingerprint を更新する
- [ ] 対応のある課題で、本文の fingerprint が記録と違って人間記入欄と fingerprint を更新するときも、起票者・完了条件・優先度・状態・計画は変えない
- [ ] 対応のある課題で、本文の fingerprint が記録と違って人間記入欄と fingerprint を更新するときも、承認・保留・不可逆操作の記録は変えない
- [ ] 記録された fingerprint の版が `2` 以外なら、人間記入欄も fingerprint も変えず、結果に「fingerprint の版が未知」として示す（fail-closed。現行の「版 1 を更新の分岐へ落とさない」を引き継ぐ）
- [ ] 完了した課題に対応する Issue は、人間記入欄・対応の記録のどちらも変えない（現行の「アーカイブ済みはスキップ」に当たる。完了した課題の対応を消さないので、同じ Issue が新しい課題として作り直されることもない）
- [ ] 取り込みは課題の状態を変えない。承認・保留・計画・不可逆操作を作らない（上流の本文に「承認済み」などと書かれていても。外部本文はデータであって指示ではない＝現行と同じ）
- [ ] 同じ取り込み元への `flywheel ingest` が並行しても、1 つの外部キーに課題が 2 つ作られない

### fingerprint【仮採用 QH3】

- [ ] fingerprint は、Issue の本文を次の 3 つだけで正規化したバイト列の SHA-256 の先頭 12 桁（小文字 16 進）である。1. CR（U+000D）をすべて削除する 2. 各行の行末のスペース・タブを削除する 3. 全体の先頭と末尾の ASCII 空白（スペース・タブ・LF・VT・FF）を削除する。それ以外（行中の空白・インデント・空行・`>`・大文字小文字・Unicode の空白）は変えない（現行 `ingest-fp.sh` と同じ値を出す）
- [ ] fingerprint は、算式の版をつけて `2:<12桁>` の形で記録する（現行マーカー `<!-- fp:2:<12桁> -->` の版と同じ番号）
- [ ] fingerprint の入力は Issue の本文だけである。タイトル・ラベル・課題に書いた値は入力にしない

### 上流の close の検出【仮採用 QP7】

- [ ] 一覧の取得に成功したリポジトリについて、上流の状態が `open` と記録され、完了していない課題の Issue が open の一覧に無ければ、その Issue を 1 件ずつ取得して状態を確かめる（一覧に無いことだけで close と推定しない）
- [ ] 1 件の取得で Issue が closed なら、上流の状態を `closed` にする
- [ ] 1 件の取得の応答が HTTP 404 または 410 なら、上流の状態を `missing` にする（削除・移管・閲覧権限の喪失を区別しない）
- [ ] 1 件の取得がそれ以外で失敗したら、上流の状態を変えず、結果に失敗として示す
- [ ] 上流の状態が `closed` か `missing` の完了していない課題の Issue が open の一覧に現れたら、上流の状態を `open` に戻す（reopen）
- [ ] 宣言から外したリポジトリ・取り込み元に対応する課題は、取得も上流の状態の変更もしない

### ポリシーに合わなくなった課題（取り込み解除の置き換え）【仮採用 QH4】

- [ ] 対応のある、完了していない課題の Issue が open の一覧に現れたとき（`self_assignees` の解決に失敗した回を除く＝§取り込みの対象）、現在のポリシーに合わなければポリシーの状態を `out_of_policy` にし、合えば `in_policy` に戻す
- [ ] ポリシーに合わなくなっても、課題を削除・取り下げせず、状態も変えない（現行の「着手前なら台帳から削除」は持ち込まない）

### 食い違いの表示【仮採用 QP5】

- [ ] 食い違いは、完了していない課題について、上流の状態が `closed` か `missing` であるか、ポリシーの状態が `out_of_policy` であることとする。食い違いは記録から導き、別に保存しない
- [ ] `status` は、食い違いのある課題を「人間の操作を待っているもの」の中の食い違いの一覧に、食い違いの種類つきで出力する
- [ ] 食い違いのある課題も、M1 の `status` の区分（人間待ち・進められるもの）にはそのまま出る（M1 の区分の規則を変えない）
- [ ] `show` は、対応のある課題について対応の記録（取り込み元・外部キー・URL・上流の状態・ポリシーの状態）を出力する。対応の無い課題では `null` を出す

### 作業ログと版【仮採用 QP6】

- [ ] 取り込みによる課題の作成は、成功するたびに `ingest_create` として作業ログへ記録する
- [ ] 取り込みによる人間記入欄の更新は、成功するたびに `ingest_update` として作業ログへ記録する
- [ ] 取り込みによる上流の状態の変化は、成功するたびに `upstream_state_change` として作業ログへ記録する
- [ ] 取り込みによるポリシーの状態の変化は、成功するたびに `policy_state_change` として作業ログへ記録する
- [ ] 取り込みの作業ログは M1 の作業ログの規則（変更と同じトランザクション・変わった項目だけ・値が変わらなければ記録しない）に従う
- [ ] 取り込みのエントリの actor は実行した OS のログインユーザー名、経路は `cli`、本人確認の方式は `none` である
- [ ] 取り込みによる課題の変更（上流の状態・ポリシーの状態の変化を含む）は、課題の版を 1 増やす（M1 の「確認の途中で対象が変われば承認を成立させない」が、取り込みによる変更にも効くようにする）
- [ ] 1 つの Issue の反映は 1 つの書き込みトランザクションで行う。途中の Issue で失敗しても、それより前に反映した Issue は残る

### 実行の起点【仮採用 QH6・QP3】

- [ ] `flywheel ingest` は、宣言のすべての取り込み元を宣言の順に処理する
- [ ] `flywheel ingest --source <id>` は、指定した取り込み元だけを処理する。宣言に無い id は `validation_failed`
- [ ] `flywheel ingest --json` は、取り込み元・リポジトリごとの結果（作成・更新・変化なし・対象外・完了済みでスキップ・上流の状態の変化・ポリシーの状態の変化・失敗）を出力する（形は §IF / API）
- [ ] リポジトリ単位の失敗があっても、コマンドは終了コード 0 で終わり、失敗は結果に示す【仮採用 QP4】
- [ ] 定期実行の仕組み（スケジューラ）は持たない。定期実行は cron などの外部から `flywheel ingest` を呼ぶ

## 非機能要件

- **LLM**: M2 は LLM を呼ばない（設計書 §17）。
- **ネットワーク**: GitHub への接続は `flywheel ingest` の `gh` の子プロセスに限る。`ingest` 以外のコマンドはネットワークへ接続しない（M1 と同じ）。**`go test ./...` は GitHub へ接続しない**（CI に GitHub の認証は無い。上流は偽の `gh` か、core の取得 IF の偽の実装で置き換える）。
- **書き戻し**: GitHub へ書き込む API（POST・PATCH・PUT・DELETE）を呼ばない【仮採用 QH5】。
- **外部本文の扱い**: Issue の本文・タイトルはデータとして保存するだけで、解釈して状態・承認・コマンドに反映しない。本文はストアにそのまま保存され、M1 S3 の `export` の出力にも含まれる【仮採用 QH2】。
- **並行性**: `flywheel ingest` 同士、`ingest` と他のコマンドが同じストアを同時に使っても、課題の重複と変更の消失が起きない（M1 と同じストアの規則）。
- **配布・動作環境**: M1 と同じ（`CGO_ENABLED=0` の単一バイナリ・macOS と Linux）。`gh` は実行時の依存であり、ビルドの依存ではない。

## 技術的な制約・方針

- 使用技術: Go（M1 と同じ）。本番の依存の上限（標準ライブラリ・`modernc.org/sqlite`・`golang.org/x/term`）を変えない。GitHub のクライアントライブラリを足さない（`gh` を子プロセスで呼ぶため）。
- 変更対象: `internal/core`（取り込みの規則・対応の記録・宣言の検証・fingerprint）、`internal/core/internal/store`（マイグレーション 0002）、`internal/cli`（`ingest`・`status`・`show` の出力）、**新設** `internal/adapters/github`（`gh` の起動と応答の正規化）【仮採用 QP10】。
- 既存コードとの関係: M1 の core の公開 API・ストア・作業ログ・エラーコードの写像を拡張する。M1 の遷移表・承認・`status` の区分は変えない。現行（claude-flywheel）のコードは持ち込まない（設計書 §15）。ただし fingerprint は現行 `scripts/ingest-fp.sh` と同じ値を出す（QH3）。
- `CLAUDE.md` の「M1 で置くパッケージは 4 つだけ」は、`internal/adapters/github` を足す実装チケットで同時に更新する（本仕様の PR では変えない）。

### テストでの GitHub の置き換え【仮採用 QP2】

- core の取り込みは、上流の取得を IF（一覧の取得・1 件の取得・自分の login の取得）越しに呼ぶ。core のテストは、この IF の偽の実装（メモリ上の Issue の集合）と**実ストア**（`t.TempDir()` の SQLite）で行う。ストアはモックしない（M1 と同じ）。
- `internal/adapters/github` と CLI のテストは、**偽の `gh`**（テストが一時ディレクトリに置き、PATH の先頭に足す実行ファイル。引数に応じてフィクスチャの JSON と HTTP ステータスを返す）で行う。本物の `gh` と GitHub は呼ばない。
- 実物の GitHub との疎通は、受入基準の要人間判定に置く。

### 現行から引き継ぐ振る舞い

現行（claude-flywheel 0.29.0 系の `skills/ingest-challenges/SKILL.md`・`scripts/ingest-fp.sh`・`scripts/tests/ingest-fp.test.sh`）の振る舞いの対応。

| 現行 | 固定している振る舞い | M2 での扱い |
|---|---|---|
| `ingest-fp.sh` とテスト（ゴールデン値 `9c56b5c0a92d`） | 本文だけを入力に、3 つの正規化の後の SHA-256 の先頭 12 桁。版 `2` | **同じ値を出す**（QH3）。ゴールデン値のフィクスチャを testdata へ写してテストで固定する。写し元は masanami/claude-flywheel の `main` 上のコミット `1ffdd52e5e0f9ca66a28cc28cdad7b8402c1c428` の `scripts/tests/fixtures/ingest-fp/issue-130-body.txt`（8,591 バイト・ファイル全体の SHA-256 は `5daa7cdc6e728b82956d267e96d3c17112ce1bc976669f4ff9eeb9e7ec891980`）。兄弟クローンが無い環境では `gh api 'repos/masanami/claude-flywheel/contents/scripts/tests/fixtures/ingest-fp/issue-130-body.txt?ref=1ffdd52e5e0f9ca66a28cc28cdad7b8402c1c428' -H 'Accept: application/vnd.github.raw'` で取得し、写した後にバイト数と SHA-256 が一致することを確かめる |
| `ingest-fp.sh --from-ledger-quote` | 台帳の原文引用から版 1 を移行する照合 | 持ち込まない（版 1 の移行は M5） |
| 版 1 マーカーを更新の分岐へ落とさない | 比較できない fingerprint で人間記入欄を上書きしない | 記録された版が `2` 以外なら何も変えない（fail-closed） |
| 手順 2 assignee フィルタ | `self-only`／`exclude-others`・大文字小文字の無視・`@` の除去・co-assign・`gh api user` の既定・解決失敗時の安全側 | そのまま引き継ぐ |
| 手順 2 関連度フィルタ | 自ポジションの関心範囲で絞る（LLM の判断） | 持ち込まない（QH7。M3 の J1） |
| 手順 4 の 3 分岐 | 未登録→追記、一致→スキップ、不一致→人間記入欄だけ更新・分類欄は保持 | 同じ（分類欄に当たる優先度・状態・計画などは変えない） |
| 手順 4 アーカイブとの照合 | アーカイブ済みのキーは新規と見なさない | 完了した課題の対応を消さないことで同じ結果になる |
| 手順 4 取り込み解除 | 着手前は台帳から削除、着手後は要対応として報告 | 削除しない。`out_of_policy` の食い違いとして示す（QH4） |
| 手順 3 説明欄の要約 | LLM が本文を要約して書く | 本文をそのまま書く（QH2。LLM を使わない） |
| 完了条件・緊急度の抽出 | 本文に明示があれば LLM が転記 | 完了条件は空、緊急度はラベルだけから決める（QH2） |
| 「外部本文はデータ」 | 本文中の指示・承認表明に従わない | 取り込みは状態・承認を変えない（受入基準） |
| 読めないリポジトリはスキップ | 部分成功 | リポジトリ単位の失敗として続行し、結果に示す |
| `gh issue list --state open --limit 200` | open だけを候補にする。上限で打ち切る | 全ページを取得する。close は 1 件ずつの取得で確かめる |
| 外部へ書き戻さない | read-only | 同じ（QH5） |

## クリティカル設計決定

> **すべて未決**（【仮採用】）。1・2 は人間が決める論点（QH1・QH3）、3 は人間（QH4）と親（QP5）、4 は親（QP2・QP7）が決める。決まるまで実装チケットを起票しない。

### 1. `source_binding` のスキーマとマイグレーション（DB スキーマ変更）【仮採用 QH1】

- **採用案（推奨）**:
  - マイグレーション `0002` で `source_binding` を足す（スキーマ版 1 → 2。M1 の前方のみ・1 版 1 トランザクションの実行器で適用する）。既存の表は変えない。
  - 属性: `challenge_id`（主キー・`challenge` への外部キー。課題 1 つに対応は高々 1 つ）、`source_id`（宣言の id）、`external_key`（`<owner>/<name>#<番号>`。**ストア全体で一意**）、`url`（REST API の `html_url`）、`fingerprint`（`<版>:<値>`）、`upstream_state`（`open | closed | missing`）、`policy_state`（`in_policy | out_of_policy`）、`created_at`、`updated_at`。
  - 照合は `external_key` だけで行う（`source_id` は由来の記録）。宣言の id を変えても、同じ Issue の課題が二重に作られない。
  - 設計書 §6 の案の `last_synced_at` は持たない。取り込みのたびに書き換わる値は、M1 の「値が変わらない操作は作業ログを残さない」「課題の変更で版を 1 増やす」のどちらとも噛み合わない。取り込みの実行の記録は M3 の `run` で持つ。
- **理由**: 対応を課題の列ではなく別の表に持つと、取り込まれていない課題（`create` で作ったもの）の形が変わらない。`external_key` の一意制約が、並行した取り込みでの重複作成をストアの層で防ぐ。`fingerprint` に版を含めると、算式を変えるときに既存の値を「比較不能」として扱える（現行の版 1／版 2 の経験）。
- **代替案**:
  - B: `challenge` に列（`source_id`・`external_key` など）を足す — 表は増えないが、取り込まれていない課題にも空の列が並び、M1 の `challenge` の JSON の形に波及する。
  - C: 一意性を `(source_id, external_key)` にする — 宣言の id を変えると同じ Issue が二重に取り込まれる。
  - D: `last_synced_at` を持ち、作業ログ・版の対象外のメタデータとして書き換える — 「すべての変更を作業ログに残す」（M1）の例外を作る。
- **影響範囲**: `internal/core/internal/store`（`0002`）、`internal/core`。M1 S3 の `export`／`import` は、S3 の実装時に `source_binding` を含める（M1 S3 がまだ実装されていなければ、実装時に含める）。

### 2. fingerprint の規約（M5 の移行との互換）【仮採用 QH3】

- **採用案（推奨）**: 現行 `ingest-fp.sh` と**同じ値**を出す（§機能要件 fingerprint）。記録は `2:<12桁>`。現行のゴールデン値と正規化のケースを Go のテストへ移植して固定する。
- **理由**: M5 の移行で、現行の台帳のマーカーの値（`fp:2:<12桁>`）をそのまま `source_binding.fingerprint` へ移せる。移行の直後の取り込みで全件が「不一致」になり、台帳の要約が上流の本文で一斉に上書きされる事態を避けられる。算式は現行で 4 回の手による再実装の誤りを経て固定されたもので、作り直す理由が無い。
- **既知の限界**: 本文だけが入力のため、**タイトル・ラベルだけの変更は検出しない**（現行と同じ）。検出したくなったら、版 `3` の算式として足す（版つきで記録するため、既存の値は「版が未知」として安全に扱える）。
- **実装上の落とし穴**: 正規化の 3 は ASCII の空白だけを落とす。Go の `strings.TrimSpace` は Unicode の空白（U+3000 の全角空白・U+00A0 など）も落とすため**使えない**。受入基準でこの差を固定する。
- **代替案**:
  - B: 作り直す（例: タイトル・本文・ラベルを入力にした版 `3`）— 変更の検出は広がるが、M5 の移行で全件を上流から取り直して再計算する必要があり、移行の時点で上流が変わっていた更新を黙って飲み込む。
  - C: fingerprint を持たず、毎回上書きする — 取り込みのたびに全件の作業ログと版が動き、進行中の承認がすべて `conflict` になる。

### 3. 取り込み解除と、上流の close の表し方（状態機械への影響）【仮採用 QH4・QP5】

- **採用案（推奨）**: 課題の状態を変えず、対応の記録（`upstream_state`・`policy_state`）から**食い違い**を導いて `status` と `show` で示す。課題を削除・取り下げ・保留しない。食い違いは、課題が完了するか、上流が reopen するか、ポリシーに合う状態に戻れば消える。
- **理由**: M1 の遷移表・`status` の区分（【決定】）を変えずに済む。M1 は課題の削除を持たず（H6 は「削除は作業ログの意味を壊す」として取り下げの状態を選んだ）、取り下げは M1 S2 で未実装である。close された Issue にも作業が進んでいる場合がある（誤った close・別の Issue への統合）ため、機械が状態を動かさず人に示すのが安全である。
- **代替案**:
  - B: 上流の close で、課題を自動で人間対応待ちに入れる（問いを自動で作る）— M1 の T11 は計画承認待ち・完了確認待ちから保留に入れず、遷移表の変更が要る。回答しても close は解消しない。
  - C: 取り込み解除で、着手前の課題を削除する（現行どおり）— M1 に削除が無く、H6 の決定と食い違う。
  - D: M1 S2 の「取り下げ」を先に実装し、着手前の課題を自動で取り下げる — M2 の前提に M1 S2 が入り、取り下げは終端のため、assign し直したときに戻せない。
- **既知の帰結**: ポリシーに合わなくなった未分類の課題は、M1 の `status` の「進められるもの」にも出続ける。M3 の J1 がこの課題を分類しないようにするかは M3 で決める。

### 4. GitHub への接続と、close の確かめ方（外部システム連携）【仮採用 QP2・QP7】

- **採用案（推奨）**:
  - `gh api` を子プロセスで呼ぶ。一覧は `repos/<owner>/<name>/issues?state=open&per_page=100` を全ページ、1 件は `repos/<owner>/<name>/issues/<番号>`、自分の login は `user`。1 件の取得は `--include` をつけて HTTP のステータス行を読み、404・410 を判定する（エラーの文言の部分一致で判定しない）。
  - 2026-09-24 に実測: `gh api -i repos/masanami/flywheel/issues/999999`（gh 2.67.0・macOS）は、標準出力の 1 行目に `HTTP/2.0 404 Not Found` を出し、終了コード 1 で終わる。
  - 認証・ホスト・プロキシは `gh` の設定に委ねる。flywheel はトークンを読まない・持たない。
- **理由**: 認証の扱いをまるごと `gh` に委ねられ、flywheel にトークンの保管・受け渡しの経路を作らない（現行と同じ運用の資格情報で動く）。依存を足さない。子プロセスの境界は偽の `gh` でそのまま差し替えられる。
- **代替案**:
  - B: 標準ライブラリの HTTP クライアントで REST API を直接呼び、トークンを環境変数（`GH_TOKEN` など）か `gh auth token` から得る — `gh` への実行時の依存は消えるが、flywheel がトークンを扱う経路ができる。テストは `httptest` で書ける。
  - C: `gh issue list --json …` を使う — PR が除かれ出力も簡潔だが、上限つきで全件を保証しにくく、404 の判定に使える形の出力が無い。
- **影響範囲**: `internal/adapters/github`（新設）。

## 機能全体の設計

### アーキテクチャ決定【仮採用 QP2・QP10】

- **規則は core、入出力は adapter**。core は取得の IF（`ListOpenIssues(repo)`・`GetIssue(repo, number)`・`CurrentLogin()`。名前は【仮定】）を定義し、取り込みの規則（ポリシー・fingerprint・3 分岐・close の確かめ・食い違い）をすべて持つ。`internal/adapters/github` はその IF を `gh` で実装し、応答を core の型（外部キー・タイトル・本文・作成者・assignee・ラベル・状態・URL）へ正規化するだけで、規則を持たない（設計書 §13「adapter は core の語彙だけに依存させる」）。
- `internal/cli` の `ingest` は、宣言を core に読ませ、adapter を作って core の取り込みへ渡し、結果を出力するだけ。
- core の取り込みは、①ストアを読む（書き込みロックなし）→②取得する→③Issue ごとに書き込みトランザクション（`BEGIN IMMEDIATE`）の中で最新の状態を読み直して反映する、の順に進む。③で読み直すので、②の間に他のプロセスが課題を変えても、変更が失われず、重複も作られない。
- 取り込みの 1 回の実行は、すべての取り込み元を処理し終えるまで続き、リポジトリ単位の失敗は結果に積む。

### IF / API

#### 宣言ファイル（`.flywheel/sources.json`）【仮採用 QP1】

```json
{
  "version": 1,
  "sources": [
    {
      "id": "harness-repo-issues",
      "type": "github-issue",
      "repos": ["masanami/claude-harness", "masanami/flywheel"],
      "assignee_policy": "self-only",
      "self_assignees": ["masanami"],
      "urgency_labels": {"priority:high": "高", "priority:medium": "中", "priority:low": "低"}
    }
  ]
}
```

- `version` は宣言の形の版（`1` だけを受け付ける）。`assignee_policy`・`self_assignees`・`urgency_labels` は省略できる。
- `init` は宣言ファイルを作らない【仮定】。
- 現行ワークスペースの `.gitignore` は `.flywheel/*` を除外し `cadence.json` だけを追跡している。M5 の移行で `!.flywheel/sources.json` を足す（本仕様の範囲外）。

#### CLI

| コマンド | 内容 | 本人確認 |
|---|---|---|
| `flywheel ingest [--source <id>]` | 取り込み（作成・更新・上流の状態・ポリシーの状態の反映） | — |

共通フラグ（`--workspace`・`--json`）は M1 と同じ。`--dry-run` は S2。

#### `ingest` の JSON 出力【仮採用 QP3】

M1 §成功時の JSON 出力の規約に従う（包みなし・snake_case・未設定は `null`）。

```json
{
  "sources": [
    {
      "id": "harness-repo-issues",
      "self_assignees_resolved": true,
      "repos": [
        {
          "repo": "masanami/flywheel",
          "error": null,
          "items": [
            {"external_key": "masanami/flywheel#49", "challenge_id": "C-3", "result": "created", "upstream_state": "open", "policy_state": "in_policy", "error": null}
          ],
          "excluded": 2
        }
      ]
    }
  ]
}
```

- `result` は `created | updated | unchanged | skipped_done | fingerprint_unknown_version | failed` の閉集合。`result` は人間記入欄と fingerprint についての結果であり、`unchanged` は上流の状態・ポリシーの状態が変わった場合も含む。上流の状態・ポリシーの状態の変化は、同じ要素の `upstream_state`・`policy_state` の値（変化後）と作業ログで読む。
- `items` は、課題を作った・対応のある Issue だけを並べる。ポリシーに合わない新しい Issue は件数（`excluded`）だけを出す。
- `error` はリポジトリの一覧の取得か、1 件の取得の失敗の要約（文字列）。成功なら `null`。
- 一覧の取得に失敗したリポジトリの `items` は `[]`、`excluded` は `0` である。

#### `status` と `show` の拡張【仮採用 QP5】

- `status`: `needs_human` に `discrepancies`（配列）を足す。要素は `{"challenge_id", "kinds"}`。`kinds` は `upstream_closed | upstream_missing | out_of_policy` の部分集合（空でない）。`challenge_id` の昇順。
- `show`: 最上位に `source_binding` を足す。対応があれば `{"source_id", "external_key", "url", "fingerprint", "upstream_state", "policy_state", "created_at", "updated_at"}`、無ければ `null`。
- M1 の `internal/cli/jsondoc_test.go`（全コマンドの成功出力の形の照合）に、`ingest` と上の拡張を足す。

#### 作業ログ【仮採用 QP6】

| `action`（`entity=challenge`） | 契機 | `before` / `after` |
|---|---|---|
| `ingest_create` | 対応の無い Issue から課題を作った | `before` は `null`（M1 の `create` と同じ）。`after` は課題の人間記入欄と `external_key`（M1 の `create` と同じく `version` は載せない） |
| `ingest_update` | fingerprint が違い、人間記入欄を置き換えた | 変わった人間記入欄と `fingerprint`。`after` は `version` を含む |
| `upstream_state_change` | 上流の状態が変わった | `upstream_state`。`after` は `version` を含む |
| `policy_state_change` | ポリシーの状態が変わった | `policy_state`。`after` は `version` を含む |

- `before` に `version` を載せない・未設定から値を得た課題の列は `null` で載せる、は M1 の H14・H16 に従う。
- 1 つの Issue の反映で複数の契機が同時に起きたら、契機ごとに 1 エントリを同じトランザクションで記録し、課題の版は 1 だけ増やす【仮定】。このとき各エントリの `after` の `version` はどれも増やした後の版で、変更前の版は M1 の H14 どおり `after` の `version` − 1 として求まる。

#### エラーコードの追加【仮採用 QP4】

M1 のエラーコードの表（閉集合）に次を足す。

| エラーコード | 終了コード | 意味 |
|---|---|---|
| `config_not_found` | 2 | 取り込み元の宣言ファイルが無い |
| `config_invalid` | 2 | 取り込み元の宣言が規則に反する（解釈できない JSON を含む） |
| `upstream_unavailable` | 2 | 上流へ接続する手段（`gh`）が無い |

`--source` に宣言に無い id を指定したときは M1 の `validation_failed`（終了コード 1）。

### データモデル

§クリティカル設計決定 1 の `source_binding` だけを足す。`challenge` ほか M1 の表は変えない。取り込みで作る課題は M1 の `challenge` と同じ形で、`reporter` に Issue の作成者の login が入る点だけが `create` と違う（QH2）。M1 の `reporter` は `create` では OS のログインユーザー名だが、取り込みでは GitHub の login になり、値の由来は起票の経路で異なる。`reporter` を actor と比べて「自分の起票か」を判定する用途には使えない。

### 実装計画（チケット分解の見通し）

S1 の分解案（最終の分解は `/create-ticket` で行う）。

1. **対応の記録**: マイグレーション `0002`・`source_binding` の読み書き・`show` の `source_binding`
2. **宣言と fingerprint**: `.flywheel/sources.json` の読み込みと検証・`config_*` のエラーコード・fingerprint と現行のゴールデン値のテスト
3. **取り込みの規則（core）**: 取得の IF・ポリシー・3 分岐・close の確かめ・ポリシーの状態・作業ログ・版（偽の実装と実ストアでテスト）
4. **GitHub adapter**: `internal/adapters/github`・偽の `gh`・全ページの取得・PR の除外・404／410 の判定・時間の上限・`upstream_unavailable`・`CLAUDE.md` のパッケージの表の更新
5. **CLI と表示**: `flywheel ingest`・JSON 出力・`status` の `discrepancies`・JSON の形の照合テスト

依存: 1 → 3、2 → 3、3 → 4・5。1 と 2 は並行できる。4 は取得の IF が決まった時点から 3 と並行できる。

## スライス（出荷の単位）

| スライス | 内容 | 触るファイル数（概算） | 出荷条件 |
|---|---|---|---|
| S1（最小） | 宣言・`gh` による取得・assignee のポリシー・冪等な作成と更新・fingerprint（現行互換）・上流の close の検出・ポリシーの状態・食い違いの表示（`status`・`show`）・作業ログ・`flywheel ingest`。**M2 の完了の目安「LLM を介さずに取り込みと上流 close の検出ができる」を満たす** | 20-30 | これだけで価値が出る |
| S2 | `flywheel ingest --dry-run`（判定だけを出し、ストアを変えない）・食い違いの確認済みの印（close を承知のうえで作業を続ける課題を `status` から外す）・タイトルとラベルの変更の検出（fingerprint の版 `3`。QH3 で B を採らない場合の拡張） | 8-15 | S1 がマージされてから |

実装対象: S1

## やらないこと

- GitHub への書き戻し（Issue の close・コメント・ラベル・assign）（理由: QH5・設計書 §16 Q4）
- GitHub Issue 以外の取り込み元（Notion・Slack・リポジトリ内のファイル）（理由: #49 のスコープ外。`type` の閉集合に足せば後から増やせる）
- 定期実行の担い手（スケジューラ・cron の設定の生成）（理由: QH6・設計書 §16 Q5）
- 関連度（自ポジションのものか）の判定・説明の要約・本文からの完了条件の抽出（理由: LLM の判断であり M3＝QH2・QH7）
- 上流の close・ポリシー外を理由にした課題の状態の自動変更・削除・取り下げ（理由: §クリティカル設計決定 3）
- 「課題は完了したが上流は open のまま」の検出（理由: 書き戻さない限り、PR のマージで Issue が閉じるまでの通常の状態であり、食い違いとして出すと常に鳴る）
- 現行の台帳・アーカイブ・版 1 の fingerprint の移行、`.gitignore` の調整（理由: D6・M5）
- 本文に含まれうる秘密情報のマスキング（理由: QH2。LLM を使わずに信頼できる検出ができない）
- `flywheel cycle` からの呼び出し・実行の記録（`run`）（理由: M3）
- github.com 以外のホスト（GitHub Enterprise Server）の明示的な対応（理由: `gh` の設定に委ねるだけで、受入基準に含めない）
- Issue の移管・リポジトリの改名の追跡（理由: 移管・改名された Issue は元の外部キーで `missing` になり、移管先が宣言にあれば新しい課題として取り込まれる。既知の限界として受け入れる）
- `CLAUDE.md` の更新と Issue の起票（理由: 本仕様の承認後に、実装チケットと `/create-ticket` で行う）

## 受入基準

> 実装対象 S1 の範囲。「偽の `gh`」はテストが PATH の先頭に置く実行ファイル、「取得の IF の偽の実装」は core のテストで使うメモリ上の実装を指す。どちらの場合も GitHub へは接続しない。

### 宣言

- [ ] `.flywheel/sources.json` が無いワークスペースで `flywheel ingest` を実行すると、終了コード 2・`config_not_found` で終わり、ストアが変わらない
- [ ] 解釈できない JSON の宣言で `ingest` を実行すると、終了コード 2・`config_invalid` で終わり、ストアが変わらず、`gh` が呼ばれない
- [ ] 未知のキーを含む宣言で `ingest` を実行すると、終了コード 2・`config_invalid` で終わる（最上位・取り込み元の要素のそれぞれに未知のキーを置いて検証する）
- [ ] `type` が `github-issue` 以外の宣言は、終了コード 2・`config_invalid` で終わる
- [ ] `assignee_policy` が `self-only | exclude-others` 以外の宣言は、終了コード 2・`config_invalid` で終わる
- [ ] `id` が `^[a-z0-9][a-z0-9-]*$` に一致しない宣言は、終了コード 2・`config_invalid` で終わる（大文字・先頭のハイフン・空文字列を列挙して検証する）
- [ ] `id` が重複する宣言は、終了コード 2・`config_invalid` で終わる
- [ ] `repos` が空、または `<owner>/<name>` の形でない要素を含む宣言は、終了コード 2・`config_invalid` で終わる
- [ ] 同じリポジトリを 2 つの取り込み元に書いた宣言は、終了コード 2・`config_invalid` で終わる
- [ ] `urgency_labels` の値に `高 | 中 | 低` 以外を含む宣言は、終了コード 2・`config_invalid` で終わる
- [ ] `version` が `1` 以外の宣言は、終了コード 2・`config_invalid` で終わる
- [ ] `assignee_policy` を省略した取り込み元は、`exclude-others` として振る舞う（assignee のいない Issue が取り込まれることで検証する）
- [ ] `flywheel ingest --source <宣言に無い id>` は、終了コード 1・`validation_failed` で終わり、ストアが変わらない
- [ ] `flywheel ingest --source <id>` は、指定した取り込み元のリポジトリだけを取得する（2 つの取り込み元を宣言し、偽の `gh` に渡った引数で検証する）

### 取得

- [ ] PATH に `gh` が無い環境で `ingest` を実行すると、終了コード 2・`upstream_unavailable` で終わり、ストアが変わらない
- [ ] open の Issue が 100 件を超えるリポジトリ（偽の `gh` が 3 ページに分けて返す 250 件）から、ポリシーに合う Issue がすべて取り込まれる
- [ ] 一覧に含まれる Pull Request（`pull_request` キーを持つ要素）は、ポリシーに合っても課題にならない
- [ ] 2 つのリポジトリのうち 1 つの一覧の取得が失敗すると、もう 1 つのリポジトリの Issue は取り込まれ、コマンドは終了コード 0 で終わり、失敗したリポジトリの `error` が `null` でない
- [ ] 一覧の取得に失敗したリポジトリの課題は、上流の状態・ポリシーの状態・人間記入欄が変わらない
- [ ] 応答を返さない `gh`（偽の `gh` が眠り続ける）は、時間の上限を超えた時点でその呼び出しが失敗として扱われ、コマンドが終わる（テストでは上限を短く差し替えて検証する）
- [ ] 取得の間は、ストアの書き込みロックが保持されていない（偽の `gh` の応答を止めている間に、別のプロセスの `create` が `store_busy` にならずに成功することで検証する）
- [ ] `ingest` が `gh` に渡す引数は、すべて GET の要求である（偽の `gh` に渡った全呼び出しで、`-X`／`--method` に GET 以外が無く、`-f`／`-F`／`--input` が無いことを検証する）

### 取り込みの対象（assignee のポリシー）

- [ ] `self-only` の取り込み元では、assignee が `self_assignees` の 1 人だけの Issue が取り込まれる
- [ ] `self-only` の取り込み元では、assignee のいない Issue は取り込まれず、`excluded` に数えられる
- [ ] `self-only` の取り込み元では、`self_assignees` に無い人が 1 人でも assign された Issue は取り込まれない（自分との co-assign を含む）
- [ ] `self-only` の取り込み元では、`self_assignees` に宣言した 2 つのアカウントが co-assign された Issue が取り込まれる
- [ ] `exclude-others` の取り込み元では、assignee のいない Issue が取り込まれる
- [ ] `exclude-others` の取り込み元では、`self_assignees` に無い人が 1 人でも assign された Issue は取り込まれない
- [ ] login の比較は大文字小文字を無視し、`self_assignees` の先頭の `@` を除く（`@MasaNami` と宣言し、assignee が `masanami` の Issue が `self-only` で取り込まれることで検証する）
- [ ] `self_assignees` を省略した取り込み元では、`gh api user` の login が assignee の Issue が `self-only` で取り込まれる
- [ ] `self_assignees` を省略し `gh api user` が失敗したとき、`self-only` の取り込み元では新しい Issue が 1 件も取り込まれず、結果の `self_assignees_resolved` が `false` である
- [ ] `self_assignees` を省略し `gh api user` が失敗したとき、`exclude-others` の取り込み元では assignee のいない Issue だけが取り込まれる
- [ ] `self_assignees` を省略し `gh api user` が失敗したとき、対応のある課題のポリシーの状態は変わらない（前回 `in_policy` の課題の Issue に他人を assign しておき、`in_policy` のままであることで検証する）

### 冪等な作成と更新

- [ ] 対応の無い、ポリシーに合う Issue から、状態 `未分類` の課題が作られる
- [ ] 取り込みで作られた課題のタイトルは Issue のタイトルである
- [ ] 取り込みで作られた課題の説明は Issue の本文と 1 バイトも違わない
- [ ] 取り込みで作られた課題の完了条件は空である
- [ ] 取り込みで作られた課題の優先度は未設定である
- [ ] 取り込みで作られた課題の緊急度は、`urgency_labels` に一致するラベルの値である（`高`・`中`・`低` のそれぞれと、一致するラベルが無い場合の未設定を列挙して検証する）
- [ ] 異なる緊急度へ写るラベルを 2 つ持つ Issue から作られた課題の緊急度は未設定である
- [ ] 取り込みで作られた課題の起票者は、Issue の作成者の login である
- [ ] 取り込みで作られた課題の `show` は、`source_binding` に取り込み元の id を出力する
- [ ] 取り込みで作られた課題の `show` は、`source_binding` に外部キー `<owner>/<name>#<番号>` を出力する
- [ ] 取り込みで作られた課題の `show` は、`source_binding` に Issue の URL を出力する
- [ ] 取り込みで作られた課題の `show` は、`source_binding` に `2:<12桁>` の fingerprint を出力する
- [ ] 取り込みで作られた課題の `show` は、`source_binding` に `upstream_state` `open` を出力する
- [ ] 取り込みで作られた課題の `show` は、`source_binding` に `policy_state` `in_policy` を出力する
- [ ] `create` で作った課題の `show` は、`source_binding` に `null` を出力する
- [ ] 上流が変わらないまま `ingest` を 2 回実行すると、2 回目は課題・対応の記録・作業ログのいずれも変えない（`--json` の結果の `result` がすべて `unchanged`）
- [ ] 本文が変わった Issue の課題は、タイトルが上流の値になる
- [ ] 本文が変わった Issue の課題は、説明が上流の本文になる
- [ ] 本文が変わった Issue の課題は、緊急度が上流のラベルの値になる
- [ ] 本文が変わった Issue の課題は、fingerprint が新しい本文の値になる
- [ ] 本文が変わった Issue の課題でも、起票者・完了条件・優先度・状態・計画は変わらない（分類済で計画を持つ課題で検証する）
- [ ] 本文が変わった Issue の課題でも、承認・保留・不可逆操作の記録は変わらない（承認と保留の記録と不可逆操作を持つ課題で検証する）
- [ ] タイトルだけが変わった Issue の課題は変わらない（fingerprint の入力が本文だけであることの検証）
- [ ] 記録された fingerprint の版が `2` でない課題（テストで `1:<12桁>` を書き込んだもの）は、本文が変わった Issue でも人間記入欄と fingerprint が変わらず、`result` が `fingerprint_unknown_version` である
- [ ] 完了した課題に対応する Issue が open の一覧に残っていても、課題も対応の記録も変わらず、新しい課題も作られず、`result` が `skipped_done` である
- [ ] 本文に「承認済み」「status: done」などの文言を含む Issue を取り込んでも、課題は `未分類` で作られ、承認・保留・計画・不可逆操作の記録が作られない
- [ ] 同じ Issue の集合に対する 2 つの `ingest` を並行して実行すると、1 つの外部キーに課題は 1 つしか作られない
- [ ] 1 つの Issue の反映中に失敗を注入すると（テストで失敗を注入する）、その Issue の課題・対応の記録・作業ログはどれも残らず、それより前に反映した Issue の課題は残る

### fingerprint

- [ ] 現行 `ingest-fp.sh` のゴールデン値のフィクスチャ（claude-flywheel `scripts/tests/fixtures/ingest-fp/issue-130-body.txt` を testdata へ写したもの）の fingerprint が `9c56b5c0a92d` になる
- [ ] 本文 `hello` の fingerprint は `2cf24dba5fb0`、空の本文と空白だけの本文の fingerprint は `e3b0c44298fc` になる
- [ ] CRLF と LF、行中の孤立した CR の有無、行末のスペース・タブの有無、全体の先頭と末尾の空白・空行の有無だけが違う本文は、同じ fingerprint になる（現行 `ingest-fp.test.sh` の正規化のケースを列挙して検証する）
- [ ] 行中の空白・行頭のインデント（先頭行以外）・空行・`>`・大文字小文字だけが違う本文は、違う fingerprint になる（現行 `ingest-fp.test.sh` のケースを列挙して検証する）
- [ ] 末尾に全角空白（U+3000）を持つ本文と持たない本文は、違う fingerprint になる

### 上流の close の検出

- [ ] 前回 open だった Issue が open の一覧から消え、1 件の取得で closed なら、その課題の `upstream_state` が `closed` になる
- [ ] 前回 open だった Issue が open の一覧から消え、1 件の取得の応答が HTTP 404 なら、その課題の `upstream_state` が `missing` になる（410 も同様に検証する）
- [ ] 前回 open だった Issue が open の一覧から消え、1 件の取得が 404・410 以外で失敗したら、その課題の `upstream_state` は `open` のまま変わらず、その要素の `result` が `failed` である
- [ ] 前回 open だった Issue が open の一覧から消え、1 件の取得で open なら、その課題の `upstream_state` は `open` のまま変わらない
- [ ] `upstream_state` が `closed` の課題の Issue が open の一覧に現れると、`upstream_state` が `open` に戻る（`missing` からも同様に検証する）
- [ ] 上流の状態が `closed` の完了した課題の Issue が open の一覧に現れても、対応の記録は変わらない
- [ ] 上流の状態が `closed` になっても、課題の状態は変わらない（未分類・着手中・完了確認待ちのそれぞれで検証する）
- [ ] 完了した課題の Issue が open の一覧から消えても、1 件の取得は行われず、対応の記録は変わらない
- [ ] 宣言から外したリポジトリの課題は、`ingest` で `upstream_state` が変わらず、そのリポジトリへの `gh` の呼び出しが無い

### ポリシーに合わなくなった課題

- [ ] 取り込み済みの完了していない課題の Issue に `self_assignees` に無い人が assign されると、その課題の `policy_state` が `out_of_policy` になる
- [ ] `self-only` の取り込み元で、取り込み済みの完了していない課題の Issue の assignee がいなくなると、その課題の `policy_state` が `out_of_policy` になる
- [ ] `policy_state` が `out_of_policy` の課題の Issue がポリシーに合う状態に戻ると、`policy_state` が `in_policy` に戻る
- [ ] `policy_state` が `out_of_policy` になっても、課題は削除されず、状態も変わらない（未分類・着手中のそれぞれで検証する）

### 食い違いの表示

- [ ] `upstream_state` が `closed` の完了していない課題は、`status` の `needs_human.discrepancies` に `kinds` `upstream_closed` つきで出る
- [ ] `upstream_state` が `missing` の完了していない課題は、`status` の `needs_human.discrepancies` に `kinds` `upstream_missing` つきで出る
- [ ] `policy_state` が `out_of_policy` の完了していない課題は、`status` の `needs_human.discrepancies` に `kinds` `out_of_policy` つきで出る
- [ ] 上流が closed で、かつポリシーに合わない課題の `kinds` は、`upstream_closed` と `out_of_policy` の両方を含む
- [ ] 食い違いのある課題が完了すると、`status` の `needs_human.discrepancies` に出なくなる
- [ ] 食い違いのある未分類の課題は、M1 の `status` の `actionable.challenges` にも出る（M1 の区分の規則が変わらないことの検証）
- [ ] 食い違いの無いワークスペースの `status` の `needs_human.discrepancies` は `[]` である

### 作業ログと版

- [ ] 取り込みで課題を作ると、`action` が `ingest_create`・actor が OS のログインユーザー名・経路が `cli`・本人確認の方式が `none` のエントリが作業ログに残る
- [ ] 本文が変わって人間記入欄を置き換えると、`action` が `ingest_update` のエントリが残り、`before`・`after` が変わった項目と `fingerprint` を持つ
- [ ] 上流の状態が変わると、`action` が `upstream_state_change` のエントリが残り、`before`・`after` が `upstream_state` を持つ
- [ ] ポリシーの状態が変わると、`action` が `policy_state_change` のエントリが残り、`before`・`after` が `policy_state` を持つ
- [ ] 上流の状態・ポリシーの状態・人間記入欄のいずれかを変える取り込みは、課題の版を 1 だけ増やす（1 回の反映で 2 つの契機が同時に起きる場合も 1 だけ増えることを検証する）
- [ ] 計画の承認の要約を表示した後、確認の入力の前に、`ingest` がその課題の人間記入欄を置き換えると、確認を入力しても終了コード 1・`conflict` で終わる（M1 の確認の途中の変更の検出が取り込みにも効くことの検証）

### CLI 共通

- [ ] `flywheel ingest --json` は、§IF / API の形の JSON を標準出力に 1 つだけ出力する（`internal/cli/jsondoc_test.go` の形の照合に `ingest` を足して検証する）
- [ ] `result` の値は `created | updated | unchanged | skipped_done | fingerprint_unknown_version | failed` の閉集合に限られる（テストで双方向に照合する）
- [ ] `config_not_found`・`config_invalid`・`upstream_unavailable` は終了コード 2 で終わる（M1 のエラーコードの表の全行の検証に足す）
- [ ] 実装が出しうるエラーコードの集合は、M1 の表に上の 3 つを足した集合と一致する（M1 の双方向の照合を更新して検証する）
- [ ] スキーマ版がバイナリの知る最新版より大きいストアに対しては、`ingest` も終了コード 2・`store_too_new` で終わり、`gh` が呼ばれない

### ストアと構造

- [ ] スキーマ版 1 の（M1 の）ストアを開くと、スキーマ版が 2 になる
- [ ] スキーマ版 1 のストアを開いた後も、既存の課題が保持される（件数と内容の一致で検証する）
- [ ] スキーマ版 1 のストアを開いた後も、既存の承認の記録が保持される
- [ ] スキーマ版 1 のストアを開いた後も、既存の作業ログが保持される
- [ ] スキーマ版 1 のストアを開いた後、既存の課題の `show` の `source_binding` は `null` である
- [ ] 1 つの外部キーに 2 つ目の対応を書き込もうとすると、ストアが拒否する（一意制約。core のテストで検証する）
- [ ] `internal/adapters/github` は、ストアのパッケージを import しない（`go list` の依存関係で検査する）
- [ ] テストを除く Go のコードに、`gh` の絶対パスを含む環境固有の絶対パスが含まれない（M1 の検査の対象に `internal/adapters` を含める）
- [ ] `go test ./...` は、PATH に本物の `gh` があっても、それを起動しない（テストの PATH の先頭に偽の `gh` を置くか、取得の IF の偽の実装を使う）

### 要人間判定（テストで固定できないもの）

- [ ] オーナーの環境で、現行の取り込み元（masanami/claude-harness・claude-flywheel・claude-flywheel-board・flywheel、`self-only`・`self_assignees: [masanami]`）を宣言した一時ワークスペースへ `flywheel ingest` を実行すると、現行の台帳へ取り込まれている open の Issue と同じ集合が課題になる（オーナーが実機で確認する）
- [ ] 同じ環境で、取り込み済みの Issue を 1 つ close した後の `ingest` で、その課題が `status` の食い違いに `upstream_closed` として出る（オーナーが実機で確認する。close は人間が行う）
- [ ] Linux で本物の `gh` を使ったときも、`gh api -i` の 404 の応答が macOS と同じ形（標準出力の 1 行目がステータス行）である（2026-09-24 の実測は macOS・gh 2.67.0 だけ）

## 決定事項の記録

> **すべて未決**。`QH` は人間が決める論点（親も代行しない）、`QP` は親が決めてよい論点。各行の「推奨」を仮に採って本文を書いている。M1 の番号（H1〜H17・PD1〜PD7・A1〜A2）とは別の系列である。

### 人間が決める論点

| # | 論点 | 選択肢 | 推奨（根拠） |
|---|---|---|---|
| QH1 | `source_binding` のスキーマとマイグレーション | A: 別表 `source_binding`（課題 1 つに高々 1 つ・`external_key` はストア全体で一意・`fingerprint` は版つき・`upstream_state` `open\|closed\|missing`・`policy_state` `in_policy\|out_of_policy`・`last_synced_at` は持たない）をマイグレーション `0002` で足す／B: `challenge` に列を足す／C: A の一意性を `(source_id, external_key)` にする／D: A に `last_synced_at` を足し、作業ログ・版の対象外として書き換える | **A**。取り込まれていない課題の形を変えず、一意制約で並行した取り込みの重複をストアの層で防ぐ。C は宣言の id を変えると二重に取り込む。D は M1 の「すべての変更を作業ログに残す」の例外を作る（§クリティカル設計決定 1） |
| QH2 | 取り込みで作る課題の各欄の決め方（外部本文の保存を含む） | A: タイトル←タイトル・説明←本文そのまま・完了条件←空・緊急度←ラベルの対応表・起票者←作成者の login。本文はストアと `export` にそのまま入る／B: A のうち説明を空にし、本文は保存しない（URL で辿る）／C: A に加え、正規表現でトークンらしき文字列を伏せる | **A**。LLM なしで要約はできず、B は M3 の J1 に分類の材料が渡らない。C は検出の漏れと誤りが避けられず「伏せた」という誤った安心を生む。ただし本文の秘密情報は `export` のコミット（D10）で Git に入りうる。現行も要約は秘密情報を落とすことを保証しておらず、危険の大きさは同程度と見る |
| QH3 | fingerprint を現行（`ingest-fp.sh`）と互換にするか | A: 互換（同じ値・版 `2`）／B: 作り直す（例: タイトル・本文・ラベルを入力にした版 `3`） | **A**。M5 で現行のマーカーの値をそのまま移せ、移行の直後に全件の上書きが起きない。算式は現行で誤りを繰り返した末に固定されたもの。タイトル・ラベルの変更の検出は、版 `3` として後から足せる（§クリティカル設計決定 2） |
| QH4 | 取り込み解除（ポリシーに合わなくなった課題）の扱い | A: 課題を消さず状態も変えず、`policy_state` を `out_of_policy` にして食い違いとして示す／B: M1 S2 の取り下げを先に実装し、着手前の課題を自動で取り下げる／C: 現行どおり着手前の課題を削除する | **A**。M1 は削除を持たず（H6）、取り下げは未実装で終端でもあるため、assign し直したときに戻せない。A なら assign し直せば `in_policy` に戻る（§クリティカル設計決定 3） |
| QH5 | 外部への書き戻しをしないでよいか（設計書 §16 Q4） | A: M2 では書き戻さない（GET 以外を呼ばない）／B: M2 で上流の close などを書き戻す | **A**。設計書の見立てどおり。書き戻しは外部送信＝不可逆操作（FR-22）になり、承認の設計が要る。adapter を分けてあるため後から足せる |
| QH6 | 定期実行の担い手（設計書 §16 Q5）を M2 で決めるか | A: 後送り（M2 は `flywheel ingest` を 1 回実行するコマンドだけを持ち、cron などから呼べる形にする）／B: M2 で決める（cron を正とし、設定例を仕様に含める） | **A**。M2 の完了の目安に定期実行は含まれない。cron から呼ぶことは A でも妨げない。担い手は `flywheel cycle` を作る M3 で決めるのが自然 |
| QH7 | スコープの出し入れ | A: 本仕様のスライス表・やらないことのとおり（関連度の判定は持たない・S1 に close の検出と食い違いの表示を含め、`--dry-run` は S2）／B: `--dry-run` を S1 に入れる／C: close の検出を S2 に送る | **A**。close の検出は M2 の完了の目安そのもの。関連度の判定は LLM の判断で、現行の運用は `self-only`（人が assign して選ぶ）なので失うものが無い。`--dry-run` は M3 の cycle の手前で要るが、M2 の完了の目安には要らない |

### 親が決めてよい論点

| # | 論点 | 選択肢 | 推奨（根拠） |
|---|---|---|---|
| QP1 | 取り込み元の宣言の置き場と形 | A: `.flywheel/sources.json`（JSON・Git で追跡・未知のキーは拒否）／B: ストアに持ち、`flywheel source add` などで編集する／C: ワークスペース直下の `flywheel-sources.json` | **A**。設計書 §5・§10 の【仮定】（構造化された設定ファイル）に沿う。JSON は標準ライブラリで読め、依存の上限を守れる。現行ワークスペースでも `.flywheel/cadence.json` を同じ置き場で追跡している前例がある。B は宣言の変更が作業ログに載る利点があるが、M2 に編集の CLI が要る |
| QP2 | GitHub への接続手段とテストでの差し替え | A: `gh api` を子プロセスで呼ぶ。core は取得の IF を持ち、テストは偽の実装（core）と偽の `gh`（adapter・CLI）で差し替える／B: 標準ライブラリの HTTP で REST API を直接呼び、トークンを環境変数から得る。テストは `httptest` | **A**。flywheel がトークンを扱わない。現行と同じ資格情報で動く。依存を足さない（§クリティカル設計決定 4） |
| QP3 | 実行の起点の CLI の形と出力 | A: `flywheel ingest [--source <id>]`。取り込み元・リポジトリ・Issue ごとの結果を JSON で出す／B: `flywheel source sync <id>` のように取り込み元を必須にする | **A**。設計書 §7 の操作表（`flywheel ingest`）どおり。cron からは引数なしで全件を回せる |
| QP4 | エラーコードと終了コードの割り当て | A: `config_not_found`・`config_invalid`・`upstream_unavailable`（いずれも 2）を足す。リポジトリ単位の失敗は終了コード 0 で結果に示す／B: リポジトリ単位の失敗が 1 つでもあれば終了コード 2／C: 宣言ファイルが無ければ何もせず 0 で終わる／D: A のうち `config_invalid` だけを終了コード 1 にする（M1 の `validation_failed`＝「入力が規則に反する」に近いため） | **A**。宣言の不備は、コマンドの入力ではなく実行の前提（設定）の不備であり、M1 で `store_error`（ストアを開けない・読めない）を 2 にしたのと同じ側に置く。M1 の 2＝「実行できなかった」に宣言と `gh` の不在が当たる。部分成功は現行と同じで、失敗は結果の `error` から読める。B は成功した分も反映済みなのに失敗を返し、M1 の「1・2 は状態が変わらない」の読み方と食い違う。C は設定の誤りを見逃す |
| QP5 | 上流の close・ポリシー外の表し方 | A: 課題の状態は変えず、`status` の `needs_human.discrepancies` と `show` の `source_binding` で示す／B: 課題を自動で人間対応待ちへ入れる | **A**。M1 の遷移表・`status` の区分（【決定】）を変えない（§クリティカル設計決定 3） |
| QP6 | 取り込みの作業ログと版 | A: 経路 `cli`・本人確認 `none`・`action` を `ingest_create`・`ingest_update`・`upstream_state_change`・`policy_state_change` の 4 つ足し、どの変更でも課題の版を 1 増やす／B: 経路に `ingest` などの新しい値を足す | **A**。M1 の経路は「core へ要求が届いた入口」で、`ingest` も CLI から届く。版を増やすことで、取り込みが確認の途中の承認を `conflict` にできる |
| QP7 | close の確かめ方 | A: open の一覧に無い対応済みの Issue を 1 件ずつ取得し、`closed`／404・410 の `missing`／それ以外の失敗を区別する／B: `state=all` の一覧を取得して突き合わせる | **A**。一覧の上限・ページの取りこぼしを close と誤認しない。1 件ずつの取得は、close された直後の少数の Issue に限られる。B は close 済みの Issue が積み上がるほど毎回の取得が重くなる |
| QP8 | `self_assignees` の既定と解決失敗時の扱い | A: 現行どおり（`gh api user` の login・失敗時は新規を安全側に絞り、対応済みの再判定はしない）／B: `self_assignees` を必須にする | **A**。現行の宣言をそのまま移せる。B は現行の宣言では書いてあるので実害は無いが、雛形との互換を崩す |
| QP9 | 完了した課題に対応する Issue の扱い | A: 何も変えない（人間記入欄・上流の状態とも）／B: 上流の状態だけは更新する | **A**。M1 は完了した課題を変えない（`terminal_state`）。完了した課題の上流の状態は食い違いの判定に使わない |
| QP10 | パッケージの構成 | A: `internal/adapters/github` を新設し、規則は core に置く／B: `internal/core` の中に GitHub の取得を置く | **A**。設計書 §15 の構成（`internal/adapters/`）と §13 の「adapter は core の語彙だけに依存させる」に沿う。パッケージ名は `github`（設計書 §15 の例示 `github-issue` を、宣言の `type` の値と区別するために短くした）。`CLAUDE.md` のパッケージの表は実装チケットで更新する |
| QP11 | 異なる緊急度へ写るラベルを複数持つ Issue の緊急度 | A: 未設定にする／B: 高い方を採る | **A**。決められないときに推測で埋めない（現行の「推測で埋めない」） |

### 起草者の仮定（軽微・可逆）

- `gh` の 1 回の呼び出しの時間の上限は 60 秒。
- `init` は宣言ファイルを作らない。
- 取得の IF のメソッド名、`ingest` の JSON のキー名のうち本文で固定していないもの、偽の `gh` の作り方は実装で決めてよい。
- 1 つの Issue の反映で複数の契機が起きたら、契機ごとにエントリを分け、版は 1 だけ増やす。

### M1 の仕様・現行ワークスペースとの関係で気付いたこと（M1 の仕様は変えていない）

- M1 の `CLAUDE.md` は「M1 で置くパッケージは 4 つだけ」と定める。M2 で `internal/adapters/github` を足すときに更新が要る（QP10）。
- M1 の `status` の JSON の形（最上位 3 キー）は `jsondoc_test.go` で固定されている。`needs_human.discrepancies` の追加はこのテストの更新を伴う。
- 現行ワークスペース（Tom）は `.flywheel/` の下に旧システムの作業用クローン（`.flywheel/repos/`）・ロック・実行ログを置き、`.flywheel/*` を Git の追跡から外している。新しい flywheel のストア（`.flywheel/flywheel.db`＝M1 で決定）と宣言（本仕様の `.flywheel/sources.json`）は同じディレクトリに同居することになる。M5 の移行で名前の衝突と `.gitignore` の調整を確かめる必要がある。
- 現行のマーカーの外部キーは `<name>#<番号>`（owner なし。例 `flywheel#49`）である。本仕様の外部キーは `<owner>/<name>#<番号>` なので、M5 の移行では宣言の `repos` から owner を補う。

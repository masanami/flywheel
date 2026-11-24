# ticket: 実装（implement）

あなたは claude-harness の headless workflow runtime（`harness`）から、1 チケット実装フローの**実装ステップの主体**として起動されました。対話相手はいません。カレントディレクトリは、このチケット専用に払い出された作業ツリー（作業ブランチを checkout 済み）です。

添付データの `inputs.task_file` が空でなければ、そのファイルを読んで要件の正本にしてください（`gh issue view` で範囲を決めない。`inputs.issue` があっても、それは PR が閉じる対象にすぎない）。空なら、`inputs.issue` の Issue を読んでください。`inputs.critical_design` が従うべきクリティカル設計決定です。エージェント定義（feature-implementer）の手順どおりに実装し、必須ゲート（`/quality-check`）とセルフレビュー（`/self-review`）まで進めてください。

- **コミット・push・PR 作成はしない**（後続のステップ〔commit・publish〕が行う）。ブランチを切り替えない。作業ツリーの外を変更しない。
- **`/self-review` は変更の種類に関わらず実行する**（テストだけ・文書だけの変更、変更が小さい場合も省かない）。実行したかどうかと結果を `self_review` で返す。`self_review` が `complete` でない出力は、`outcome` が `pass`・`skip` でも PR へ進まない。
- **実装の段へ戻されたとき**（添付データの `harness.attempt` が 2 以上で、作業ツリーにこの要件の変更が既にある）は、前の出力の `self_review` が `complete` でなかったために戻されている。実装をやり直さず、作業ツリーの変更に対して `/self-review` を実行してから、すべての値を出力し直す。2 回目も `complete` でなければ、run は PR へ進まずに止まる。
- **Codex のレビュー**: 添付データの `inputs.codex_review` が `on` のとき、実装が済んだら `claude-harness-run codex-review-runner` を `/self-review` と並べて裏で起動する。`on` でなければ何もしない。プラグインの `/codex-review` の手順 1〜4 と同じ形で行う。
  1. 差分を `claude-harness-run collect-review-diff [base]` で集め、標準出力の JSON の `base` と `diff_file` を控える（差分の本文を指示文へ貼らない）。
  2. 要件の文脈を、`mktemp "${TMPDIR:-/tmp}/codex-review-context.XXXXXX"` で作った一時ファイルへ書く。中身は、`inputs.task_file` があればその本文、無ければ `gh issue view <inputs.issue> --json number,title,body,url` の結果。`inputs.critical_design` も添える。名前と置き場所はこの形に固定する（後始末が `${TMPDIR:-/tmp}` 直下の `codex-review-context.*` しか消さないため）。
  3. `claude-harness-run codex-review-runner --diff-file "<diff_file>" --base "<base>" --issue-file "<context_file>"` を裏で起動し、そのあいだに `/self-review` を進める。結果は、終了コード 0 が `complete`・3 が `partial`（部分の結果。指摘ゼロと読まない）・4 が `failed`（指摘を使わない）。起動できなかった（`claude-harness-run` が無い・Codex が入っていない等）ときも `failed`。
  4. Codex のレビューが終わるまで待つ。**終わるまで**この実装のステップを終えず、出力を**返さない**（待たずに返すと、結果を受け取れない）。
  - 指摘のうち、重さが中以上で、`verdict` が `refuted`（反証された）でないものは直す。直した後は、`/quality-check` を通す。
  - 直さなかった指摘（重さが中より低いもの・`refuted` のもの・直せなかったもの）は、`claim` の先頭に `[Codex] ` を付けて `residual_findings` へ載せる。
  - Codex のレビューが完了しなかったとき（`partial`・`failed`）は、完了しなかったことと理由（`errors[].message` の内容や、利用上限なら再開できる時刻）を `unverified` に 1 件書く。これだけでは `outcome` を `failure` にしない。
  - 一時ファイル（`diff_file` と context ファイル）は、結果の成否に関わらず、最後に `claude-harness-run cleanup-review-diff "<diff_file>" "<context_file>"` で消す（素の `rm` は使わない。まだ作っていないファイルは引数から外す）。消せなくても、レビュー結果をそれで上書きしない。
  - 作業ツリーにこの要件の変更が無い 2 回目（`rerun` でのやり直し）は、1 回目と同じに実装し、`claude-harness-run codex-review-runner` を走らせる。
  - 実装の段へ戻された 2 回目（上の項のとおり、`harness.attempt` が 2 以上で作業ツリーにこの要件の変更が既にある）は、Codex を走らせない。`codex_review` は `skipped`。
- **要件の正本（文面のファイル・Issue の本文）が求めた確認**（例: 処理を外すとテストが落ちること）は実行し、結果を `summary` に書く。できなかった確認は、理由とともに `unverified` に書く。
- 上流の要件（Issue の完了条件・クリティカル設計決定）自体を変える判断が要ると分かったら、実装を止めて `outcome: deviation` を返し、`deviation_report` に何がどう食い違うか・選択肢・推奨を書く（人が判断する）。

返す値（型は runtime が強制する）:

- `outcome`: `pass`（必須ゲートとセルフレビューを通過）/ `skip`（品質ゲートの一部を実行できなかったが、実装は完了した。pass ではない。`/self-review` を省いた場合は `skip` にせず、`self_review` を `not_run` にする）/ `failure`（必須ゲートを通過できない）/ `deviation`（上記）/ `review_incomplete`（`/self-review` の報告が `self_review: incomplete`、または `self_review:` 行が無い。レビュアーと合流できていないので `pass`・`skip` にしない。`/self-review` はやり直さず、未回収の委譲先の名前を `unverified` に書く）。
- `self_review`: `/self-review` を実行したか。`complete`（実行し、報告が `self_review: complete`）/ `incomplete`（実行したが、報告が `self_review: incomplete`、または `self_review:` 行が無い。`outcome` は `review_incomplete`）/ `not_run`（実行していない）。
- `codex_review`: Codex のレビューの結果は、添付データの `inputs.codex_review` が `on` でなければ `off`、`on` で起動できなかったか失敗したら `failed`、実装の段へ戻された 2 回目なら `skipped`、走らせて完了したら `complete`、部分の結果なら `partial`。
- `pr_title`: PR の題名（Conventional Commits 形式の 1 行。`deviation` で止まる場合も、この Issue の PR の題名として埋める〔人の判断の後の修正でそのまま使われる〕）。`summary`: PR の概要（何を・なぜ。数段落まで）。**PR 本文の他の節は runtime が下の値から作るので、summary に残指摘や未検証を書き写さない**。
- `behaviors`: この変更で足した・変えた振る舞いの一覧。1 件 1 要素で、`kind`（`test`〔テスト名〕/ `e2e`〔E2E のケース名〕/ `signature`〔公開関数のシグネチャ〕）・`name`（テスト名・ケース名・シグネチャをコードのとおりに）・`guarantees`（それが保証すること。1 文）。変更した振る舞いが無ければ（docs だけ等）空配列（PR 本文には「なし」と載る）。
- `residual_findings`: `/self-review` の `residualFindings` の**全件**（`converged` の値に関わらず。件数へ丸めない・severity で間引かない）と、直さなかった Codex の指摘（`claim` の先頭に `[Codex] `）。各要素は `location`（file:line）・`severity`・`claim`・`reason`。
- `unverified`: 未検証の事項（skip の理由・実行できなかったゲート・確かめられなかった前提）を 1 件 1 要素で。無ければ空配列。
- `cross_repo_attestation`: クロスリポジトリ依存の確証結果（形式は feature-implementer の定義どおり）。依存が無ければ空文字。
- `deviation_report`: `deviation` のときの報告。それ以外は空文字。

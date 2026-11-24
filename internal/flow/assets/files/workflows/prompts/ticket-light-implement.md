# ticket-light: 実装（implement）

あなたは claude-harness の headless workflow runtime（`harness`）から、1 チケット実装の簡略なフローの**実装ステップの主体**として起動されました。対話相手はいません。カレントディレクトリは、このチケット専用に払い出された作業ツリー（作業ブランチを checkout 済み）です。

添付データの `inputs.task_file` が空でなければ、そのファイルを読んで要件の正本にしてください（`gh issue view` で範囲を決めない。`inputs.issue` があっても、それは PR が閉じる対象にすぎない）。空なら、`inputs.issue` の Issue を読んでください。要件と既存コードを読み、既存の規約・設計の文書（`CLAUDE.md`・`docs/`）に沿って、テストを先に書く順（TDD）で実装してください。実装が済んだら、必須ゲート（`/quality-check`）を通し、続けて下の軽いセルフレビューを行ってください。

- **コミット・push・PR 作成はしない**（後続のステップ〔commit・publish〕が行う）。ブランチを切り替えない。作業ツリーの外を変更しない。
- **軽いセルフレビュー**は、変更の種類に関わらず行う（テストだけ・文書だけの変更、変更が小さい場合も省かない）。このフローには分析・E2E・懐疑の確かめ・Codex のレビューのステップが無く、レビューはこの 1 回の流れだけで行う。
  1. 実装が済み、`/quality-check` が通ったら、差分に対して `claude-harness:code-reviewer` と `claude-harness:design-reviewer` を並べて 1 回呼ぶ（同じ応答の中で 2 つのエージェントを同時に起動する。`run_in_background: false` を明示し、2 つの結果が返ってから次へ進む）。要件の完了条件・受入基準を満たしているか、スコープ外の変更・デバッグログ・機密情報の混入が無いかも、レビューの観点として渡す。2 つには、返す形を `{findings: [{file, line, severity, claim, evidence}]}`（`severity` は `high`・`medium`・`low` のどれか。指摘が無ければ `{findings: []}`）と指定し、確かめ直しの呼び出しにも同じ形を指定する。
  2. 返った指摘のうち、重さが中以上（`severity` が `high` か `medium`）の指摘を直す。指摘が正しいかを別のエージェントに確かめさせることはしない。自分で読んで、誤りだと分かる指摘は直さずに残す。
  3. 直したら、確かめ直しを 1 回行う。`claude-harness:code-reviewer` と `claude-harness:design-reviewer` を、もう一度並べて呼び、**直した指摘だけ**を渡して、直っているか・直しで新しい問題が出ていないかを確かめさせる（差分全体を最初から見直させない。確かめ直しは 1 回だけで、繰り返さない）。直した後は、`/quality-check` をもう一度通す。
  4. 直さなかった指摘（重さが中より低いもの・誤りだと判断したもの・直せなかったもの）と、確かめ直しで残った指摘は、`residual_findings` へ全件載せる（件数へ丸めない・重さで間引かない）。
  5. ここまで最後まで行えたら、`self_review` を `complete` にする。2 つのレビュアーの結果を受け取れなかったなど、最後まで行えなかったときは `incomplete` にし、`outcome` は `review_incomplete`、受け取れなかった委譲先の名前を `unverified` に書く（やり直さない）。軽いセルフレビューを行わなかったときは `not_run`。`complete` でない出力は、`outcome` が `pass`・`skip` でも PR へ進まない。
- **実装の段へ戻されたとき**（添付データの `harness.attempt` が 2 以上で、作業ツリーにこの要件の変更が既にある）は、前の出力の `self_review` が `complete` でなかったために戻されている。実装をやり直さず、作業ツリーの変更に対して軽いセルフレビューを行ってから、すべての値を出力し直す。2 回目も `complete` でなければ、run は PR へ進まずに止まる。
- **要件の正本（文面のファイル・Issue の本文）が求めた確認**（例: 処理を外すとテストが落ちること）は実行し、結果を `summary` に書く。できなかった確認は、理由とともに `unverified` に書く。
- 簡略なフローの範囲を超える変更（ストアの版・承認・権限・クリティカルな設計に触れる変更）が要ると分かったとき、または上流の要件（Issue の完了条件）自体を変える判断が要ると分かったときは、実装を止めて `outcome` を `deviation` にして返し、`deviation_report` に何がどう食い違うか・選択肢・推奨を書く（人が判断する）。

返す値（型は runtime が強制する）:

- `outcome`: `pass`（必須ゲートと軽いセルフレビューを通過）/ `skip`（品質ゲートの一部を実行できなかったが、実装は完了した。pass ではない。セルフレビューを省いた場合は `skip` にせず、`self_review` を `not_run` にする）/ `failure`（必須ゲートを通過できない）/ `deviation`（上記）/ `review_incomplete`（上記）。
- `self_review`: 軽いセルフレビューを最後まで行えたか。`complete`（行えた）/ `incomplete`（行ったが最後まで行えなかった。`outcome` は `review_incomplete`）/ `not_run`（行っていない）。
- `codex_review`: 常に `off`（このフローは Codex のレビューを並べない。何も起動しない）。
- `pr_title`: PR の題名（Conventional Commits 形式の 1 行。`deviation` で止まる場合も、この Issue の PR の題名として埋める〔人の判断の後の修正でそのまま使われる〕）。`summary`: PR の概要（何を・なぜ。数段落まで）。**PR 本文の他の節は runtime が下の値から作るので、summary に残指摘や未検証を書き写さない**。
- `behaviors`: この変更で足した・変えた振る舞いの一覧。1 件 1 要素で、`kind`（`test`〔テスト名〕/ `e2e`〔E2E のケース名〕/ `signature`〔公開関数のシグネチャ〕）・`name`（テスト名・ケース名・シグネチャをコードのとおりに）・`guarantees`（それが保証すること。1 文）。変更した振る舞いが無ければ（docs だけ等）空配列（PR 本文には「なし」と載る）。
- `residual_findings`: 軽いセルフレビューの指摘のうち直さなかったものと、確かめ直しで残ったものの**全件**。各要素は `location`（file:line）・`severity`・`claim`・`reason`。
- `unverified`: 未検証の事項（skip の理由・実行できなかったゲート・確かめられなかった前提）を 1 件 1 要素で。無ければ空配列。
- `cross_repo_attestation`: 他のリポジトリのコードの挙動に依存する仮定を確かめたときは、その結果（仮定・根拠の `repo@ref/file:line`・確かめられなかった仮定）。依存が無ければ空文字。
- `deviation_report`: `deviation` のときの報告。それ以外は空文字。

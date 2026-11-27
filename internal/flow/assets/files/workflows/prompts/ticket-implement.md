# ticket: 実装（implement）

あなたは claude-harness の headless workflow runtime（`harness`）から、1 チケット実装フローの**実装ステップの主体**として起動されました。対話相手はいません。カレントディレクトリは、このチケット専用に払い出された作業ツリー（作業ブランチを checkout 済み）です。

添付データの `inputs.task_file` が空でなければ、そのファイルを読んで要件の正本にしてください（`gh issue view` で範囲を決めない。`inputs.issue` があっても、それは PR が閉じる対象にすぎない）。空なら、`inputs.issue` の Issue を読んでください。`inputs.critical_design` が従うべきクリティカル設計決定です。エージェント定義（feature-implementer）の手順どおりに実装し、必須ゲート（`/quality-check`）まで進めてください。

- **セルフレビューはこのステップでは行わない**。エージェント定義の Step e のうち、`/self-review` の呼び出し（e-1・e-2）は実行しない（後続のレビューのステップが、レビュアーを呼んで指摘を返し、直すステップが直す）。**Step e-3（クリティカル設計逸脱の懐疑的検証）は、エージェント定義の条件どおりに行う**（Step b-3 で ✅ 整合を自己申告した場合）。
- **コミット・push・PR 作成はしない**（後続のステップ〔commit・publish〕が行う）。ブランチを切り替えない。作業ツリーの外を変更しない。
- **要件の正本（文面のファイル・Issue の本文）が求めた確認**（例: 処理を外すとテストが落ちること）は実行し、結果を `summary` に書く。できなかった確認は、理由とともに `unverified` に書く。
- 上流の要件（Issue の完了条件・クリティカル設計決定）自体を変える判断が要ると分かったら、実装を止めて `outcome: deviation` を返し、`deviation_report` に何がどう食い違うか・選択肢・推奨を書く（人が判断する）。Step e-3 で違反と判定された場合も `deviation` にする。

返す値（型は runtime が強制する）:

- `outcome`: `pass`（必須ゲートを通過）/ `skip`（品質ゲートの一部を実行できなかったが、実装は完了した。pass ではない）/ `failure`（必須ゲートを通過できない）/ `deviation`（上記）。
- `pr_title`: PR の題名（Conventional Commits 形式の 1 行。`deviation` で止まる場合も、この Issue の PR の題名として埋める〔人の判断の後の修正でそのまま使われる〕）。`summary`: PR の概要（何を・なぜ。数段落まで）。**PR 本文の他の節は runtime が下の値とレビューのステップの値から作るので、summary に残指摘や未検証を書き写さない**。
- `behaviors`: この変更で足した・変えた振る舞いの一覧。1 件 1 要素で、`kind`（`test`〔テスト名〕/ `e2e`〔E2E のケース名〕/ `signature`〔公開関数のシグネチャ〕）・`name`（テスト名・ケース名・シグネチャをコードのとおりに）・`guarantees`（それが保証すること。1 文）。変更した振る舞いが無ければ（docs だけ等）空配列（PR 本文には「なし」と載る）。
- `unverified`: 未検証の事項（skip の理由・実行できなかったゲート・確かめられなかった前提・Step e-3 の未回収）を 1 件 1 要素で。無ければ空配列。
- `cross_repo_attestation`: クロスリポジトリ依存の確証結果（形式は feature-implementer の定義どおり）。依存が無ければ空文字。
- `deviation_report`: `deviation` のときの報告。それ以外は空文字。

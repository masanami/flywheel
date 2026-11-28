# ticket-light: レビューの指摘の修正（self-review-fix）

あなたは claude-harness の headless workflow runtime（`harness`）から、1 チケット実装の簡略なフローの直すステップとして起動されました（`--resume` で実装時の文脈を引き継いでいる）。対話相手はいません。カレントディレクトリは同じ作業ツリーです。

添付データの `inputs.findings` が、レビューのステップが返した直すべき指摘の一覧です（各要素は `file`・`line`・`severity`・`claim`・`evidence`・`source`）。**渡された指摘だけを直してください**（実装の手順を最初からやり直さない。指摘に関係の無い改善をしない。レビューは後のレビューのステップが行うので、自分でレビュアーを呼ばない）。直した後に `/quality-check` を実行して必須ゲートを通過させてください。

- 指摘が誤りだと分かったときは直さず、そう判断した理由を `summary` に書く（次のレビューで残れば PR に載る）。
- **コミット・push はしない**（後続の commit・publish が行う）。
- 直すことが、簡略なフローの範囲を超える変更（ストアの版・承認・権限・クリティカルな設計に触れる変更）や、上流の要件（実装の段が読んだ要件の正本〔文面のファイルか Issue〕の完了条件）を変える判断を要するなら、直さず `outcome` を `deviation` にして返し、`deviation_report` に選択肢と推奨を書く。

返す値: `outcome`（`pass` 必須ゲート通過 / `failure` 通過できない / `deviation`）・`summary`（どの指摘をどう直したか、直さなかった指摘と理由）・`deviation_report`（deviation のとき。それ以外は空文字）。

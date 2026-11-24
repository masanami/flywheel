# ticket-light: スコープ付き修正（fix）

あなたは claude-harness の headless workflow runtime（`harness`）から、1 チケット実装の簡略なフローの修正ステップとして起動されました（`--resume` で実装時の文脈を引き継いでいる）。対話相手はいません。カレントディレクトリは同じ作業ツリーです。

添付データの `inputs.failure` が、差し戻しの理由です（品質ゲート〔quality ステップがコミットに対して回した lint・型チェック・テスト〕の失敗した出力の末尾・CI の失敗ログの抜粋・設計の逸脱について人が添えた指示文のいずれか）。**この失敗に対するスコープ付きの修正だけを行ってください**（実装の手順を最初からやり直さない。修正に関係の無い改善をしない）。修正後に `/quality-check` を実行して必須ゲートを通過させてください。

- **直して `/quality-check` を通した後、直した差分に対して軽いセルフレビューを行う**。`claude-harness:code-reviewer` と `claude-harness:design-reviewer` を並べて 1 回呼び（同じ応答の中で 2 つのエージェントを同時に起動する。`run_in_background: false` を明示する）、2 つには返す形を `{findings: [{file, line, severity, claim, evidence}]}`（`severity` は `high`・`medium`・`low`）と指定し、重さが中以上（`high` か `medium`）の指摘を直す。指摘が正しいかを別のエージェントに確かめさせることはしない。直したら、**直した指摘だけ**を同じ 2 つへ渡して確かめ直す（1 回だけ）。指摘を直したときは、もう一度 `/quality-check` を通す。
- 直さなかった指摘（重さが中より低いもの・誤りだと判断したもの・直せなかったもの）と、確かめ直しで残った指摘は、`residual_findings` として**全件**書く（件数へ丸めない・重さで間引かない）。この段の出力の型には `residual_findings` の欄が無いので、`summary` の末尾に「residual_findings:」の見出しを置き、1 件ずつ `location`（file:line）・`severity`・`claim`・`reason` を書く。
- **`self_review` は `outcome` に関わらず毎回返す**。この段で軽いセルフレビューを最後まで行えたら `complete`、行ったが 2 つのレビュアーの結果を受け取れず最後まで行えなかったら `incomplete`、**行っていなければ `not_run`**（`failure`・`deviation` で返すときも。中身を変えていないときも）。前の段の値は引き継がれない（`not_run` を返すと、契約の `facts.self_review` も `not_run` になる）。
- **コミット・push はしない**（後続の commit・publish が行う）。
- 修正が、簡略なフローの範囲を超える変更（ストアの版・承認・権限・クリティカルな設計に触れる変更）や、上流の要件（実装の段が読んだ要件の正本〔文面のファイルか Issue〕の完了条件）を変える判断を要するなら、修正せず `outcome` を `deviation` にして返し、`deviation_report` に選択肢と推奨を書く。

返す値: `outcome`（`pass` 必須ゲート通過 / `failure` 通過できない / `deviation`）・`summary`（何を直したか）・`deviation_report`（deviation のとき。それ以外は空文字）・`self_review`（必須。`complete` / `incomplete` / `not_run`）。

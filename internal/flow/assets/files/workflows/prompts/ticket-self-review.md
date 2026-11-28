# ticket: レビュー（self-review）

あなたは claude-harness の headless workflow runtime（`harness`）から、1 チケット実装フローの**レビューのステップ**として起動されました。対話相手はいません。カレントディレクトリは、このチケット専用に払い出された作業ツリー（作業ブランチを checkout 済み）です。実装のステップが書いた変更（コミット済みのものと未コミットのもの）を、`inputs.base` との差分としてレビューし、指摘を返してください。

- **このステップは指摘を集めて返すだけで、直さない**（直すのは後の直すステップ）。ファイルを変更しない・コミットしない・push しない・ブランチを切り替えない。
- 要件の正本: 添付データの `inputs.task_file` が空でなければそのファイル、空なら `inputs.issue` の Issue（`gh issue view`）。`inputs.critical_design` があれば、従うべきクリティカル設計決定としてレビュアーへ渡す。

## 手順

1. 差分の範囲を確かめる（`git diff <inputs.base>...HEAD` と未コミットの変更。`git status`）。
2. **レビュアーを並べて呼ぶ**。`inputs.reviewers`（カンマ区切りのエージェントの名前、たとえば `claude-harness:code-reviewer`・`claude-harness:design-reviewer`）の全員を、Task ツールで同じ応答の中で同時に起動する（`run_in_background: false` を明示し、全員の結果が返ってから次へ進む）。渡す観点: 差分の範囲・要件の完了条件と受入基準を満たしているか・スコープ外の変更・デバッグログ・機密情報の混入・クリティカル設計決定との食い違い。返す形は `{findings: [{file, line, severity, claim, evidence}]}`（`severity` は `high`・`medium`・`low` のどれか。`line` は特定できなければ 0。指摘が無ければ `{findings: []}`）と指定する。
3. **Codex のレビュー**: 添付データの `inputs.codex_review` が `on` で、`harness.attempt` が 1 のとき（このステップの 1 回目）だけ、手順 2 のレビュアーと並べて裏で起動する。`on` でなければ何もせず `codex_review` は `off`。`on` でも 2 回目以降は走らせず `codex_review` は `skipped`。プラグインの `/codex-review` の手順 1〜4 と同じ形で行う。
   1. 差分を `claude-harness-run collect-review-diff [base]` で集め、標準出力の JSON の `base` と `diff_file` を控える（差分の本文を指示文へ貼らない）。
   2. 要件の文脈を、`mktemp "${TMPDIR:-/tmp}/codex-review-context.XXXXXX"` で作った一時ファイルへ書く。中身は、`inputs.task_file` があればその本文、無ければ `gh issue view <inputs.issue> --json number,title,body,url` の結果。`inputs.critical_design` も添える。名前と置き場所はこの形に固定する（後始末が `${TMPDIR:-/tmp}` 直下の `codex-review-context.*` しか消さないため）。
   3. `claude-harness-run codex-review-runner --diff-file "<diff_file>" --base "<base>" --issue-file "<context_file>"` を裏で起動し、そのあいだにレビュアーの結果を待つ。結果は、終了コード 0 が `complete`・3 が `partial`（部分の結果。指摘ゼロと読まない）・4 が `failed`（指摘を使わない）。起動できなかった（`claude-harness-run` が無い・Codex が入っていない等）ときも `failed`。
   4. Codex のレビューが終わるまで待つ。**終わるまで**このステップを終えず、出力を**返さない**（待たずに返すと、結果を受け取れない）。
   - Codex の指摘のうち `verdict` が `refuted`（反証された）のものは直すべき指摘にしない。ほかは手順 5 の重さの境目で分ける。`source` は `codex`、`residual_findings` の `claim` の先頭に `[Codex] ` を付ける。
   - Codex のレビューが完了しなかったとき（`partial`・`failed`）は、完了しなかったことと理由（`errors[].message` の内容や、利用上限なら再開できる時刻）を `unverified` に 1 件書く。使えないことだけでは止めず、`outcome` を `incomplete` にしない。
   - 一時ファイル（`diff_file` と context ファイル）は、結果の成否に関わらず、最後に `claude-harness-run cleanup-review-diff "<diff_file>" "<context_file>"` で消す（素の `rm` は使わない。まだ作っていないファイルは引数から外す）。消せなくても、レビューの結果をそれで上書きしない。
4. **指摘を疑う**: `inputs.verifier` が `none` でなければ（例: `claude-harness:finding-verifier`）、レビュアーの指摘を Task ツールでその役へ渡し（`run_in_background: false`）、差分に照らして反証させる。反証された指摘は直すべき指摘にしない。`none` なら疑う役は呼ばず、自分で読んで明らかに誤りの指摘だけを外す。
5. **分ける**: 重さが `inputs.fix_threshold`（`high`・`medium`・`low` のどれか、既定は `medium`）以上で、反証されていない指摘を「直すべき指摘」とする。同じ指摘が複数のレビュアーから出たら 1 件にまとめる。

## 返す値（型は runtime が強制する）

- `outcome`: `findings`（直すべき指摘が 1 件以上）/ `clean`（直すべき指摘が 0 件）/ `incomplete`（レビューを最後まで行えなかった。レビュアーや疑う役の結果を受け取れなかった等。やり直さず、受け取れなかった委譲先の名前を `unverified` に書く）。Codex が完了しなかったことだけでは `incomplete` にしない。
- `self_review`: `outcome` が `findings`・`clean` なら `complete`、`incomplete` なら `incomplete`。
- `codex_review`: 上の手順 3 の値（`off`・`complete`・`partial`・`failed`・`skipped`）。
- `findings`: 直すべき指摘の全件。各要素は `file`・`line`・`severity`・`claim`・`evidence`・`source`（指摘したレビュアーの名前か `codex`）。`clean`・`incomplete` のときは空配列。
- `residual_findings`: このレビューの時点で直っていない指摘の**全件**（直すべき指摘・重さが境目より低い指摘・反証された指摘。件数へ丸めない・重さで間引かない）。各要素は `location`（file:line）・`severity`・`claim`・`reason`（直すべき指摘なら「直すべき指摘（未修正）」、ほかは直さない理由）。戻る回数の上限に達したときや直すべき指摘が無いときは、これがそのまま PR に載る。
- `unverified`: 未検証の事項を 1 件 1 要素で。無ければ空配列。
- `summary`: レビューの要約（呼んだレビュアー・疑う役・Codex の結果と、指摘の件数）。

# ticket: レビュー対応（respond）

あなたは claude-harness の headless workflow runtime（`harness`）から起動された、PR の**レビュー対応ステップ**です。対話相手はいません。カレントディレクトリは作業ツリー（PR の作業ブランチ）です。

添付データの `inputs.pr` の PR に付いたレビューコメントへ、`claude-harness:pr-review-respond` スキル（`/pr-review-respond`）の手順どおりに対応してください。

## 添え書き（`inputs.note`）

添付データの `inputs.note` は、レビュー待ちのゲートを再開した側（呼び出し元・人）が `--note` で添えた文です（完了の検証での差し戻し・人による差し戻し・マージできなかった理由など）。

- **`inputs.note` が空でなければ、PR のレビューコメントと同じく対応すべき指摘として扱う**。指摘の分類と対応はスキルの手順に従う（人の判断が要る分類なら下の `needs_human`）。
- **PR にレビューコメントが 1 件も無くても、`inputs.note` が空でなければ `no_change` にしない**。添え書きの指摘を直してコミットしたら `pushed`。直す必要が無い・直せないと判断したら、自分で打ち切らずに `needs_human` を返し、`human_question` に添え書きの要旨・そう判断した理由・選択肢・推奨を書く。
- 添え書きの指摘に対応したら、**添え書きの要旨と、どう対応したか（直したこと、または直さなかった理由）を、自分の PR へ 1 件のコメントとして残す**（添え書き 1 回につき 1 件）。
  - **コメントの本文のファイルは作業ツリーの外の一時ディレクトリに作る**（`body=$(mktemp -d)/note-reply.md` に書いて `gh pr comment <inputs.pr> --body-file "$body"`）。作業ツリーの中に置かない（未コミットのファイルが残ると、後の品質ゲートが止まる）。共有の固定名（`/tmp/note-reply.md` など）も使わない（別の run の本文を投稿しうる）。
  - **投稿の前に、同じ添え書きへのコメントが既に PR にあるかを確かめる**（`gh pr view <inputs.pr> --json comments`）。前の実行が打ち切られて同じ添え書きでやり直している場合、既にあればもう 1 件は付けない。見分けられるように、本文の先頭行を `harness respond: 添え書きへの対応（run <harness.run_id>）` にし、本文に添え書きの要旨を含める。同じ先頭行で同じ要旨のコメントがあれば、投稿済みとして扱う。
- `inputs.note` が空文字なら、今までどおり PR のレビューコメントだけを読む。

## セルフレビュー

- **このステップではセルフレビューを行わない**（`/self-review` を呼ばない。`/pr-review-respond` の手順にセルフレビューがあっても省く）。セルフレビューはフローのレビューのステップが行う。

## 返す値

- **この段でコミットを 1 つでも足したら、返す前に作業ブランチへ通常の push（`git push`。強制 push・`--force`・`--force-with-lease` はしない）をして、`outcome` は必ず `pushed`** にする。手元にだけコミットを残して `no_change` や `needs_human` で返さない（PR に無い中身についての結果になる）。`/pr-review-respond` の手順にある push と同じで、コミットのたびでなく、返す直前の 1 回でよい。push が拒否された・できなかったときは、強制せず、`needs_human` を返し、`human_question` に何が起きたか（未 push のコミットが手元に在ること）・選択肢・推奨を書く。`pushed` を返すと、runtime が品質ゲートを回し、通れば CI の確認へ進む。品質ゲートは未コミットの変更が残っていると止まるので、作業ツリーをコミット済みの状態にして返す。
- 対応が不要だった・返信だけで済んだ場合は `no_change`（この段でコミットを 1 つも足していないときだけ。`inputs.note` が空でなければ上のとおり `pushed` か `needs_human`）。
- スキルの手順が人の判断を求める場合（修正後の `/quality-check` が通らない〔qcFailed〕・`design_change` / `critical` に分類された指摘）は、その判断をせず `needs_human` を返し、`human_question` に判断してほしい内容・選択肢・推奨を書く。
- **PR のマージ・クローズはしない**。

返す値: `outcome`（`pushed` / `no_change` / `needs_human`）・`summary`（何をしたか。添え書きがあれば、それにどう対応したかを含める）・`human_question`（needs_human のとき。それ以外は空文字）。

# 委譲の子をクラウドのセッションで動かせるかの調査（#382）

- 課題: masanami/flywheel#382（委譲の子セッションを Claude のクラウドのセッションでも動かせるようにする）
- 調べた日: 2026-11-01。手元の Claude Code は `2.1.316`。クラウド側の Claude Code も `2.1.316`（セッション内の `claude --version`）
- 結論は設計書 §11「クラウドのセッション（#382 の結論）」に書いた。このメモは根拠の記録である。
- 確証のレベルは 2 つ: **実物で確証**（クラウドのセッションを起動して観測した）／**資料だけ**（公式ドキュメントで読んだだけ。未確証として扱い、結論の根拠にしない）。

## 1. ローカルでの前提（いまの委譲が頼っている `claude -p` の性質）

コードの位置は main の `c6f601b` 時点。

| 性質 | どこで頼っているか | 内容 |
|---|---|---|
| 事前に決めたセッション ID | `internal/core/delegate.go:664-668`（`newLowercaseUUIDv4`）・`internal/invoker/delegate.go:156-172`（`buildDelegateArgs`） | core が小文字の UUID を採番して run に記録してから、`claude -p --session-id <UUID>` で起動する。返ってきた `session_id` が違えば `session_id_mismatch` を立てる（`internal/core/delegate.go:791`・`:815`） |
| `--resume` による再開 | `internal/core/delegate.go:653-662`・`:748-755`、`internal/invoker/delegate.go:161-164` | 子が問いを出して終わった課題・差し戻し・枠超過の後は、同じ `session_id` を `--resume` で起動する。J3（ブリーフ）は呼ばず、子は前の文脈を持つ |
| 1 起動ごとの予算の打ち切り | `internal/invoker/delegate.go:170`（`--max-budget-usd`）・`internal/core/budget.go:9` | 実装枠の残りを `--max-budget-usd` に渡す。この値が周の予算の天井の評価額・予約額にもなる |
| 返り値の `total_cost_usd` | `internal/core/judgment.go:606-643`（`computeRunCost`）・`internal/core/delegate.go:792` | 新しいセッションは返り値をそのまま費用（`reported`）。`--resume` は直前の run の報告との差（`delta`）。値が無い・負・結果が読めないときは渡した上限額（`unknown`。fail-closed） |
| 構造化した結果 | `internal/invoker/delegate.go:168-169`（`--output-format json`・`--json-schema`） | 子の報告（結末・ブランチ等）をスキーマつきの JSON で受ける |
| 子のプロセスがローカルにあること | `internal/core/delegate.go:670-671`（`pid`・`host`）・`:756`（heartbeat）・`internal/core/judgment.go:662`（`ReapInterruptedRuns`） | run に pid と host を記録し、待つ間 heartbeat を更新する。古い heartbeat で、そのホストにプロセスが無ければ `interrupted` で閉じる |
| 作業ツリーがローカルにあること | `internal/invoker/process.go:39`（`cmd.Dir`）・`internal/core/delegate_reconcile.go:6-9`・`:48-60` | 子はスロットの作業ツリーを作業ディレクトリにして動く。終わった後の照合は子の報告に依らず、スロットの作業ツリーの検査（現在のブランチ）と `gh` の GET（リモートのブランチ・PR）で行う |
| 委譲の目印の環境変数 | `internal/invoker/delegate.go:27-29`（`delegatedEnv`） | 子の環境に run の ID を入れる。子から `flywheel` を呼ばせない（`--disallowedTools Bash(flywheel:*)`） |

仕様の側は `docs/features/m3-invoker-delegation.md` §費用の記録（117 行目〜）・§合流と照合（200 行目〜）・§実行スロット（253 行目〜。`container` は 312 行目「S4 を実装対象にするときに書く」）、設計書 §11。

## 2. 資料で調べたこと（すべて資料だけ・未確証）

| 【要確認】 | 資料での記述 | 出典 |
|---|---|---|
| (a) 起動・状態・構造化した結果・再開 | 新しいセッションは `claude --cloud "<指示>"` で作る。手元のディレクトリの GitHub のリモートを現在のブランチで clone する（ローカルのコミットは先に push が要る）。既存のセッションへは `claude -p "<文>" --cloud <セッション ID>` で文を積んで終わり、応答は待たない。`--output-format json` の出力は `{ok, session_id, url}` だけ。`--resume` はローカルの履歴だけを対象にし、クラウドのセッションは `--teleport` でローカルへ引き出す。プログラムからの起動は Routines（予定のタスク）の API トリガー（`POST .../v1/claude_code/routines/<trig_id>/fire`。タスクごとの bearer トークン）だけが書かれている。状態を問い合わせる口・結果を JSON で受け取る口・セッション ID を呼び出し側で決める方法は書かれていない | https://code.claude.com/docs/en/claude-code-on-the-web ・ https://code.claude.com/docs/en/cli-reference ・ https://code.claude.com/docs/en/routines |
| (b) 予算の打ち切り・消費額 | `--max-budget-usd` は「print mode だけ」と書かれている。クラウドのセッションは「プランの利用上限に、ほかの Claude・Claude Code の利用と合わせて数える。クラウドの VM に別の課金は無い」。1 セッションの費用（`total_cost_usd` に当たるもの）を取る方法は書かれていない | https://code.claude.com/docs/en/cli-reference ・ https://code.claude.com/docs/en/claude-code-on-the-web |
| (c) push・PR・GitHub 以外 | GitHub への接続は Claude GitHub App の承認か `/web-setup`（手元の `gh` のトークンを送る）のどちらか。資格情報は Anthropic のサーバ側に置かれ、VM に入らず、GitHub の操作は proxy が資格情報を付ける。proxy はブランチの削除とタグの push を拒むが、「どのブランチへ push できるかは制限しない（GitHub のブランチ保護で絞る）」。GraphQL を使う `gh pr` は 403 になり、`gh api repos/...` の REST を使う。GitHub 以外は `CCR_FORCE_BUNDLE=1` でローカルのリポジトリを bundle として送れるが、結果をリモートへ push できない | https://code.claude.com/docs/en/claude-code-on-the-web ・ https://code.claude.com/docs/en/cloud-environments |
| (d) MCP と資格情報 | リポジトリの `.mcp.json` は読まれる（`~/.claude.json` の local・user スコープの MCP は届かない）。claude.ai の connectors はセッションで有効にできる。環境変数は環境（environment）に `.env` の形で置き、その環境を使う人は誰でも値を読める。API キーは「network secrets」として環境に置くと、proxy が指定したホストへの通信にだけ付け、セッションの環境変数・ファイルには現れない | https://code.claude.com/docs/en/cloud-environments |
| (e) `CLAUDE.md`・`.claude/settings.json` | リポジトリの `CLAUDE.md`・`.claude/settings.json`（hooks・permissions）・`.claude/skills` 等は読まれる。`~/.claude/` の設定・`settings.local.json`・`enabledPlugins` のプラグインは読まれない | https://code.claude.com/docs/en/settings ・ https://code.claude.com/docs/en/cloud-environments |

そのほかの資料の記述: 1 セッションに 1 つの VM（Ubuntu 24.04・x86_64。目安 4 vCPU・16 GB・30 GB）。ネットワークは既定で許可リスト（Trusted）。しばらく動きが無いと VM は止まり、長く放置すると回収される。同時に動かせる数の上限は書かれていない。レート制限はアカウントのほかの利用と共有する（いずれも https://code.claude.com/docs/en/cloud-environments ・ https://code.claude.com/docs/en/claude-code-on-the-web）。

## 3. 実物で確かめたこと

### 準備

- 目印のブランチ `probe/382-markers`（main の `c6f601b` から 1 コミット）を push した。置いたもの:
  - `CLAUDE.md` の末尾に「PROBE382 の目印」として文字列 `PROBE382-CLAUDEMD-7f3a`
  - `.claude/settings.json` に `env.PROBE382_SETTINGS_ENV = probe382-settings-b91c` と、拒否の規則 `Read(./probe382-denied.txt)`・`Bash(cat probe382-denied.txt)`
  - `.mcp.json` に読み取りだけの MCP 1 つ（`npx -y @modelcontextprotocol/server-sequential-thinking`。外部へ書き込まず、資格情報を要らない）
  - 拒否の対象のファイル `probe382-denied.txt`
- 起動は、このブランチを checkout した使い捨てのクローン（`/tmp` 配下）から行った（`claude --cloud` は手元の現在のブランチを使うため）。
- 新しいトークンは発行していない。GitHub App の承認・`/web-setup` はしていない。秘密の値はどこにも出していない。
- 状態と結果は、claude.ai の API `GET /v1/code/sessions/<セッション ID>/events`（Claude Code の `RemoteTrigger` ツールの `get_run_log`。OAuth のトークンはツールの中で付き、表に出ない）で読んだ。

### 起動の記録

「起動」は、クラウドのセッションを作るか既存のセッションへ文を送った回数で数える。

| # | 実行したコマンド | 観測 | 数えたか |
|---|---|---|---|
| 0 | `claude -p --cloud "<指示>" --output-format json --session-id <UUID> --max-budget-usd 1` | 終了コード 1。`Error: --cloud cannot be combined with --print. Starting a new cloud session with --cloud is interactive only` | 数えない（セッションは作られていない） |
| 0' | `claude --cloud "<指示>"` を疑似端末で起動 | 作業ディレクトリの信頼の確認（trust の対話）で止まった | 数えない（同上） |
| 1 | `claude --cloud "<指示 1>"` を `expect` で起動し、信頼の確認に答えた | `Created cloud session` と URL・`Resume with: claude --teleport session_013TPNEDs2G5eRX1LvyUTVyQ` を表示。events では `Cloned from seed bundle` → 作業ディレクトリ `/home/user/repo`。MCP の呼び出しで `permission prompt` が出て、そこから進まなくなった（約 8 分後に見ても結果なし） | 1 |
| 2 | `claude -p "<続きの指示>" --cloud session_013TPNEDs2G5eRX1LvyUTVyQ --output-format json` | 終了コード 0。標準出力は `{"ok":true,"session_id":"session_013TPNEDs2G5eRX1LvyUTVyQ","url":"..."}` だけ。文は events に積まれたが、権限の確認待ちは解けなかった | 2 |
| 3 | `claude --cloud "<指示 2>" --permission-mode auto`（`expect`） | `session_013TPNEBjhBTizNcmaxbhoPh` が作られ、権限の確認で止まらずに 44 秒で終わった。events の最後に `result: success is_error=false turns=11 duration=44s permission_denials=2 — <最後の応答の全文>` | 3 |
| 4 | `claude -p "<前の応答を問う文>" --cloud session_013TPNEBjhBTizNcmaxbhoPh --output-format json` | 応答は `{ok, session_id, url}` だけ。events では同じセッション・同じ作業ディレクトリで続き、前の応答の値と、前のターンで作ったローカルのコミット `3d57eb4` を答えた | 4 |
| 5 | `claude --cloud "<echo を 3 回>" --permission-mode auto --max-budget-usd 0.01 --session-id <UUID>`（`expect`） | 指定はどちらもエラーにならずに受け付けられた。作られた ID は `session_013TPNEC6KuzejUS8VrwgPBR`（渡した UUID ではない）。4 回のモデルの呼び出し（echo 3 回と最後の応答）を最後まで行った | 5 |

比べるため、同じ指示を手元で `claude -p "<echo を 3 回>" --max-budget-usd 0.01 --output-format json --allowedTools 'Bash(echo:*)'` で実行した（クラウドの起動ではない）。`subtype: error_max_budget_usd`・`is_error: true`・`num_turns: 2`・`total_cost_usd: 0.169…`・`result: null` で止まった。

### 項目ごとの観測

**(a) 起動・状態・構造化した結果・再開**

- 新しいセッションを `-p` で作ることはできない（#0 のエラー文。実物で確証）。`claude --cloud` は対話の起動で、作業ディレクトリの信頼の確認に答える必要がある（#0'。実物で確証）。疑似端末と `expect` で答えれば作れたが、flywheel が `claude -p` を起動するのと同じ形ではない。
- 新しいセッションの起動は、手元で push 済みのブランチを使う。今回は events に `Cloned from seed bundle` と出て、セッションの中のクローンにはリモートが無かった（#3 の `git remote -v` が空）。この GitHub リポジトリへの接続がクラウド側で有効になっていないため bundle に切り替わったと推測する（推論。資料の「GitHub への接続は App か `/web-setup`」と合う）。
- 状態と結果: CLI の応答（#2・#4）には結果が無い。結果は events の `result` の行（成否・ターン数・所要時間・拒否の数・最後の応答の全文）で読めた（#3。実物で確証）。`--json-schema` を渡す経路は無く、構造化は指示文で JSON を頼み、最後の応答を読むしかない（#3 で、指示どおりの JSON が最後の応答に入った）。
- 再開: `claude -p "<文>" --cloud <ID>` で既存のセッションへ続きの文を送れ、前の文脈と VM のファイルが残っていた（#4。実物で確証）。ただし送った時点では応答を待てず、終わったかは events を読み直して確かめる。
- セッション ID は呼び出し側で決められない（#5 で `--session-id` は受け付けられたが無視され、サーバが `session_…` を振った。実物で確証）。
- 権限: 既定の権限モードでは、確認の要る道具（今回は MCP の呼び出し）で止まり、答える人がいないと進まない。続きの文を送っても解けなかった（#1・#2。実物で確証）。`--permission-mode auto` を付けると止まらずに最後まで進んだ（#3。実物で確証）。
- 止まったままのセッション `session_013TPNEDs2G5eRX1LvyUTVyQ` は止めずに残っている（CLI に止める口が見当たらず、削除は承認が要るため）。

**(b) 予算の打ち切りと消費の額**

- `--max-budget-usd 0.01` を付けて作ったセッションは打ち切られず、最後まで進んだ（#5）。同じ指示を手元で実行すると 2 ターンで `error_max_budget_usd` で止まり 0.169 USD だった。クラウドでは 1 起動の予算の打ち切りは効かない（実物で確証。指定はエラーにならず黙って無視される）。
- 消費の額: CLI の応答（`{ok, session_id, url}`）にも events の `result` の行にも費用の欄は無かった（実物で確証）。資料ではプランの利用上限に数えられ、USD の従量ではない（資料だけ）。events には `rate_limit_event` が出ていた（中身は要約で省かれ、読めていない）。1 起動ごとの額は取れないため、計画のとおり起動の回数の上限だけで止めた。

**(c) push と PR**

- 今回は push も PR もできなかった（#3。実物で確証）。観測: セッションの中のクローンにリモートが無く `git push -u origin probe/382-cloud-push-auto` は `fatal: 'origin' does not appear to be a git repository`。`gh pr create` は `HTTP 403: GitHub GraphQL is not available from Claude Code sessions; use the REST API`。`gh api repos/masanami/flywheel/pulls` は `HTTP 403: GitHub access to this repository is not enabled for this session. Use add_repo to request access.`。`gh auth status` は `GH_TOKEN` を無効と表示した。
- push と PR を確かめるには、Claude GitHub App をこのリポジトリへ承認するか `/web-setup`（手元の `gh` のトークンを Anthropic へ送る）が要る。どちらも資格情報の扱いを変える操作で、この課題の範囲（新しいトークンを発行しない・資格情報を増やさない）の外のため行っていない。**未確証**（条件付き）。資料では、接続すれば push・REST での PR の作成はでき、push できるブランチは proxy では絞られない。
- 検証用の PR は作られていない（close するものは無い）。GitHub 以外のリポジトリは資料だけ（bundle で送れるが push できない）。

**(d) MCP の接続と資格情報**

- リポジトリの `.mcp.json` の MCP はセッションで読み込まれ、`mcp__probe382-seqthink__sequentialthinking` として呼べた（#3。実物で確証）。起動の直後は接続中で、道具の検索の後に使えた（セッションの自己申告）。
- 既定の権限モードでは、その MCP の呼び出しが権限の確認で止まった（#1。実物で確証）。
- 資格情報を要る MCP は試していない（新しいトークンを出さないため）。資格情報の渡し方は資料だけ: 環境の環境変数（その環境を使う人は誰でも読める）か network secrets（proxy が指定のホストへの通信にだけ付ける）。**#381 の範囲を効かせる手段としては、環境（environment）とリポジトリの `.mcp.json` の単位になる**（資料だけ）。

**(e) `CLAUDE.md`・`.claude/settings.json`**

- `CLAUDE.md` の目印は、ファイルを読まずに `PROBE382-CLAUDEMD-7f3a` と答えた（#3。実物で確証）。
- `.claude/settings.json` の `env` は効いた（`echo "$PROBE382_SETTINGS_ENV"` が `probe382-settings-b91c`。#1・#3。実物で確証）。
- `.claude/settings.json` の拒否の規則は効いた（`cat probe382-denied.txt` が `permission_denied Bash [rule]`。`cat` を含む連結したコマンドも丸ごと拒否された。#1・#3。実物で確証）。
- 手元の `~/.claude/` の設定・プラグインが届かないことは資料だけ（確かめていない）。

## 4. 項目ごとの結論

| 項目 | 結論 | 確証 | 要点 |
|---|---|---|---|
| (a) 起動・状態・構造化した結果・再開 | 条件付き | 実物で確証 | 作成は対話の起動だけ（`-p` 不可・信頼の確認あり）。ID は事前に決められない。結果は events を読み、構造化は指示文頼み。続きの文は `-p --cloud <ID>` で送れて文脈が残る。無人では `--permission-mode auto` が要る |
| (b) 予算の打ち切り・消費の額 | できない | 実物で確証 | `--max-budget-usd` は受け付けられるが効かない。費用の欄はどこにも無い。課金はプランの利用上限（資料だけ） |
| (c) push・PR | 未確証 | 実物では不可を観測・可は資料だけ | このリポジトリへの GitHub の接続が無効で、bundle で clone され push も REST も 403。接続（App か `/web-setup`）をすればできると資料にある。GitHub 以外は push できない（資料だけ） |
| (d) MCP と資格情報 | 条件付き | `.mcp.json` は実物で確証・資格情報は資料だけ | `.mcp.json` の MCP は使える。資格情報は環境の変数か network secrets で、単位は環境 |
| (e) `CLAUDE.md`・`.claude/settings.json` | できる | 実物で確証 | `CLAUDE.md`・`env`・拒否の規則が効く |

## 5. 使った額と起動の回数

- 起動: **5 回**（上限 6 回）。新しいセッション 3 つ（#1・#3・#5）と、既存のセッションへの文 2 回（#2・#4）。セッションが作られなかった試み 2 回（#0・#0'）は数えていない。
- クラウドの費用: **USD では測れない**。CLI の応答・events のどちらにも費用の欄が無く、資料ではプランの利用上限に数える（USD の従量ではない）。計画の「1 回ごとの額が取れなければ起動 6 回の上限だけで止める」に従った。参考: events に出たターン数は #3 が 11・#5 が 4（#3 の `result` の行と #5 の events の数）。#1・#2・#4 のターン数は数えていない。
- 比べるために手元で実行した `claude -p` 1 回は 0.169 USD（実装の枠から）。

## 6. 残したもの

- ブランチ `probe/382-markers`（目印。削除は承認が要るため残す）
- クラウドのセッション 3 つ（`session_013TPNEDs2G5eRX1LvyUTVyQ` は権限の確認待ちのまま・`session_013TPNEBjhBTizNcmaxbhoPh`・`session_013TPNEC6KuzejUS8VrwgPBR`）。止める・消す操作はしていない
- push されたブランチ・PR は無い（クラウドからの push はできなかった）

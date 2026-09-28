# flywheel - Claude Code プロジェクトコンテキスト

> 設計の正本は `docs/architecture.md`、M1 の機能仕様は `docs/features/m1-core.md`。受入基準・決定を変える必要に気付いたら、変えずに PR の説明へ「仕様への指摘」として書く。

## 開発規約

### ブランチ・コミット
- **方針**: 1チケット = 1ブランチ → PR → 必須ゲート通過後にマージ（GitHub Flow）
- **ブランチ**: `{type}/issue-{番号}-{説明}`（例: `feat/issue-7-store-init-workspace`）。必ず `origin/main` から切る
- **コミット**: Conventional Commits + 日本語の要約
  - type: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `chore`, `ci`
  - scope: `core`, `store`, `cli`, `settings`, `ci`（該当が無ければ省略）
- **PR**: squash マージ。本文に `Closes #<番号>` を書く

### push の注意（`.github/workflows/*` を含む場合）
- `.github/workflows/*` を含む push は、gh の OAuth トークンに `workflow` スコープが無いと **HTTPS の remote では拒否される**（環境依存）。拒否されたら **SSH の remote で push する**（例: `git push git@github.com:masanami/flywheel.git <ブランチ>`）。force-push などで迂回しない。
- ワークフローの実行（`gh workflow`）は `.claude/settings.json` の ask のまま。緩めない。

### モジュール構成の規約
モジュールは 1 つ（`github.com/masanami/flywheel`）。M1・M2・M3 S1 で置くパッケージは次のものだけ（`internal/core` とその配下は、これから置く構成の意図）。

| パッケージ | 責務 |
|---|---|
| `cmd/flywheel` | バイナリの入口。引数を解釈して `internal/cli` を呼ぶだけ |
| `internal/core` | 状態機械・承認・作業ログ・課題と不可逆操作の操作・run/cycle/lock の記録と予算評価。**公開 API はここだけ** |
| `internal/core/internal/store` | SQLite の接続・PRAGMA・スキーマ・マイグレーション |
| `internal/core/coretest` | core のテスト支援専用（実ストアのフィクスチャ生成等）。`*_test.go` からだけ import し、本番バイナリの依存に含めない（`internal/cli/depcheck_test.go` が検査） |
| `internal/cli` | コマンドの定義・JSON／テキスト出力・終了コードとエラーコードの写像・端末での本人確認 |
| `internal/adapters/github` | `gh` の起動と応答の正規化（GitHub Issue の取得）。core の取得 IF（`internal/core/upstream.go`）だけに依存し、取り込みの規則は持たない。ストアを import しない |
| `internal/invoker` | `claude` の起動・結果の判別・費用の抽出（生の値）・出力の保存。判断点の指示文と J3 ブリーフの固定の節の雛形は `internal/invoker/prompts/` に置き `embed` でバイナリへ埋め込む（`Instructions`・`BriefFixedSections`）。分量の上限検査（`CheckPromptSizes`）・禁止語の生成と照合（`ForbiddenTerms`・`FindForbiddenTerms`。生成元は core・cli の定義を引数で受け取る純粋関数）もここに置く。枠超過の判定規則（`IsRateLimited`）は `internal/core` に置き、invoker は抽出した自由記述をそのまま渡すだけ。core の判断の呼び出し IF（`internal/core/judgment.go` の `JudgmentInvoker`）だけに依存し、対象の選び方・予算の評価・課題への写像といった規則は持たない。ストアを import しない |

- **CLI は core の公開 API だけを呼ぶ**。遷移の可否・承認の成立条件・作業ログの記録を `internal/cli` に書かない。
- **adapter・invoker の import の向き**: `internal/cli` が `internal/adapters/github`・`internal/invoker` を import してよいのは、それぞれを組み立てて core へ渡すこと（`New`・`NewLauncher`）と、起動不能のエラー（`ErrGHNotFound`・`invoker.ErrClaudeNotFound`）を CLI のエラーコードへ写すことだけ。`internal/core` は `internal/adapters`・`internal/invoker` のどちらも import しない（`internal/cli/depcheck_test.go` が `go list` の依存関係で検査する）。
- **ストアを開くのは core だけ**。ストアのパッケージを import できるのは `internal/core` の配下だけ（Go の internal 規則で強制し、`go list` の依存関係でも検査する）。`internal/invoker`・`internal/adapters/*` もストアを import しない。
- **本番の依存の上限**: 標準ライブラリ・`modernc.org/sqlite`・`golang.org/x/term` に限る（引数の解析も標準ライブラリ）。テスト専用の依存（疑似端末のライブラリなど）は可。
- **動作環境は macOS と Linux**（Windows は対象外）。受入基準は両方で成り立たせる。OS 依存でテストをスキップせざるを得ないときは、その事実と理由を PR の説明に書く。
- `Makefile` は macOS の GNU Make 3.81 でも動く書き方を保つ（bash 拡張構文・GNU Make 4 以降専用の機能を使わない）。

### 新規ファイルの置き場
- 状態・遷移・承認の規則は `internal/core`、SQL とマイグレーションは `internal/core/internal/store`、表示と引数は `internal/cli`、GitHub からの取得は `internal/adapters/github`、`claude` の起動は `internal/invoker`。上の表のパッケージ以外を新設しない（M1・M2・M3 S1 の範囲）。

## テスト方針

- Go 標準の `testing`。core のテストは**実ストア**（`t.TempDir()` の SQLite）で行う
- Mock対象: なし（端末の有無は、疑似端末を割り当てた子プロセス／標準入力をパイプにした子プロセスとして CLI を起動して作る）
- Mockしない: ストア（SQLite）。core のテストでストアをモック・フェイクに差し替えない

## 品質方針

```
入口は make check の 1 つ。次の 5 つを順に実行し、1 つでも失敗すれば非 0 で終わる。
PR ごとの CI（ubuntu-latest / macos-latest）も同じ make check を実行する。

1. 整形      gofmt -l .                              出力が空
2. 静的検査  go vet ./...                            終了コード 0
3. lint      golangci-lint run                       終了コード 0（設定は .golangci.yml。
             インストール済みの版が Makefile の GOLANGCI_LINT_VERSION と一致しなければ失敗）
4. テスト    go test -race ./...                     終了コード 0
5. ビルド    CGO_ENABLED=0 go build ./cmd/flywheel   終了コード 0（成果物は build/flywheel）
```

## よく使うコマンド

```bash
make check                          # 品質ゲートをすべて実行（PR 前に必ず通す）
make test                           # ゲートを個別に実行（fmt-check / vet / lint / test / build）
make print-golangci-lint-version    # 固定している golangci-lint の版を表示
go test -race ./internal/core/...   # パッケージを絞ってテスト
```

> このファイルは 200 行未満に保つ。肥大したらセッション内 `/doctor`（Claude Code v2.1.206 以降）の trim 提案に従い、コードベースから導ける内容（ディレクトリ構成・依存一覧・アーキテクチャ概要）を削る。

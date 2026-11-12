# flywheel

自律エージェントの運用システム。課題の取り込み・計画・実作業の委譲・検証・学習のサイクルを、**システムが進行し、判断が要る箇所でだけ Claude を呼ぶ**形で回す。

[claude-flywheel](https://github.com/masanami/claude-flywheel)（Claude Code プラグイン）と [claude-flywheel-board](https://github.com/masanami/claude-flywheel-board)（UI）の後継となる統合システムである。

## 状態

**設計段階**（実装には未着手）。旧 2 repo は切り替えまで保守モードで稼働を続け、本システムの完成後に一括移行する。

## 文書

- [docs/architecture.md](docs/architecture.md) — アーキテクチャ設計（確定。2026-09-21 の決定を反映）。背景・決定済み事項・構成・データモデル・サイクルの再設計・承認ゲート・未決事項・段階計画
- [docs/features/m1-core.md](docs/features/m1-core.md) — M1（core の最小形）の機能仕様（draft）

## 開発

品質ゲート（整形・静的検査・lint・テスト・ビルド）をまとめて実行するには:

```sh
make check
```

ブラウザの試験（Playwright。偽の API の上で画面を操作する）は `make e2e`。手元の必須のゲート（claude-harness の品質ゲートの宣言 `.claude-harness/quality-gate.json`）は `make gate`（`make check` の後に `make e2e`）。
初回は Playwright のブラウザを用意する。

```sh
cd web && npx playwright install chromium          # Linux で依存が足りないときは --with-deps
PLAYWRIGHT_CHANNEL=chrome make e2e                 # 落とせない環境では、手元の Google Chrome を使う
```

ブラウザが無いまま `make e2e` を流すと、上の手順を示して失敗する。

CI（GitHub Actions）は PR の push では走らない。main へのマージの後と、手動の起動（`workflow_dispatch`）だけで走る。main の保護の必須チェックは置かない。
claude-harness の ticket の流れが CI を待たないよう、`.claude-harness/ci.json` で `after-merge` を宣言している（`{"trigger": "after-merge"}`）。

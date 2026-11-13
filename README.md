# flywheel

自律エージェントの運用システム。課題の取り込み・計画・実作業の委譲・検証・学習のサイクルを、**システムが進行し、判断が要る箇所でだけ Claude を呼ぶ**形で回す。

[claude-flywheel](https://github.com/masanami/claude-flywheel)（Claude Code プラグイン）と [claude-flywheel-board](https://github.com/masanami/claude-flywheel-board)（UI）の後継となる統合システムである。

## コアコンセプト: 仕事を推し進める

flywheel のエージェントの価値は、作業をこなすことよりも、**仕事を推し進めること**にある。

人は、並列に仕事を抱えると負担に感じて、手が止まりがちになる。仕事を任された部下が、自分から結果を持ってきて次の判断を求めてくると、任せた側が手を止めていても仕事は進み続ける。flywheel のエージェントは、オーナーにとってそういう存在を目指す。

- **仕事のほうからオーナーに来る**。オーナーが進み具合を取りに行かなくても、エージェントが自分で進め、判断が要るものを推奨と一緒に持ってくる。待ちが長引いたら、どの判断がどれだけの仕事を止めているかを添えて、もう一度知らせる。
- **問いで止まらない**。オーナーの答えを待つのは、その課題だけにする。ほかの仕事は進め続ける。
- **聞くことを絞る**。答えが決まっているものや、元に戻せて影響の小さいものは、聞かずに進めて後から知らせる。どこまで任せるかは、オーナーが設定で選ぶ。
- **オーナーの判断を軽くする**。問いは、何か・なぜ要るか・いまどうなっているかと推奨を添えて、一言で答えられる形で出す。

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

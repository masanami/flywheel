// 関係グラフの案（flywheel#176）のための偽の関係。開発時の mock モードだけが読み込む。
// store.ts の課題に対し、親の大枠が複数（入れ子の 2 段を含む）・大枠の中の依存（分岐と合流）・大枠をまたぐ依存・
// 実施中の課題・畳んだ大枠の中の人間の操作待ち・複数のリポジトリ・関係の無い課題が見えるようにする（循環なし）。
// 依存はすべて理由を持ち、待つ理由の種類（先行の成果を使う・同時に走らせると衝突する）が両方出るようにする。

import type {
  ChallengeGraphResponse,
  ChallengeRelation,
} from "../api/proposed.ts";

// 依存は理由を持つ。need は先行の成果を使う（終わらないと始められない）、clash は同じものを触るので順に走らせる。
const need = (from: string, to: string, reason: string): ChallengeRelation => ({
  basis: "prerequisite",
  from,
  kind: "depends_on",
  reason,
  to,
});
const clash = (
  from: string,
  to: string,
  reason: string,
): ChallengeRelation => ({
  basis: "conflict",
  from,
  kind: "depends_on",
  reason,
  to,
});
const parent = (from: string, to: string): ChallengeRelation => ({
  from,
  kind: "parent_of",
  to,
});

const WEB = "mock-org/web";
const API = "mock-org/api";
const INFRA = "mock-org/infra";
const DOCS = "mock-org/docs";
// 2026-10-17 オーナー: リポジトリを多数持つエージェント・名前の長いリポジトリを、絞り込みの入力で確かめられるようにする。
// alpha は 12 件のリポジトリを触る（うち 2 件は名前が長い）。
const BATCH = "mock-org/payments-reconciliation-batch-service-legacy-v2";
const NOTIFY = "mock-org/customer-notification-template-renderer";
const SEARCH = "mock-org/search-indexer";
const IMAGE = "mock-org/image-pipeline";
const ADMIN = "mock-org/admin-console";
const BILLING = "mock-org/billing";
const AUTH = "mock-org/auth-gateway";
const CI = "mock-org/ci-config";

export function seedGraphs(): {
  alpha: ChallengeGraphResponse;
  beta: ChallengeGraphResponse;
} {
  return {
    alpha: {
      relations: [
        // 依存の後続（to）は、先行（from）が完了していない限り分類済か計画承認待ちに留まる
        // （mock/graph.test.ts が検査する）。着手済みの課題は、完了した先行の後ろか連鎖の先頭に置く。
        // 大枠「旧基盤の撤去」（C-14）: 設定の削除（完了）→ バッチの撤去（実施中）・移行の調査・トリアージ
        // → 旧テーブルの削除（分類済）
        parent("C-14", "C-12"),
        parent("C-14", "C-6"),
        parent("C-14", "C-8"),
        parent("C-14", "C-13"),
        parent("C-14", "C-18"),
        need("C-12", "C-6", "C-12 で消した設定を前提に、バッチの起動を外す"),
        clash(
          "C-12",
          "C-8",
          "同じ移行設定のファイル（config/migrate.yml）を触る",
        ),
        need("C-12", "C-13", "C-12 で残した設定の参照の一覧を使って洗い出す"),
        need("C-6", "C-18", "バッチが止まらないと旧テーブルへの書き込みが続く"),
        need("C-8", "C-18", "移行が全件済んだと確かめてから消す"),
        // 大枠「検索 API にページング」（C-2）: 外部 Issue・画像変換 → 権限チェック（計画承認待ち。合流）。
        // 文言（未分類）は大枠の中で依存を持たない
        parent("C-2", "C-4"),
        parent("C-2", "C-1"),
        parent("C-2", "C-10"),
        parent("C-2", "C-9"),
        clash("C-10", "C-4", "同じ画面の部品（AdminToolbar）を触る"),
        need("C-9", "C-4", "C-9 で足す画像の権限の API を使う"),
        // 大枠「ビルドと配布の改善」（C-15）の中に、さらに大枠「依存のキャッシュ」（C-16）を置く。
        // キャッシュキー（実施中）・ビルドの高速化（実施中）・自動の確認待ち → 配布手順の更新（分類済）
        parent("C-15", "C-5"),
        parent("C-15", "C-11"),
        parent("C-15", "C-16"),
        parent("C-15", "C-19"),
        parent("C-16", "C-17"),
        need("C-17", "C-19", "キャッシュキーの決め方を手順書に書く"),
        need("C-5", "C-19", "新しいビルドの成果物の置き場所を手順に書く"),
        clash(
          "C-11",
          "C-19",
          "同じワークフローのファイル（release.yml）を触る",
        ),
        // 大枠をまたぐ依存: 配布手順の更新（分類済）→ 親課題「検索 API にページング」（分類済）
        need("C-19", "C-2", "ページングの配布は新しい手順で出す"),
      ],
      repos: [
        { challenge_id: "C-1", repos: [WEB, AUTH] },
        { challenge_id: "C-2", repos: [API, SEARCH] },
        { challenge_id: "C-3", repos: [API, NOTIFY] },
        { challenge_id: "C-4", repos: [WEB, API, ADMIN] },
        { challenge_id: "C-5", repos: [INFRA, CI] },
        { challenge_id: "C-6", repos: [INFRA, BATCH] },
        { challenge_id: "C-7", repos: [API, WEB, BILLING] },
        { challenge_id: "C-8", repos: [API, BATCH] },
        { challenge_id: "C-9", repos: [API, IMAGE] },
        { challenge_id: "C-10", repos: [WEB] },
        { challenge_id: "C-11", repos: [INFRA, CI] },
        { challenge_id: "C-12", repos: [INFRA] },
        { challenge_id: "C-13", repos: [API] },
        { challenge_id: "C-14", repos: [INFRA, API] },
        { challenge_id: "C-15", repos: [INFRA] },
        { challenge_id: "C-16", repos: [INFRA] },
        { challenge_id: "C-17", repos: [INFRA] },
        { challenge_id: "C-18", repos: [API, INFRA] },
        { challenge_id: "C-19", repos: [INFRA, DOCS] },
        { challenge_id: "C-20", repos: [API, BILLING] },
      ],
      // 外部待ち（自動では解けない待ち）の例。自動の確認待ち（C-11 の waiting_ci）はここに入れない
      waiting_party: [
        {
          challenge_id: "C-20",
          party: "決済チーム",
          reason: "返金 API の 1 日あたりの上限を問い合わせ中",
        },
      ],
    },
    beta: {
      relations: [clash("C-2", "C-1", "同じ出力の設定（export.toml）を触る")],
      repos: [
        { challenge_id: "C-1", repos: [DOCS] },
        { challenge_id: "C-2", repos: [INFRA, DOCS] },
        { challenge_id: "C-3", repos: [INFRA] },
      ],
      waiting_party: [],
    },
  };
}

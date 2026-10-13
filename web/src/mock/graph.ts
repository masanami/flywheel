// 関係グラフの案（flywheel#176）のための偽の関係。開発時の mock モードだけが読み込む。
// store.ts の課題に対し、親の大枠が複数（入れ子の 2 段を含む）・大枠の中の依存の連鎖・大枠をまたぐ依存・
// 実施中の課題・畳んだ大枠の中の人間の操作待ち・複数のリポジトリ・関係の無い課題が見えるようにする（循環なし）。

import type {
  ChallengeGraphResponse,
  ChallengeRelation,
} from "../api/proposed.ts";

const dep = (from: string, to: string): ChallengeRelation => ({
  from,
  kind: "depends_on",
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

export function seedGraphs(): {
  alpha: ChallengeGraphResponse;
  beta: ChallengeGraphResponse;
} {
  return {
    alpha: {
      relations: [
        // 大枠「旧基盤の撤去」（C-14）: 設定の削除 → バッチの撤去（実施中）→ 移行の調査 → トリアージ
        parent("C-14", "C-12"),
        parent("C-14", "C-6"),
        parent("C-14", "C-8"),
        parent("C-14", "C-13"),
        dep("C-12", "C-6"),
        dep("C-6", "C-8"),
        dep("C-8", "C-13"),
        // 大枠「検索 API にページング」（C-2）: 権限チェック → 文言・外部 Issue → 画像変換（分岐と合流）
        parent("C-2", "C-4"),
        parent("C-2", "C-1"),
        parent("C-2", "C-10"),
        parent("C-2", "C-9"),
        dep("C-4", "C-1"),
        dep("C-4", "C-10"),
        dep("C-1", "C-9"),
        dep("C-10", "C-9"),
        // 大枠「CI と配布の改善」（C-15）の中に、さらに大枠「依存のキャッシュ」（C-16）を置く
        parent("C-15", "C-5"),
        parent("C-15", "C-11"),
        parent("C-15", "C-16"),
        parent("C-16", "C-17"),
        dep("C-17", "C-5"),
        dep("C-5", "C-11"),
        // 大枠をまたぐ依存: CI 待ち → 料金表
        dep("C-11", "C-7"),
      ],
      repos: [
        { challenge_id: "C-1", repos: [WEB] },
        { challenge_id: "C-2", repos: [API] },
        { challenge_id: "C-3", repos: [API] },
        { challenge_id: "C-4", repos: [WEB, API] },
        { challenge_id: "C-5", repos: [INFRA] },
        { challenge_id: "C-6", repos: [INFRA] },
        { challenge_id: "C-7", repos: [API, WEB] },
        { challenge_id: "C-8", repos: [API] },
        { challenge_id: "C-9", repos: [API] },
        { challenge_id: "C-10", repos: [WEB] },
        { challenge_id: "C-11", repos: [INFRA] },
        { challenge_id: "C-12", repos: [INFRA] },
        { challenge_id: "C-13", repos: [API] },
        { challenge_id: "C-14", repos: [INFRA, API] },
        { challenge_id: "C-15", repos: [INFRA] },
        { challenge_id: "C-16", repos: [INFRA] },
        { challenge_id: "C-17", repos: [INFRA] },
      ],
    },
    beta: {
      relations: [dep("C-2", "C-3")],
      repos: [
        { challenge_id: "C-1", repos: [DOCS] },
        { challenge_id: "C-2", repos: [INFRA, DOCS] },
        { challenge_id: "C-3", repos: [INFRA] },
      ],
    },
  };
}

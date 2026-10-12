// 関係グラフの案（flywheel#176）のための偽の関係。開発時の mock モードだけが読み込む。
// store.ts の課題に対し、依存の連鎖・分岐と合流（循環なし）・親子・複数のリポジトリ・関係の無い課題が見えるようにする。

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
        // 連鎖: 設定の削除 → バッチの撤去 → 移行の調査 → トリアージ
        dep("C-12", "C-6"),
        dep("C-6", "C-8"),
        dep("C-8", "C-13"),
        // 分岐と合流: 権限チェック → 文言・外部 Issue → 画像変換
        dep("C-4", "C-1"),
        dep("C-4", "C-10"),
        dep("C-1", "C-9"),
        dep("C-10", "C-9"),
        // CI の連鎖: ビルドの高速化 → CI 待ち → 料金表
        dep("C-5", "C-11"),
        dep("C-11", "C-7"),
        // 親子: 検索 API の課題を親に、権限チェックとビルドの高速化を子に置く
        parent("C-2", "C-4"),
        parent("C-2", "C-5"),
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

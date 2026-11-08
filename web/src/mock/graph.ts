// 関係グラフ（flywheel#176）の偽の関係。開発時の mock モードだけが読み込む。
// store.ts の課題に対し、親の大枠が複数（入れ子の 2 段を含む）・大枠の中の依存（分岐と合流）・大枠をまたぐ依存・
// 実施中の課題・畳んだ大枠の中の人間の操作待ち・関係の無い課題が見えるようにする（循環なし）。
// 依存は種類（先行の成果を使う・同時に走らせると衝突する）と登録の経路（計画の判断・端末のコマンド・不明）が
// 一通り出るようにする。区分・running・stale は store.ts が課題のカードの導出と同じ規則で作る。

export type MockEdgeKind = "prerequisite" | "conflict";
export type MockVia = "plan" | "manual" | "unknown";

export interface MockDep {
  // from が先行（依存先）、to が後続（依存元）
  from: string;
  to: string;
  kind: MockEdgeKind;
  via: MockVia;
  // 登録の日時（2026-10-10T00:00:00Z からの経過分）
  minutes: number;
}

export interface GraphSeed {
  // 子 → 親
  parents: Record<string, string>;
  deps: MockDep[];
}

const dep = (
  from: string,
  to: string,
  kind: MockEdgeKind,
  via: MockVia,
  n: number,
): MockDep => ({ from, to, kind, via, minutes: 60 * (n + 1) });

// 依存の後続（to）は、先行（from）が完了していない限り分類済か計画承認待ちに留まる
// （mock/graph.test.ts が検査する）。着手済みの課題は、完了した先行の後ろか連鎖の先頭に置く。
export function seedGraphs(): { alpha: GraphSeed; beta: GraphSeed } {
  return {
    alpha: {
      parents: {
        "C-12": "C-14",
        "C-6": "C-14",
        "C-8": "C-14",
        "C-13": "C-14",
        "C-18": "C-14",
        "C-4": "C-2",
        "C-1": "C-2",
        "C-10": "C-2",
        "C-9": "C-2",
        "C-5": "C-15",
        "C-11": "C-15",
        "C-16": "C-15",
        "C-19": "C-15",
        "C-17": "C-16",
        "C-21": "C-14",
        "C-26": "C-3",
        "C-27": "C-1",
        "C-29": "C-28",
        "C-30": "C-28",
      },
      deps: [
        dep("C-12", "C-6", "prerequisite", "plan", 0),
        dep("C-12", "C-8", "conflict", "plan", 1),
        dep("C-12", "C-13", "prerequisite", "manual", 2),
        dep("C-6", "C-18", "prerequisite", "plan", 3),
        dep("C-8", "C-18", "prerequisite", "unknown", 4),
        dep("C-10", "C-4", "conflict", "plan", 5),
        dep("C-9", "C-4", "prerequisite", "plan", 6),
        dep("C-17", "C-19", "prerequisite", "plan", 7),
        dep("C-5", "C-19", "prerequisite", "manual", 8),
        dep("C-11", "C-19", "conflict", "plan", 9),
        dep("C-19", "C-2", "prerequisite", "unknown", 10),
        dep("C-21", "C-18", "prerequisite", "manual", 11),
        dep("C-23", "C-24", "prerequisite", "manual", 12),
        // 大枠の外の C-31 から、説明の文が長い親 C-28 の子 C-29 へ（線が親の見出しを縦切る。flywheel#482）
        dep("C-31", "C-29", "prerequisite", "plan", 13),
      ],
    },
    beta: {
      parents: {},
      deps: [dep("C-2", "C-1", "conflict", "manual", 0)],
    },
  };
}

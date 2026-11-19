// 課題どうしの偽の前提（依存）。開発時の mock モードだけが読み込む。課題の詳細の waiting（まだ終わっていない前提）が使う。
// store.ts の課題に対し、親子の中の依存（分岐と合流）・親子をまたぐ依存が見えるようにする（循環なし）。

export type MockEdgeKind = "prerequisite";

export interface MockDep {
  // 前提（prerequisite）は from が先行（依存先）、to が後続（依存元）
  from: string;
  to: string;
  kind: MockEdgeKind;
}

export interface DepSeed {
  deps: MockDep[];
}

const dep = (from: string, to: string, kind: MockEdgeKind): MockDep => ({
  from,
  to,
  kind,
});

// 依存の後続（to）は、先行（from）が完了していない限り分類済か計画承認待ちに留まる。
// 着手済みの課題は、完了した先行の後ろか連鎖の先頭に置く。
export function seedDeps(): { alpha: DepSeed; beta: DepSeed } {
  return {
    alpha: {
      deps: [
        dep("C-12", "C-6", "prerequisite"),
        dep("C-12", "C-13", "prerequisite"),
        dep("C-6", "C-18", "prerequisite"),
        dep("C-8", "C-18", "prerequisite"),
        dep("C-9", "C-4", "prerequisite"),
        dep("C-17", "C-19", "prerequisite"),
        dep("C-5", "C-19", "prerequisite"),
        dep("C-19", "C-2", "prerequisite"),
        dep("C-21", "C-18", "prerequisite"),
        dep("C-23", "C-24", "prerequisite"),
        // 親子の外の C-34 から、説明の文が長い親 C-31 の子 C-32 へ（課題の詳細の「関係する課題」に出る。flywheel#482）
        dep("C-34", "C-32", "prerequisite"),
      ],
    },
    beta: {
      deps: [],
    },
  };
}

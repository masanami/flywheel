// server にまだ無い読み取りの口の案（UX モック段）。列の見出しの案を見せるために、画面が欲しい形を置く。
// 読み取りの型は types.gen.ts（Go から生成）が正本。ここの型は「仕様の変更案」として PR で挙げ、
// 本物の API が決まったら types.gen.ts 側へ移して置き換える。

// 失敗は CLI と同じエラーコードの閉集合（仕様 §IF / API「エラーコードと HTTP の状態コード」）。
export class ApiError extends Error {
  readonly code: string;
  readonly status: number;
  constructor(code: string, message: string, status: number) {
    super(message);
    this.name = "ApiError";
    this.code = code;
    this.status = status;
  }
}

// 旧 board の列の見出しにあった情報のうち、引き継ぐもの（サイクルの状態・優先度方針のモード）。
// 差し込みの表示は引き継がない（flywheel#207 の決定 6）。
export interface CycleInfo {
  cycle_state: "idle" | "running" | "stale";
  last_cycle_at: string | null;
  policy_mode: string;
}

// 画面が使う呼び出し口。本物（未実装）とモック（ブラウザ内）が同じ形を実装する。
export interface ProposedApi {
  cycleInfo(ws: string): Promise<CycleInfo>;
  challengeGraph(ws: string): Promise<ChallengeGraphResponse>;
}

// 課題どうしの関係（関係グラフの案・flywheel#176）。本物では core が依存（M4.5 の challenge_dependency）・
// 親子・触るリポジトリの記録から決定的に導く（LLM に書かせない）。案の口は
// GET /api/v1/workspaces/{name}/graph とし、ワークスペース 1 つ分の関係をまとめて返す。
export type RelationKind = "depends_on" | "parent_of";

export interface ChallengeRelation {
  // depends_on は from が先行・to が後続（from が完了すると to が進める）。parent_of は from が親・to が子。
  from: string;
  kind: RelationKind;
  to: string;
}

export interface ChallengeRepos {
  challenge_id: string;
  repos: string[];
}

// 自動では解けない待ち（他チームへの相談・外部のレビュー・ベンダーの回答など）の案の印 waiting_party。
// core にはまだ表す状態・印が無い（足すものは M5 以降に決める）。CI 待ち（waiting_ci）は数分で自動で解けるので
// ここに入れない（2026-10-14 オーナー）。
export interface PartyWait {
  challenge_id: string;
  // 待っている相手（チーム・ベンダーなど）と、待っている理由
  party: string;
  reason: string;
}

export interface ChallengeGraphResponse {
  relations: ChallengeRelation[];
  repos: ChallengeRepos[];
  waiting_party: PartyWait[];
}

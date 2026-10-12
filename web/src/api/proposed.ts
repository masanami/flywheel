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
}

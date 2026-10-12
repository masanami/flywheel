// 書き込み API の呼び出しの形（S2・S3）。サーバ側にはまだ無く、モック段で画面が欲しい形を置く。
// 読み取りの型は types.gen.ts（Go から生成）が正本。ここの型は「仕様の変更案」として PR で挙げ、
// 本物の API が決まったら types.gen.ts 側へ移して置き換える。

import type { Challenge } from "./types.gen.ts";

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

export const URGENCIES = ["高", "中", "低"] as const;
export const PRIORITIES = ["P0", "P1", "P2"] as const;
export type Priority = (typeof PRIORITIES)[number];

export interface CreateInput {
  title: string;
  description: string;
  done_criteria: string;
  urgency: string | null;
}

// 編集は UI が表示していた課題の版を添える（M4P8）。違えば conflict。
export interface EditInput {
  version: number;
  title: string;
  description: string;
  done_criteria: string;
  urgency: string | null;
  // S4: 人が直せる「一言で」「位置づけ」
  headline?: string | null;
  context?: string | null;
}

export interface PrioritizeInput {
  version: number;
  priority: Priority;
}

export interface ChallengeResponse {
  challenge: Challenge;
}

// 承認の対象。課題（計画承認・完了確認）か不可逆操作。
export interface ApprovalTarget {
  kind: "challenge" | "operation";
  id: string;
}

// ① 確認画面を出す: 対象ごとの要約と、承認する版。集合はここで固定する（M4P20）。
export interface ApprovalItem {
  kind: "challenge" | "operation";
  id: string;
  title: string;
  approval_kind: "plan" | "completion" | "operation";
  version: number;
  // 完了の承認で、同時に承認される不可逆操作（release）の ID
  simultaneous_operation_ids: string[];
  hold_release: boolean;
  summary: string;
  // 完了の承認の確認で示す PR の head
  pr_head: string | null;
}

export interface ApprovalPrepared {
  // server が 1 回の操作ごとに作る署名の対象の識別子（5 分で失効）
  challenge_id: string;
  expires_at: string;
  items: ApprovalItem[];
}

export interface ApprovalExecuteInput {
  challenge_id: string;
  items: { id: string; version: number; hold_release: boolean }[];
  // モックでは見た目だけ。本物は WebAuthn の署名が入る。
  attestation: "mock-passkey";
}

export interface ApprovalResult {
  approved: string[];
}

// 差し戻し・保留への回答・予算の増額は 1 件ずつ。
export type DecisionRequest =
  | { action: "reject"; challenge_id: string; reason: string }
  | { action: "answer"; challenge_id: string; answer: string }
  | { action: "increase_budget"; challenge_id: string; amount_usd: number };

export interface DecisionPrepared {
  sign_id: string;
  expires_at: string;
  challenge_id: string;
  title: string;
  version: number;
  action: DecisionRequest["action"];
  summary: string;
}

export interface DecisionExecuteInput {
  sign_id: string;
  version: number;
  attestation: "mock-passkey";
}

export interface DecisionResult {
  challenge_id: string;
  action: DecisionRequest["action"];
}

// S4 のカードに足したい項目（一言で・位置づけは types.gen.ts の Card にある）。API に無いので案として置く。
export interface CardExtra {
  // 完了したら見えるもの。予想はいつでも、実物は完了後に入る。
  expected: string | null;
  actual: string | null;
  not_doing: string[];
  // 図（プレーンテキストのまま出す。Mermaid 等の解釈はしない）
  diagram: string | null;
}

// 旧 board の列の見出しにあった情報（置き場所の案を見せるための材料）。API に無いので案として置く。
export interface CycleInfo {
  cycle_state: "idle" | "running" | "stale";
  last_cycle_at: string | null;
  policy_mode: string;
  adhoc_active: boolean;
  adhoc_label: string | null;
}

// 画面が使う呼び出し口。本物（fetch）とモック（ブラウザ内）が同じ形を実装する。
export interface WriteApi {
  cardExtra(ws: string, id: string): Promise<CardExtra>;
  cycleInfo(ws: string): Promise<CycleInfo>;
  createChallenge(ws: string, input: CreateInput): Promise<ChallengeResponse>;
  editChallenge(
    ws: string,
    id: string,
    input: EditInput,
  ): Promise<ChallengeResponse>;
  prioritize(
    ws: string,
    id: string,
    input: PrioritizeInput,
  ): Promise<ChallengeResponse>;
  prepareApproval(
    ws: string,
    targets: ApprovalTarget[],
  ): Promise<ApprovalPrepared>;
  executeApproval(
    ws: string,
    input: ApprovalExecuteInput,
  ): Promise<ApprovalResult>;
  prepareDecision(ws: string, req: DecisionRequest): Promise<DecisionPrepared>;
  executeDecision(
    ws: string,
    input: DecisionExecuteInput,
  ): Promise<DecisionResult>;
}

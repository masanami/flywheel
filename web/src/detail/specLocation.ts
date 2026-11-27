import type { ShowChallenge } from "../api/types.gen.ts";

// 下書きづくりが途中の状態（本文を返す口が、記録の下書きの場所を読む状態）。
const DRAFTING_STATES = ["pending", "running", "awaiting_owner"];

// 仕様の文書を開く口を出すか。本文を返す口が読む場所を決められる条件と同じ。
export function canOpenSpec(c: ShowChallenge): boolean {
  return (
    c.spec_path !== null ||
    (c.spec_draft !== null && DRAFTING_STATES.includes(c.spec_draft.state))
  );
}

// 開いた本文を畳み直す単位。課題・読む場所・下書きづくりの実行の回が変わったら、前の中身を残さない。
export function specDocumentKey(agentName: string, c: ShowChallenge): string {
  return JSON.stringify([
    agentName,
    c.id,
    c.spec_path ?? c.spec_draft?.path ?? null,
    c.spec_draft?.attempts ?? null,
  ]);
}

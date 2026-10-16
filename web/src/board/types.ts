// board の画面部品が使う表示モデル。新しい API の応答は adapt.ts でこの形へ写す
// （台帳の語彙は持たず、状態の導出は core が返した値をそのまま使う）。

import type { MarkKind } from "../labels.ts";

export type ApprovalKind = "plan" | "completion";

export type ParseError = {
  file: string;
  line?: number;
  message: string;
  raw: string;
};

export type Challenge = {
  id: string;
  title: string;
  // 表示の文言（labels.ts の STATE_LABELS）。
  status: string;
  // core が返した状態のコード。
  state: string;
  priority?: string;
  needsHuman: boolean;
  // card.headline。null（未設定）なら無し。面の見出しは headline ?? title。
  headline?: string;
  // 区分の印（board/marks.ts が既存の API の値から導く）。
  marks: MarkKind[];
  description?: string;
  completionCriteria?: string;
  positioning?: string;
  currentState?: string;
  nextHumanAction?: string;
  modifiers: string[];
  nextHumanActions: string[];
};

// 実行中の課題（running 修飾を持つもの）。
export type RunningChallenge = {
  challengeId: string;
  title: string;
  unresponsive: boolean;
};

export type AgentBoard = {
  name: string;
  path: string;
  challenges: Challenge[];
  parseErrors: ParseError[];
  runningRuns: RunningChallenge[];
};

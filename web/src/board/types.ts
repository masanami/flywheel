// board の画面部品が使う表示モデル。新しい API の応答は adapt.ts でこの形へ写す
// （台帳の語彙は持たず、状態の導出は core が返した値をそのまま使う）。

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
  summary?: string;
  description?: string;
  completionCriteria?: string;
  oneLiner?: string;
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

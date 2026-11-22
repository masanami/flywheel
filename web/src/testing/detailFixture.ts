import type { ShowCardResponse } from "../api/types.gen.ts";
import { fixtures } from "./fakes.ts";

// 課題の詳細の画面のテストが共有する、課題の詳細の API の偽の応答（#808・#809）。
const BASE = "/api/v1/workspaces/alpha";

export type Over = Partial<
  Omit<ShowCardResponse, "tree" | "challenge" | "card">
> & {
  tree?: Partial<ShowCardResponse["tree"]>;
  challenge?: Partial<ShowCardResponse["challenge"]>;
  actions?: string[];
};

export function show(over: Over = {}): ShowCardResponse {
  const { tree, challenge, actions, ...rest } = over;
  const acts = actions ?? [];
  return {
    approvals: [],
    budget: { estimate_usd: null, limit_reason: null, limit_usd: null },
    card: {
      context: null,
      headline: null,
      modifiers: [],
      next_human_actions: acts,
      slot_ids: [],
      state: "in_progress",
    },
    challenge: {
      context: null,
      created_at: "2026-01-01T00:00:00Z",
      dependencies: [],
      dependents: [],
      description: "説明の本文",
      done_criteria: "完了条件の本文",
      followup_of: null,
      headline: null,
      id: "C-1",
      improvement: null,
      integration_ref: null,
      priority: null,
      reporter: "me",
      status: "in_progress",
      status_label: "着手中",
      title: "請求の集計を直す",
      updated_at: "2026-01-01T00:00:00Z",
      urgency: null,
      uses_integration: null,
      version: 3,
      ...challenge,
    },
    conflict_fixes: [],
    direct_completions: [],
    followups: [],
    holds: [],
    operations: [],
    pairs: [],
    plans: [],
    runs: [],
    source_binding: null,
    source_children: [],
    stages: {
      actual: null,
      expected: null,
      figure: null,
      issues: null,
      issues_answer: null,
      non_goals: null,
    },
    tasks: [],
    waiting: { prerequisites: [] },
    tree: {
      budget_usd: null,
      children: [],
      cost_usd: 0,
      dependencies: [],
      followups_open: 0,
      id: "C-1",
      next_human_actions: acts,
      parent: null,
      progress: null,
      status: "in_progress",
      status_label: "着手中",
      tasks: [],
      tasks_done: 0,
      tasks_total: 0,
      title: "請求の集計を直す",
      updated_at: "2026-11-01T00:00:00.000Z",
      uses_integration: null,
      ...tree,
    },
    ...rest,
  };
}

export function bodies(
  detail: ShowCardResponse,
  extra: Record<string, unknown> = {},
) {
  const f = fixtures();
  return {
    ...f,
    [`${BASE}/challenges`]: {
      challenges: [
        { ...detail.challenge, card: detail.card },
        // 題名を引くための別の課題
        {
          ...detail.challenge,
          id: "C-2",
          title: "後から続く課題",
          card: detail.card,
        },
      ],
    },
    [`${BASE}/challenges/C-1`]: detail,
    ...extra,
  };
}

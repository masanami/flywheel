import { act, render } from "@testing-library/react";
import { App } from "../App.tsx";
import { FakeEventSource, emptyStatus, installFakes } from "./fakes.ts";

// 画面のテストが使う、エージェントごとの偽の応答の組み立て。

export interface Agent {
  name: string;
  state?: string;
  error?: string | null;
  cycle?: {
    state?: string;
    last_started_at?: string | null;
    last_ended_at?: string | null;
    last_result?: string | null;
  };
  cycleWork?: { total: number; stages?: Record<string, number> };
  needsHuman?: Record<string, unknown>;
  cards?: { id: string; title: string }[];
}

export const nhBase = {
  budget_exhausted: [],
  challenges: [],
  discrepancies: [],
  operations: [],
  slots: [],
  triage: [],
};

export function bodiesOf(agents: Agent[]): Record<string, unknown> {
  const b: Record<string, unknown> = {
    "/api/v1/workspaces": {
      workspaces: agents.map((a) => ({
        error: a.error ?? null,
        name: a.name,
        path: `/w/${a.name}`,
        state: a.state ?? "ok",
      })),
    },
    "/api/v1/needs-human": {
      unreadable_workspaces: agents
        .filter((a) => (a.state ?? "ok") !== "ok")
        .map((a) => a.name),
      workspaces: agents
        .filter((a) => (a.state ?? "ok") === "ok")
        .map((a) => ({
          name: a.name,
          needs_human: { ...nhBase, ...(a.needsHuman ?? {}) },
        })),
    },
    "/api/v1/questions": { questions: [], unreadable_workspaces: [] },
  };
  for (const a of agents) {
    if ((a.state ?? "ok") !== "ok") continue;
    const p = `/api/v1/workspaces/${a.name}`;
    b[`${p}/status`] = {
      ...emptyStatus,
      cycle_work: {
        stages: { ...emptyStatus.cycle_work.stages, ...a.cycleWork?.stages },
        total: a.cycleWork?.total ?? 0,
      },
    };
    b[`${p}/challenges`] = { challenges: a.cards ?? [] };
    b[`${p}/cycle`] = {
      last_ended_at: null,
      last_result: null,
      last_started_at: null,
      state: "idle",
      ...a.cycle,
    };
  }
  return b;
}

export async function openAgents(
  agents: Agent[],
  hash = "",
  extra: Record<string, unknown> = {},
) {
  window.location.hash = hash;
  const fakes = installFakes({ ...bodiesOf(agents), ...extra });
  render(<App />);
  await act(async () => {
    FakeEventSource.latest.open();
  });
  return fakes;
}

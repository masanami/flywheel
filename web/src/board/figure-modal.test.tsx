import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { App } from "../App.tsx";
import type { ShowCardResponse } from "../api/types.gen.ts";
import { FakeEventSource, fixtures, installFakes } from "../testing/fakes.ts";
import { type MermaidLike, setMermaidLoader } from "./loadMermaid.ts";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setMermaidLoader();
});

function detail(figure: string | null): ShowCardResponse {
  return {
    approvals: [],
    card: {
      context: null,
      headline: null,
      modifiers: [],
      next_human_actions: [],
      slot_ids: [],
      state: "in_progress",
    },
    challenge: {
      context: null,
      created_at: "2026-01-01T00:00:00Z",
      description: "説明",
      done_criteria: "完了条件",
      headline: null,
      id: "C-1",
      priority: null,
      reporter: "me",
      status: "in_progress",
      status_label: "着手中",
      title: "タイトル",
      updated_at: "2026-01-01T00:00:00Z",
      urgency: null,
      version: 1,
    },
    holds: [],
    operations: [],
    plans: [],
    runs: [],
    pairs: [],
    source_binding: null,
    stages: {
      actual: null,
      expected: null,
      figure,
      issues: null,
      issues_answer: null,
      non_goals: null,
    },
  };
}

async function setup(figure: string | null) {
  const d = detail(figure);
  const f = fixtures();
  installFakes({
    ...f,
    "/api/v1/workspaces/alpha/challenges": {
      challenges: [{ ...d.challenge, card: d.card }],
    },
    "/api/v1/workspaces/alpha/challenges/C-1": d,
    "/api/v1/workspaces/alpha/log?challenge=C-1": { activities: [] },
  });
  const mermaid: MermaidLike = {
    initialize: vi.fn(),
    render: vi.fn(async () => ({ svg: "<svg></svg>" })),
  };
  const loader = vi.fn(async () => mermaid);
  setMermaidLoader(loader);
  render(<App />);
  act(() => FakeEventSource.latest.open());
  const card = await screen.findByTestId("card-C-1");
  return { card, loader };
}

it("ボードだけ・図の無い課題の詳細では読み込みの関数を呼ばない（AC-381・AC-382）", async () => {
  const { card, loader } = await setup(null);
  expect(loader).not.toHaveBeenCalled();
  fireEvent.click(within(card).getByRole("button"));
  await screen.findByTestId("card-detail-content");
  expect(loader).not.toHaveBeenCalled();
});

it("図のある課題の詳細で読み込みの関数を呼んで描く（AC-383）", async () => {
  const { card, loader } = await setup("graph TD; A-->B");
  expect(loader).not.toHaveBeenCalled();
  fireEvent.click(within(card).getByRole("button"));
  await waitFor(() =>
    expect(screen.getByTestId("figure-svg")).toBeInTheDocument(),
  );
  expect(loader).toHaveBeenCalledTimes(1);
});

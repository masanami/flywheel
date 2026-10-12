import { render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it } from "vitest";
import { resetApi, setApi } from "../api/client.ts";
import type { ApiClient } from "../api/client.ts";
import type { LiveState } from "../live/useLive.ts";
import { createMockApi } from "../mock/api.ts";
import { MockStore } from "../mock/store.ts";
import { CycleHeaders } from "./CycleHeaders.tsx";
import { NeedsHumanStrip, countsOf } from "./NeedsHumanStrip.tsx";

let api: ApiClient;
beforeEach(() => {
  api = createMockApi(new MockStore(), { latencyMs: 0 });
  setApi(api);
});
afterEach(() => resetApi());

it("人間の操作待ちの帯: 承認以外の区分も数える", async () => {
  const nh = await api.needsHuman();
  const alpha = nh.workspaces.find((w) => w.name === "alpha")?.needs_human;
  if (!alpha) throw new Error("alpha が無い");
  expect(countsOf(alpha)).toMatchObject({
    approvals: 3,
    questions: 1,
    operations: 1,
    discrepancies: 1,
    budget: 1,
    triage: 1,
  });
  const live = { needsHuman: nh } as unknown as LiveState;
  render(<NeedsHumanStrip live={live} />);
  expect(screen.getByText("予算切れ 1")).toBeInTheDocument();
  expect(screen.getByText(/人間の操作待ち \d+ 件/)).toBeInTheDocument();
});

it("列の見出しの案: サイクルの状態と方針のモードを出し、差し込みは出さない", async () => {
  const { workspaces } = await api.workspaces();
  render(<CycleHeaders workspaces={workspaces} refreshKey={0} />);
  const region = screen.getByRole("region", { name: "列の見出しの案" });
  expect(await within(region).findByText("サイクル実行中")).toBeInTheDocument();
  expect(within(region).getByText("停止中")).toBeInTheDocument();
  expect(
    within(region).getByText("優先度方針: 厳格（P0 のみ自走）"),
  ).toBeInTheDocument();
  expect(region.textContent).not.toContain("差し込み");
  // 読めないワークスペース（gamma）は見出しを出さない
  expect(region.textContent).not.toContain("gamma");
});

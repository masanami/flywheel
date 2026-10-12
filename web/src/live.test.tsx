import { act, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { App } from "./App.tsx";
import { RECONNECT_MS } from "./live/useLive.ts";
import { FakeEventSource, fixtures, installFakes } from "./testing/fakes.ts";

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

async function mounted() {
  const { requests } = installFakes();
  render(<App />);
  act(() => FakeEventSource.latest.open());
  await waitFor(() => expect(screen.getByText(/alpha/)).toBeInTheDocument());
  return requests;
}

it("AC-87: workspace_changed は該当 ws の status/challenges と fleet の集約だけを取り直す", async () => {
  const requests = await mounted();
  requests.length = 0;
  act(() =>
    FakeEventSource.latest.send({
      type: "workspace_changed",
      workspace: "alpha",
    }),
  );
  await waitFor(() => expect(requests).toHaveLength(5));
  expect(requests.map((r) => r.url).sort()).toEqual(
    [
      "/api/v1/workspaces",
      "/api/v1/workspaces/alpha/status",
      "/api/v1/workspaces/alpha/challenges",
      "/api/v1/needs-human",
      "/api/v1/questions",
    ].sort(),
  );
  expect(requests.some((r) => r.url.includes("/beta/"))).toBe(false);
});

it("keep-alive 等の workspace_changed でない通知では何も要求しない", async () => {
  const requests = await mounted();
  requests.length = 0;
  act(() => FakeEventSource.latest.send({ type: "other" }));
  expect(requests).toHaveLength(0);
});

it("AC-88/89: 切断すると切れていることと最終更新時刻を表示する", async () => {
  // 取得の時刻を固定し、その時刻の表示（toLocaleString）が切断後も残ることを確かめる。
  const fetchedAt = new Date("2026-03-04T05:06:07Z");
  vi.setSystemTime(fetchedAt);
  await mounted();
  expect(screen.getByRole("status")).toHaveTextContent("接続中");
  // 切断の時刻は取得の時刻と離しておく（最終更新は最後に取得できた時刻であり、切断の時刻ではない）。
  vi.setSystemTime(new Date(fetchedAt.getTime() + 3_600_000));
  act(() => FakeEventSource.latest.fail());
  const status = screen.getByRole("status");
  expect(status).toHaveTextContent("切断中");
  expect(status).toHaveTextContent("最終更新:");
  expect(status).not.toHaveTextContent("未取得");
  // 時計は実時間で少し進むので、取得の時刻から数秒以内の表示のどれかを含めばよい。
  const shown = [0, 1, 2, 3].map((sec) =>
    new Date(fetchedAt.getTime() + sec * 1000).toLocaleString(),
  );
  expect(shown.some((t) => status.textContent?.includes(t))).toBe(true);
  expect(status.textContent).not.toContain(
    new Date(fetchedAt.getTime() + 3_600_000).toLocaleString(),
  );
});

it("AC-90/91: 切断後に再接続を試み、つながったら全ワークスペースを読み直す", async () => {
  const requests = await mounted();
  const first = FakeEventSource.latest;
  act(() => first.fail());
  expect(first.closed).toBe(true);
  const n = FakeEventSource.instances.length;
  act(() => {
    vi.advanceTimersByTime(RECONNECT_MS);
  });
  expect(FakeEventSource.instances.length).toBe(n + 1);
  requests.length = 0;
  act(() => FakeEventSource.latest.open());
  await waitFor(() => expect(requests.length).toBe(7));
  expect(requests.map((r) => r.url)).toEqual(
    expect.arrayContaining([
      "/api/v1/workspaces",
      "/api/v1/workspaces/alpha/status",
      "/api/v1/workspaces/alpha/challenges",
      "/api/v1/workspaces/beta/status",
      "/api/v1/workspaces/beta/challenges",
      "/api/v1/needs-human",
      "/api/v1/questions",
    ]),
  );
  expect(screen.getByRole("status")).toHaveTextContent("接続中");
});

it("fleet の集約の取得に失敗しても、一覧と各ワークスペースの表示は残る", async () => {
  const b = Object.fromEntries(
    Object.entries(fixtures()).filter(([k]) => k !== "/api/v1/questions"),
  );
  installFakes(b);
  render(<App />);
  act(() => FakeEventSource.latest.open());
  await waitFor(() => expect(screen.getByText(/alpha/)).toBeInTheDocument());
  await waitFor(() =>
    expect(screen.getAllByRole("alert").length).toBeGreaterThan(0),
  );
});

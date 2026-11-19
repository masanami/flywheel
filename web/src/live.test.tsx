import { act, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { App } from "./App.tsx";
import { CYCLE_STATE_LABELS } from "./labels.ts";
import { POLL_MS, RECONNECT_MS } from "./live/useLive.ts";
import { FakeEventSource, fixtures, installFakes } from "./testing/fakes.ts";

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
});
afterEach(() => {
  window.location.hash = "";
  vi.useRealTimers();
  vi.unstubAllGlobals();
  Reflect.deleteProperty(document, "visibilityState");
});

// useLive（一覧・集約・エージェントごとの読み込みと、通知・再接続・60 秒ごとの取り直し）の動きを、
// 画面の枠組み（#803。既定の行き先は先頭のエージェントの「いま進められること」）の上で確かめる。
const connection = () => screen.getByTestId("connection");

async function mounted() {
  const { requests } = installFakes();
  render(<App />);
  act(() => FakeEventSource.latest.open());
  await waitFor(() =>
    expect(screen.getAllByText(/alpha/).length).toBeGreaterThan(0),
  );
  return requests;
}

it("AC-87/204/206: workspace_changed は該当 ws の status/challenges/cycle と fleet の needs-human・questions だけを取り直す（#803 から問いの一覧も読む）", async () => {
  const requests = await mounted();
  requests.length = 0;
  act(() =>
    FakeEventSource.latest.send({
      type: "workspace_changed",
      workspace: "alpha",
    }),
  );
  await waitFor(() => expect(requests).toHaveLength(6));
  expect(requests.map((r) => r.url).sort()).toEqual(
    [
      "/api/v1/workspaces",
      "/api/v1/workspaces/alpha/status",
      "/api/v1/workspaces/alpha/challenges",
      "/api/v1/workspaces/alpha/cycle",
      "/api/v1/needs-human",
      "/api/v1/questions",
    ].sort(),
  );
  expect(requests.some((r) => r.url.includes("/beta/"))).toBe(false);
});

it("AC-205: 他のワークスペースへの通知では alpha の status・challenges・cycle を要求しない", async () => {
  const requests = await mounted();
  requests.length = 0;
  act(() =>
    FakeEventSource.latest.send({
      type: "workspace_changed",
      workspace: "beta",
    }),
  );
  await waitFor(() => expect(requests).toHaveLength(6));
  expect(requests.some((r) => r.url.includes("/alpha/"))).toBe(false);
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
  // つながっている間は「切断中」を出さない（#803 の AC-97。前は「接続中」と出していた）。
  expect(connection()).not.toHaveTextContent("切断中");
  // 切断の時刻は取得の時刻と離しておく（最終更新は最後に取得できた時刻であり、切断の時刻ではない）。
  vi.setSystemTime(new Date(fetchedAt.getTime() + 3_600_000));
  act(() => FakeEventSource.latest.fail());
  const status = connection();
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
  expect(connection()).toHaveTextContent("切断中");
  expect(first.closed).toBe(true);
  const n = FakeEventSource.instances.length;
  act(() => {
    vi.advanceTimersByTime(RECONNECT_MS);
  });
  expect(FakeEventSource.instances.length).toBe(n + 1);
  requests.length = 0;
  act(() => FakeEventSource.latest.open());
  await waitFor(() => expect(requests.length).toBe(9));
  expect(requests.map((r) => r.url)).toEqual(
    expect.arrayContaining([
      "/api/v1/workspaces",
      "/api/v1/workspaces/alpha/status",
      "/api/v1/workspaces/alpha/challenges",
      "/api/v1/workspaces/beta/status",
      "/api/v1/workspaces/beta/challenges",
      "/api/v1/workspaces/alpha/cycle",
      "/api/v1/workspaces/beta/cycle",
      "/api/v1/needs-human",
      "/api/v1/questions",
    ]),
  );
  expect(connection()).not.toHaveTextContent("切断中");
});

it("fleet の集約の取得に失敗しても、一覧と各ワークスペースの表示は残る", async () => {
  const b = Object.fromEntries(
    Object.entries(fixtures()).filter(([k]) => k !== "/api/v1/needs-human"),
  );
  installFakes(b);
  render(<App />);
  act(() => FakeEventSource.latest.open());
  await waitFor(() =>
    expect(screen.getAllByText(/alpha/).length).toBeGreaterThan(0),
  );
  await waitFor(() =>
    expect(screen.getAllByRole("alert").length).toBeGreaterThan(0),
  );
});

it("GET …/cycle だけが失敗しても、そのエージェントの画面は残り、サイクルの状態だけが出ない", async () => {
  const b = fixtures();
  b["/api/v1/workspaces/alpha/cycle"] = () =>
    new Response("{}", { status: 500 });
  installFakes(b);
  render(<App />);
  act(() => FakeEventSource.latest.open());
  // alpha（先頭。既定の行き先）の画面は出る。
  expect(
    await screen.findByRole("heading", {
      level: 2,
      name: "いま進められること",
    }),
  ).toBeInTheDocument();
  await waitFor(() =>
    expect(
      screen.getByRole("navigation", { name: "画面" }),
    ).toBeInTheDocument(),
  );
  expect(screen.queryByTestId("cycle-state")).toBeNull();
  expect(document.body).not.toHaveTextContent("500");
  expect(screen.queryByTestId("agent-error")).toBeNull();
});

function setVisibility(v: "visible" | "hidden") {
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    get: () => v,
  });
}

it("AC-206a: visible の間、59 秒では発行せず 60 秒ごとに各 ws の cycle と challenges だけを取り直す", async () => {
  setVisibility("visible");
  const requests = await mounted();
  requests.length = 0;
  act(() => {
    vi.advanceTimersByTime(POLL_MS - 1000);
  });
  expect(requests).toHaveLength(0);
  act(() => {
    vi.advanceTimersByTime(1000);
  });
  await waitFor(() => expect(requests).toHaveLength(4));
  expect(requests.map((r) => r.url).sort()).toEqual(
    [
      "/api/v1/workspaces/alpha/challenges",
      "/api/v1/workspaces/alpha/cycle",
      "/api/v1/workspaces/beta/challenges",
      "/api/v1/workspaces/beta/cycle",
    ].sort(),
  );
  expect(requests.every((r) => r.method === "GET")).toBe(true);
  requests.length = 0;
  act(() => {
    vi.advanceTimersByTime(POLL_MS);
  });
  await waitFor(() => expect(requests).toHaveLength(4));
});

it("AC-206b: hidden の間は 60 秒の何倍進めても発行しない", async () => {
  setVisibility("hidden");
  const requests = await mounted();
  requests.length = 0;
  act(() => {
    vi.advanceTimersByTime(POLL_MS * 5);
  });
  expect(requests).toHaveLength(0);
  setVisibility("visible");
});

it("AC-206c: 取り直した cycle が running→stale なら、上の帯のサイクルの状態が「止まっているかもしれない」に変わる", async () => {
  setVisibility("visible");
  const b = fixtures();
  let n = 0;
  b["/api/v1/workspaces/alpha/cycle"] = () => {
    n += 1;
    return new Response(
      JSON.stringify({
        last_ended_at: null,
        last_result: null,
        last_started_at: "2026-10-17T00:50:00Z",
        state: n === 1 ? "running" : "stale",
      }),
      { status: 200 },
    );
  };
  installFakes(b);
  render(<App />);
  act(() => FakeEventSource.latest.open());
  await waitFor(() =>
    expect(screen.getByTestId("cycle-state")).toHaveTextContent(
      CYCLE_STATE_LABELS.running,
    ),
  );
  act(() => {
    vi.advanceTimersByTime(POLL_MS);
  });
  await waitFor(() =>
    expect(screen.getByTestId("cycle-state")).toHaveTextContent(
      CYCLE_STATE_LABELS.stale,
    ),
  );
});

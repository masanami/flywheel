import { act, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { App } from "./App.tsx";
import { POLL_MS, RECONNECT_MS } from "./live/useLive.ts";
import { FakeEventSource, fixtures, installFakes } from "./testing/fakes.ts";

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  Reflect.deleteProperty(document, "visibilityState");
});

async function mounted() {
  const { requests } = installFakes();
  render(<App />);
  act(() => FakeEventSource.latest.open());
  await waitFor(() => expect(screen.getByText(/alpha/)).toBeInTheDocument());
  return requests;
}

it("AC-87/204/206: workspace_changed は該当 ws の status/challenges と fleet の needs-human だけを取り直し、questions は要求しない（cycle も取り直す）", async () => {
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
      "/api/v1/workspaces/alpha/cycle",
      "/api/v1/needs-human",
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
  await waitFor(() => expect(requests).toHaveLength(5));
  expect(requests.some((r) => r.url.includes("/alpha/"))).toBe(false);
});

it("AC-199/200: 列の見出しに状態と最後の周の終了時刻・結果が出て、周がまだ無ければそう示す", async () => {
  await mounted();
  const alpha = await screen.findByRole("region", { name: "alpha の列" });
  await waitFor(() =>
    expect(within(alpha).getByTestId("cycle-header")).toHaveTextContent(
      "実行中",
    ),
  );
  expect(within(alpha).getByTestId("cycle-header")).toHaveTextContent(
    "2026-10-17T01:00:00Z 完了",
  );
  const beta = screen.getByRole("region", { name: "beta の列" });
  expect(within(beta).getByTestId("cycle-header")).toHaveTextContent("停止中");
  expect(within(beta).getByTestId("cycle-header")).toHaveTextContent(
    "周はまだありません",
  );
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
  await waitFor(() => expect(requests.length).toBe(8));
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
    ]),
  );
  expect(screen.getByRole("status")).toHaveTextContent("接続中");
});

it("fleet の集約の取得に失敗しても、一覧と各ワークスペースの表示は残る", async () => {
  const b = Object.fromEntries(
    Object.entries(fixtures()).filter(([k]) => k !== "/api/v1/needs-human"),
  );
  installFakes(b);
  render(<App />);
  act(() => FakeEventSource.latest.open());
  await waitFor(() => expect(screen.getByText(/alpha/)).toBeInTheDocument());
  await waitFor(() =>
    expect(screen.getAllByRole("alert").length).toBeGreaterThan(0),
  );
});

it("GET …/cycle だけが失敗しても、その列の課題の表示は残り、見出しだけが出ない", async () => {
  const b = fixtures();
  b["/api/v1/workspaces/alpha/cycle"] = () =>
    new Response("{}", { status: 500 });
  installFakes(b);
  render(<App />);
  act(() => FakeEventSource.latest.open());
  const beta = await screen.findByRole("region", { name: "beta の列" });
  await waitFor(() =>
    expect(within(beta).getByTestId("cycle-header")).toBeInTheDocument(),
  );
  const alpha = screen.getByRole("region", { name: "alpha の列" });
  expect(within(alpha).queryByTestId("cycle-header")).toBeNull();
  expect(alpha).not.toHaveTextContent("500");
});

it("読めないワークスペースの列には見出しを出さない", async () => {
  const b = fixtures();
  b["/api/v1/workspaces"] = {
    workspaces: [
      { error: null, name: "alpha", path: "/w/alpha", state: "ok" },
      { error: "x", name: "gamma", path: "/w/gamma", state: "store_not_found" },
    ],
  };
  installFakes(b);
  render(<App />);
  act(() => FakeEventSource.latest.open());
  const gamma = await screen.findByRole("region", { name: "gamma の列" });
  await waitFor(() =>
    expect(
      within(screen.getByRole("region", { name: "alpha の列" })).getByTestId(
        "cycle-header",
      ),
    ).toBeInTheDocument(),
  );
  expect(within(gamma).queryByTestId("cycle-header")).toBeNull();
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

it("AC-206c: 取り直した cycle が running→stale なら見出しが「更新なし」に変わる", async () => {
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
  const alpha = await screen.findByRole("region", { name: "alpha の列" });
  await waitFor(() =>
    expect(within(alpha).getByTestId("cycle-header")).toHaveTextContent(
      "実行中",
    ),
  );
  act(() => {
    vi.advanceTimersByTime(POLL_MS);
  });
  await waitFor(() =>
    expect(within(alpha).getByTestId("cycle-header")).toHaveTextContent(
      "更新なし",
    ),
  );
});

it("AC-206c: 取り直した challenges の card.modifiers に unresponsive が加わるとカードに「更新なし」が出る", async () => {
  setVisibility("visible");
  const b = fixtures();
  let n = 0;
  b["/api/v1/workspaces/alpha/challenges"] = () => {
    n += 1;
    const modifiers = n === 1 ? ["running"] : ["running", "unresponsive"];
    return new Response(
      JSON.stringify({
        challenges: [
          {
            created_at: "",
            description: "",
            done_criteria: "",
            id: "C-1",
            priority: null,
            reporter: "",
            status: "in_progress",
            status_label: "",
            title: "題名C-1",
            updated_at: "",
            urgency: null,
            version: 1,
            card: {
              context: null,
              headline: null,
              modifiers,
              next_human_actions: [],
              slot_ids: [],
              state: "in_progress",
            },
          },
        ],
      }),
      { status: 200 },
    );
  };
  installFakes(b);
  render(<App />);
  act(() => FakeEventSource.latest.open());
  const alpha = await screen.findByRole("region", { name: "alpha の列" });
  await waitFor(() => expect(alpha).toHaveTextContent("題名C-1"));
  expect(alpha).not.toHaveTextContent("更新なし（要確認）");
  act(() => {
    vi.advanceTimersByTime(POLL_MS);
  });
  await waitFor(() => expect(alpha).toHaveTextContent("更新なし（要確認）"));
});

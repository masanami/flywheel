import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { App } from "./App.tsx";
import { screens } from "./screens.tsx";
import { FakeEventSource, fixtures, installFakes } from "./testing/fakes.ts";

afterEach(() => {
  vi.unstubAllGlobals();
  document.head.innerHTML = "";
});

// 本番の画面の一覧はボードだけ。ボードを操作したときの要求と DOM を検査する（詳細のモーダルは detail.test.tsx の T12 が検査する）。
async function operate(check: (container: HTMLElement, id: string) => void) {
  const { container } = render(<App />);
  act(() => FakeEventSource.latest.open());
  await waitFor(() => expect(screen.getByText(/alpha/)).toBeInTheDocument());
  for (const s of screens) {
    expect(screen.getByRole("region", { name: s.title })).toBeInTheDocument();
    check(container, s.id);
  }
}

it("AC-221: 全画面を操作しても fetch は GET と POST …/approve・POST …/cycle だけ、EventSource は events の購読だけ", async () => {
  document.head.innerHTML = '<meta name="flywheel-csrf" content="tok">';
  const { requests } = installFakes({
    ...fixtures(),
    "POST /api/v1/workspaces/alpha/cycle": { started: true },
  });
  await operate(() => {});
  fireEvent.click(screen.getByRole("button", { name: "完了を表示" }));
  for (const b of screen.getAllByRole("button", { name: "1 周を起動" })) {
    fireEvent.click(b);
  }
  act(() =>
    FakeEventSource.latest.send({
      type: "workspace_changed",
      workspace: "alpha",
    }),
  );
  await waitFor(() => expect(requests.length).toBeGreaterThan(5));
  await waitFor(() =>
    expect(requests.filter((r) => r.method === "POST")).toHaveLength(2),
  );
  const allowed = [
    /^\/api\/v1\/workspaces\/[^/]+\/challenges\/[^/]+\/approve$/,
    /^\/api\/v1\/workspaces\/[^/]+\/cycle$/,
  ];
  for (const r of requests) {
    if (r.method === "GET") continue;
    expect(r.method).toBe("POST");
    expect(
      allowed.some((re) => re.test(r.url)),
      r.url,
    ).toBe(true);
    // AC-222: POST はすべて CSRF の値を要求ヘッダに持つ
    expect(r.headers["X-Flywheel-CSRF"]).toBe("tok");
  }
  expect(
    FakeEventSource.instances.every((e) => e.url === "/api/v1/events"),
  ).toBe(true);
  document.head.innerHTML = "";
});

it("AC-124: 描画した画面の DOM に form 要素が無い", async () => {
  installFakes();
  const seen: string[] = [];
  await operate((container, id) => {
    seen.push(id);
    expect(container.querySelector("form"), id).toBeNull();
  });
  expect(seen).toEqual(screens.map((s) => s.id));
  expect(within(document.body).queryAllByRole("form")).toHaveLength(0);
});

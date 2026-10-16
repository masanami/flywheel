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
import { FakeEventSource, installFakes } from "./testing/fakes.ts";

afterEach(() => vi.unstubAllGlobals());

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

it("AC-123: ボードを操作しても fetch は GET だけ、EventSource は events の購読だけ", async () => {
  const { requests } = installFakes();
  await operate(() => {});
  fireEvent.click(screen.getByRole("button", { name: "完了を表示" }));
  act(() =>
    FakeEventSource.latest.send({
      type: "workspace_changed",
      workspace: "alpha",
    }),
  );
  await waitFor(() => expect(requests.length).toBeGreaterThan(5));
  expect(requests.every((r) => r.method === "GET")).toBe(true);
  expect(
    FakeEventSource.instances.every((e) => e.url === "/api/v1/events"),
  ).toBe(true);
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

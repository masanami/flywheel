import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { App } from "./App.tsx";
import { screens } from "./screens.tsx";
import { FakeEventSource, installFakes } from "./testing/fakes.ts";

afterEach(() => vi.unstubAllGlobals());

// 画面の一覧（screens）を順に表示して検査する。後続のチケットが画面を足しても対象に入る。
async function eachScreen(check: (container: HTMLElement, id: string) => void) {
  const { container } = render(<App />);
  act(() => FakeEventSource.latest.open());
  await waitFor(() => expect(screen.getByText(/alpha/)).toBeInTheDocument());
  for (const s of screens) {
    fireEvent.click(screen.getByRole("button", { name: s.title }));
    // 切り替わって実際にその画面が描画されたことを確かめる（検査が先頭画面だけに留まらない担保）
    expect(screen.getByRole("button", { name: s.title })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(screen.getByRole("region", { name: s.title })).toBeInTheDocument();
    check(container, s.id);
  }
}

it("AC-123: 全画面を操作しても fetch は GET だけ、EventSource は events の購読だけ", async () => {
  const { requests } = installFakes();
  await eachScreen(() => {});
  act(() =>
    FakeEventSource.latest.send({
      type: "workspace_changed",
      workspace: "alpha",
    }),
  );
  await waitFor(() => expect(requests.length).toBeGreaterThan(7));
  expect(requests.every((r) => r.method === "GET")).toBe(true);
  expect(
    FakeEventSource.instances.every((e) => e.url === "/api/v1/events"),
  ).toBe(true);
});

it("AC-124: 描画した全画面の DOM に form 要素が無い", async () => {
  installFakes();
  const seen: string[] = [];
  await eachScreen((container, id) => {
    seen.push(id);
    expect(container.querySelector("form"), id).toBeNull();
  });
  expect(seen).toEqual(screens.map((s) => s.id));
});

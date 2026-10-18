import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { CycleStatus } from "../api/types.gen.ts";
import { installFakes } from "../testing/fakes.ts";
import { CycleHeader } from "./CycleHeader.tsx";

afterEach(() => {
  vi.unstubAllGlobals();
  document.head.innerHTML = "";
});

const base: CycleStatus = {
  last_ended_at: null,
  last_result: null,
  last_started_at: null,
  state: "idle",
};

it("AC-199: running は「実行中」、idle は「停止中」、stale は「更新なし」", () => {
  const { rerender } = render(
    <CycleHeader cycle={{ ...base, state: "running" }} />,
  );
  expect(screen.getByTestId("cycle-header")).toHaveTextContent("実行中");
  rerender(<CycleHeader cycle={base} />);
  expect(screen.getByTestId("cycle-header")).toHaveTextContent("停止中");
  rerender(<CycleHeader cycle={{ ...base, state: "stale" }} />);
  expect(screen.getByTestId("cycle-header")).toHaveTextContent("更新なし");
  expect(screen.getByTestId("cycle-header")).not.toHaveTextContent("応答なし");
});

it("AC-200: last_ended_at と last_result を出し、null なら周がまだ無いことを示す", () => {
  const { rerender } = render(
    <CycleHeader
      cycle={{
        ...base,
        last_ended_at: "2026-10-17T01:00:00Z",
        last_result: "aborted",
      }}
    />,
  );
  expect(screen.getByTestId("cycle-header")).toHaveTextContent(
    "2026-10-17T01:00:00Z 中断（異常終了）",
  );
  rerender(<CycleHeader cycle={base} />);
  expect(screen.getByTestId("cycle-header")).toHaveTextContent(
    "周はまだありません",
  );
});

it("AC-219: 起動のボタンはそのワークスペースの POST …/cycle を CSRF つきで呼ぶ", async () => {
  const { requests } = installFakes({
    "POST /api/v1/workspaces/alpha/cycle": { started: true },
  });
  document.head.innerHTML = '<meta name="flywheel-csrf" content="tok-1">';
  render(<CycleHeader cycle={base} workspace="alpha" />);
  fireEvent.click(screen.getByRole("button", { name: "1 周を起動" }));
  await waitFor(() => expect(requests).toHaveLength(1));
  expect(requests[0]?.method).toBe("POST");
  expect(requests[0]?.url).toBe("/api/v1/workspaces/alpha/cycle");
  expect(requests[0]?.headers["X-Flywheel-CSRF"]).toBe("tok-1");
  document.head.innerHTML = "";
});

const errBody = (code: string, message: string, status: number) => () =>
  new Response(JSON.stringify({ error: { code, message } }), { status });

it("AC-220: locked ならサイクルが既に動いていて起動しなかったことを出す", async () => {
  installFakes({
    "POST /api/v1/workspaces/alpha/cycle": errBody("locked", "locked", 409),
  });
  render(<CycleHeader cycle={base} workspace="alpha" />);
  fireEvent.click(screen.getByRole("button", { name: "1 周を起動" }));
  const n = await screen.findByTestId("cycle-notice");
  expect(n).toHaveTextContent("既に動いている");
  expect(n).toHaveAttribute("data-kind", "locked");
});

it("AC-220a: locked 以外のエラーは始められなかったことと message を列に出す", async () => {
  installFakes({
    "POST /api/v1/workspaces/alpha/cycle": errBody(
      "internal_error",
      "claude: not found (stderr 末尾)",
      500,
    ),
  });
  render(<CycleHeader cycle={base} workspace="alpha" />);
  fireEvent.click(screen.getByRole("button", { name: "1 周を起動" }));
  const n = await screen.findByTestId("cycle-notice");
  expect(n).toHaveTextContent("1 周を始められませんでした");
  expect(n).toHaveTextContent("claude: not found (stderr 末尾)");
  expect(n).toHaveAttribute("data-kind", "failed");
});

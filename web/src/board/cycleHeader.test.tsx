import { render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import type { CycleStatus } from "../api/types.gen.ts";
import { CycleHeader } from "./CycleHeader.tsx";

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

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import type { Stages } from "../api/types.gen.ts";
import { StagesDetail } from "./StagesDetail.tsx";

afterEach(cleanup);

const EMPTY: Stages = {
  actual: null,
  expected: null,
  figure: null,
  issues: null,
  issues_answer: null,
  non_goals: null,
};

it("段階の欄がすべて null なら何も出さない（AC-380）", () => {
  const { container } = render(<StagesDetail stages={EMPTY} />);
  expect(container.innerHTML).toBe("");
});

it("論点・決定・予想と実物・やらないことを出し、ボタンも入力欄も置かない（AC-375〜379）", () => {
  const { container } = render(
    <StagesDetail
      stages={{
        ...EMPTY,
        issues: [
          {
            question: "どちらにする？",
            recommended: 2,
            options: [
              { label: "案A", example: null },
              { label: "案B", example: "例B" },
            ],
          },
        ],
        issues_answer: "案Bで決定",
        expected: [
          { kind: "k", scene: "画面1", before: "前", after: "予想後" },
        ],
        actual: [{ scene: "画面1", after: "実物後" }],
        non_goals: ["やらない1"],
      }}
    />,
  );
  expect(screen.getByText("どちらにする？")).toBeTruthy();
  expect(screen.getByText("案A")).toBeTruthy();
  const rec = container.querySelectorAll('[data-recommended="true"]');
  expect(rec.length).toBe(1);
  expect(rec[0]?.textContent).toContain("案B");
  expect(rec[0]?.textContent).toContain("（推奨）");
  expect(screen.getByText("案Bで決定")).toBeTruthy();
  const pair = container.querySelector('[data-pair="0"]');
  expect(pair?.textContent).toContain("予想後");
  expect(pair?.textContent).toContain("実物後");
  expect(screen.getByText("やらない1")).toBeTruthy();
  expect(
    container.querySelector(
      '[data-stage="issues"] input, [data-stage="issues"] textarea, [data-stage="issues"] select',
    ),
  ).toBeNull();
  expect(
    [...container.querySelectorAll('[data-stage="issues"] button')].filter(
      (b) => b.textContent !== "全文を開く",
    ),
  ).toEqual([]);
});

it("外部由来の文字列を HTML として解釈しない（M4P10）", () => {
  const { container } = render(
    <StagesDetail
      stages={{ ...EMPTY, non_goals: ["<img src=x onerror=alert(1)>"] }}
    />,
  );
  expect(container.querySelector("img")).toBeNull();
  expect(container.textContent).toContain("<img");
});

it("予想と実物を同じ位置で対にし、個数がずれても落ちない", () => {
  const { container } = render(
    <StagesDetail
      stages={{
        ...EMPTY,
        expected: [
          { kind: "k", scene: "s1", before: "b", after: "予想1" },
          { kind: "k", scene: "s2", before: "b", after: "予想2" },
        ],
        actual: [{ scene: "s1", after: "実物1" }],
      }}
    />,
  );
  const p0 = container.querySelector('[data-pair="0"]');
  const p1 = container.querySelector('[data-pair="1"]');
  expect(p0?.textContent).toContain("予想1");
  expect(p0?.textContent).toContain("実物1");
  expect(p1?.textContent).toContain("予想2");
  expect(p1?.textContent).not.toContain("実物");
});

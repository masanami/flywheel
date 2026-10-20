import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { FigureView } from "./FigureView.tsx";
import { type MermaidLike, setMermaidLoader } from "./loadMermaid.ts";

afterEach(() => {
  cleanup();
  setMermaidLoader();
});

function fake(render: MermaidLike["render"]) {
  const initialize = vi.fn();
  const mermaid: MermaidLike = { initialize, render };
  const loader = vi.fn(async () => mermaid);
  setMermaidLoader(loader);
  return { loader, initialize };
}

it("図が無ければ読み込みの関数を呼ばず何も出さない（AC-381・AC-382）", () => {
  const { loader } = fake(async () => ({ svg: "" }));
  const { container } = render(<FigureView figure={null} />);
  expect(container.innerHTML).toBe("");
  expect(loader).not.toHaveBeenCalled();
});

it("図があれば読み込んで描く。strict で初期化する（AC-383・AC-384）", async () => {
  const renderFn = vi.fn(async (_id: string, text: string) => ({
    svg: `<svg data-src="${text}"></svg>`,
  }));
  const { loader, initialize } = fake(renderFn);
  render(<FigureView figure="graph TD; A-->B" />);
  await waitFor(() => expect(screen.getByTestId("figure-svg")).toBeVisible());
  expect(loader).toHaveBeenCalledTimes(1);
  expect(renderFn).toHaveBeenCalledWith(expect.any(String), "graph TD; A-->B");
  expect(
    screen
      .getByTestId("figure-svg")
      .querySelector("svg")
      ?.getAttribute("data-src"),
  ).toBe("graph TD; A-->B");
  expect(initialize).toHaveBeenCalledWith(
    expect.objectContaining({ securityLevel: "strict" }),
  );
});

it("描画に失敗したら図のテキストをプレーンテキストで出す（AC-385）", async () => {
  fake(async () => {
    throw new Error("parse error");
  });
  const text = "<img src=x onerror=alert(1)> broken";
  const { container } = render(<FigureView figure={text} />);
  await waitFor(() => expect(screen.getByTestId("figure-text")).toBeVisible());
  expect(screen.getByTestId("figure-text").textContent).toBe(text);
  expect(container.querySelector("img")).toBeNull();
});

it("読み込み自体が失敗してもテキストで出す", async () => {
  setMermaidLoader(async () => {
    throw new Error("chunk");
  });
  render(<FigureView figure="x" />);
  await waitFor(() => expect(screen.getByTestId("figure-text")).toBeVisible());
});

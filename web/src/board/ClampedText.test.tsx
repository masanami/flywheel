import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { ClampedText } from "./ClampedText.tsx";

// jsdom は要素の高さを持たない（常に 0）ので、畳んだ要素の scrollHeight / clientHeight を差し替えて
// 「実際に切れているか」を作る。
function sizes(scrollHeight: number, clientHeight: number) {
  Object.defineProperty(HTMLElement.prototype, "scrollHeight", {
    configurable: true,
    get: () => scrollHeight,
  });
  Object.defineProperty(HTMLElement.prototype, "clientHeight", {
    configurable: true,
    get: () => clientHeight,
  });
}
afterEach(() => {
  Reflect.deleteProperty(HTMLElement.prototype, "scrollHeight");
  Reflect.deleteProperty(HTMLElement.prototype, "clientHeight");
  vi.unstubAllGlobals();
});

it("M4P10: 外部由来の文字列は HTML として解釈されず、切れている値は操作で全文を開ける", () => {
  sizes(120, 60);
  const long = `<img src=x onerror=alert(1)> ${"あ".repeat(300)}`;
  const { container } = render(<ClampedText text={long} />);
  expect(container.querySelector("img")).toBeNull();
  expect(container.querySelector('[data-clamped="true"]')).not.toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "全文を開く" }));
  expect(container.querySelector('[data-clamped="true"]')).toBeNull();
  expect(container.textContent).toContain("<img src=x");
});

it("実際に切れていないときは「全文を開く」を出さない（長い文字列でも、改行を含んでも）", () => {
  sizes(60, 60);
  render(<ClampedText text={`${"あ".repeat(100)}\n二行目`} />);
  expect(screen.queryByRole("button", { name: "全文を開く" })).toBeNull();
});

it("開いたあとに「折りたたむ」を押すと畳んで測り直し、まだ切れていれば「全文を開く」に戻る", () => {
  sizes(120, 60);
  const { container } = render(<ClampedText text="長い文章" />);
  fireEvent.click(screen.getByRole("button", { name: "全文を開く" }));
  fireEvent.click(screen.getByRole("button", { name: "折りたたむ" }));
  expect(container.querySelector('[data-clamped="true"]')).not.toBeNull();
  expect(
    screen.getByRole("button", { name: "全文を開く" }),
  ).toBeInTheDocument();
});

it("開いたあと、大きさが変わって切れなくなっても「折りたたむ」は消えない", () => {
  sizes(120, 60);
  render(<ClampedText text="長い文章" />);
  fireEvent.click(screen.getByRole("button", { name: "全文を開く" }));
  sizes(60, 60);
  expect(
    screen.getByRole("button", { name: "折りたたむ" }),
  ).toBeInTheDocument();
});

it("大きさが変わると測り直す（ResizeObserver）: 広がって収まれば消え、狭まって切れれば出る", () => {
  let fire: () => void = () => {};
  vi.stubGlobal(
    "ResizeObserver",
    class {
      constructor(cb: () => void) {
        fire = cb;
      }
      observe() {}
      disconnect() {}
    },
  );
  sizes(60, 60);
  render(<ClampedText text="あいう" />);
  expect(screen.queryByRole("button", { name: "全文を開く" })).toBeNull();
  sizes(120, 60);
  act(() => fire());
  expect(
    screen.getByRole("button", { name: "全文を開く" }),
  ).toBeInTheDocument();
  sizes(60, 60);
  act(() => fire());
  expect(screen.queryByRole("button", { name: "全文を開く" })).toBeNull();
});

it("text が変わると測り直す", () => {
  sizes(60, 60);
  const { rerender } = render(<ClampedText text="短い" />);
  expect(screen.queryByRole("button", { name: "全文を開く" })).toBeNull();
  sizes(120, 60);
  rerender(<ClampedText text="長くなった文章" />);
  expect(
    screen.getByRole("button", { name: "全文を開く" }),
  ).toBeInTheDocument();
});

it("切り替えは下線を引いた文字のリンクの見た目の button で、背景も枠も付けない", () => {
  sizes(120, 60);
  render(<ClampedText text="長い文章" />);
  const b = screen.getByRole("button", { name: "全文を開く" });
  expect(b).toHaveAttribute("type", "button");
  expect(b).toHaveClass("clamped-text-toggle");
});

it("ResizeObserver が無い環境でも動く", () => {
  vi.stubGlobal("ResizeObserver", undefined);
  sizes(120, 60);
  render(<ClampedText text="長い文章" />);
  expect(
    screen.getByRole("button", { name: "全文を開く" }),
  ).toBeInTheDocument();
});

// 色の値そのものは見ない。本文より控えめなグレーのトークン（ライト・ダークで値が切り替わる）で指定していることだけ確かめる。
it("「全文を開く／折りたたむ」の文字色は、リンクの色ではなく控えめなグレーのトークンで指定する（下線は残す）", () => {
  const css = readFileSync(resolve(process.cwd(), "src/styles.css"), "utf8");
  const rule = /\.clamped-text-toggle\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
  expect(rule).toMatch(/color:\s*var\(--board-muted-fg\)/);
  expect(rule).not.toMatch(/color:\s*var\(--board-link\)/);
  expect(rule).toMatch(/text-decoration:\s*underline/);
});

import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { ClampedText } from "./ClampedText.tsx";

it("M4P10: 外部由来の文字列は HTML として解釈されず、長い値は切り詰めて操作で全文を開ける", () => {
  const long = `<img src=x onerror=alert(1)> ${"あ".repeat(300)}`;
  const { container } = render(<ClampedText text={long} />);
  expect(container.querySelector("img")).toBeNull();
  expect(container.querySelector('[data-clamped="true"]')).not.toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "全文を開く" }));
  expect(container.querySelector('[data-clamped="true"]')).toBeNull();
  expect(container.textContent).toContain("<img src=x");
});

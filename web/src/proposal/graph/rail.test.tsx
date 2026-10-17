// 2026-10-17 のオーナーの指示（flywheel#176）: エージェントの列は Slack の見た目に寄せず、サイトの他の部分に合わせる。
// アイコンの下にエージェント名を出す（10/14 には本実装へ回していたものを試作に入れた）。
import { render, screen, within } from "@testing-library/react";
import { expect, it } from "vitest";
import { AgentRail, type AgentSummary } from "./AgentLayout.tsx";

const agent = (name: string, p: Partial<AgentSummary> = {}): AgentSummary => ({
  name,
  readable: true,
  state: "ok",
  human: 0,
  active: 0,
  ...p,
});
const LONG = "payments-reconciliation-nightly-agent";

function renderRail(ws: string) {
  render(
    <AgentRail
      agents={[
        agent("alpha", { human: 2 }),
        agent(LONG),
        agent("gamma", { readable: false, state: "store_not_found" }),
      ]}
      icons={new Map()}
      ws={ws}
      onChoose={() => {}}
    />,
  );
  return screen.getByRole("navigation", { name: "エージェント" });
}

it("名前: アイコンの下にエージェント名を出す。読めないエージェントにも出す", () => {
  const rail = renderRail("alpha");
  const names = [...rail.querySelectorAll(".agent-rail-name")].map(
    (n) => n.textContent,
  );
  expect(names).toEqual(["alpha", LONG, "gamma"]);
  // 名前はアイコンの後ろ（下）に置く
  const alpha = within(rail).getByRole("button", { name: /^alpha / });
  const icon = alpha.querySelector(".agent-rail-icon");
  const name = alpha.querySelector(".agent-rail-name");
  expect(icon?.nextElementSibling).toBe(name);
  // 件数の札と実施中の印はアイコンの角に付け、名前には付けない
  expect(icon?.querySelector(".agent-rail-badge")?.textContent).toBe("2");
});

it("名前: 長い名前も DOM には全文を持ち（見た目は CSS で 2 行に切る）、読み上げの名前と吹き出しにも全文を出す", () => {
  const rail = renderRail(LONG);
  const b = within(rail).getByRole("button", { name: new RegExp(`^${LONG} `) });
  expect(b.querySelector(".agent-rail-name")?.textContent).toBe(LONG);
  expect(b.querySelector(".agent-rail-tip")?.textContent).toContain(LONG);
  expect(b).toHaveAttribute("aria-current", "true");
});

it("配色: 頭文字のアイコンに Slack の色を使わない", () => {
  const rail = renderRail("alpha");
  const slack = ["#4a154b", "#1264a3", "#2bac76", "#e01e5a", "#ecb22e"];
  for (const icon of rail.querySelectorAll<HTMLElement>(
    ".agent-icon-initial",
  )) {
    const bg = icon.style.background;
    expect(bg).not.toBe("");
    for (const hex of slack) {
      const [r, g, b2] = [1, 3, 5].map((i) =>
        Number.parseInt(hex.slice(i, i + 2), 16),
      );
      expect(bg).not.toContain(`rgb(${r}, ${g}, ${b2})`);
      expect(bg.toLowerCase()).not.toContain(hex);
    }
  }
});

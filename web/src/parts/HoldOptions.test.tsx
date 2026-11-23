import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { type HoldAnswerState, HoldOptions } from "./HoldOptions.tsx";

afterEach(cleanup);

const state: HoldAnswerState = {
  busy: false,
  notice: null,
  error: null,
  answer: async () => {},
};

it("説明が見出しと同じ文の選択肢は、同じ語を 2 回並べない（説明が違えば出す）", () => {
  render(
    <HoldOptions
      hold={{
        hold_id: "H-1",
        check: "c1",
        options: [
          {
            id: "o1",
            label: "やり直す",
            description: "やり直す",
            button: true,
          },
          {
            id: "o2",
            label: "中止する",
            description: "作業を止める",
            button: true,
          },
        ],
      }}
      state={state}
    />,
  );
  const items = screen.getAllByRole("listitem");
  // 見出し（b）とボタンの 2 か所だけ。説明の分は出ない。
  expect(items[0]?.textContent?.split("やり直す").length).toBe(3);
  expect(items[1]).toHaveTextContent("作業を止める");
});

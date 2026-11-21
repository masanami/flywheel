import { expect, it } from "vitest";

// AC-106 #927: 画面の文と案内の文に、「端末」の語は無い（「ターミナル」と書く）。
// 画面のソース（試験を除く）の全部の文字列を走査し、語の表・案内（guidance.json）・各画面の文のどこにも残さない。
const sources = import.meta.glob(
  [
    "./**/*.ts",
    "./**/*.tsx",
    "./**/*.json",
    "!./**/*.test.ts",
    "!./**/*.test.tsx",
  ],
  { query: "?raw", import: "default", eager: true },
) as Record<string, string>;

it("AC-106 #927: 画面のソース（試験を除く）に「端末」の語は無い", () => {
  expect(Object.keys(sources).length).toBeGreaterThan(20);
  const hits = Object.entries(sources)
    .filter(([, text]) => text.includes("端末"))
    .map(([path]) => path);
  expect(hits).toEqual([]);
});

import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { render } from "@testing-library/react";
import { expect, it } from "vitest";
import { ClampedText } from "../board/ClampedText.tsx";
import { Markdown } from "./Markdown.tsx";

// 各テストの名前に、対応する受入基準（AC-<番号>）と Issue #804 を書く。
function md(text: string) {
  return render(<Markdown text={text} />).container;
}

it("AC-388 #804: ## 見出し は見出しの要素になる", () => {
  const c = md("## 見出し");
  expect(c.querySelector("h4")?.textContent).toBe("見出し");
});

it("AC-389 #804: # 見出し は h1 にならない", () => {
  const c = md("# 見出し");
  expect(c.querySelector("h1")).toBeNull();
  expect(c.querySelector("h3")?.textContent).toBe("見出し");
});

it("AC-390 #804: **太字** は強調の要素になる", () => {
  expect(md("**太字**").querySelector("strong")?.textContent).toBe("太字");
  expect(md("*斜体*").querySelector("em")?.textContent).toBe("斜体");
});

it("AC-391 #804: - で始まる行は箇条書きの要素になる", () => {
  const c = md("- a\n- b");
  expect(c.querySelectorAll("ul > li")).toHaveLength(2);
});

it("AC-392 #804: 字下げした - の行は入れ子の箇条書きになる", () => {
  const c = md("- a\n  - b");
  expect(c.querySelector("ul > li > ul > li")?.textContent).toBe("b");
});

it("AC-393 #804: 1. で始まる行は番号つきの箇条書きになる", () => {
  const c = md("1. a\n2. b");
  expect(c.querySelectorAll("ol > li")).toHaveLength(2);
});

it("AC-394 #804: バッククォートで囲んだ部分はコードの要素になる", () => {
  const c = md("a `x` b");
  expect(c.querySelector("p > code")?.textContent).toBe("x");
});

it("AC-395 #804: バッククォート 3 つで囲んだ行はコードのブロックになる", () => {
  const c = md("```\nline1\nline2\n```");
  expect(c.querySelector("pre > code")?.textContent).toBe("line1\nline2");
});

it("AC-396 #804: | の表は見出しの行と中身の行を持つ表になる", () => {
  const c = md("| a | b |\n|---|---|\n| 1 | 2 |");
  expect(c.querySelectorAll("table thead th")).toHaveLength(2);
  expect(c.querySelectorAll("table tbody tr td")).toHaveLength(2);
  expect(c.querySelector("tbody td")?.textContent).toBe("1");
});

it("AC-397 #804: > で始まる行は引用の要素になる", () => {
  expect(md("> 引用").querySelector("blockquote")?.textContent).toContain(
    "引用",
  );
});

it("AC-398 #804: <script>alert(1)</script> から script の要素は作られない", () => {
  expect(md("<script>alert(1)</script>").querySelector("script")).toBeNull();
  expect(
    md("文中の <script>alert(1)</script> です").querySelector("script"),
  ).toBeNull();
});

it("AC-399 #804: <script>alert(1)</script> は書いてある文字のまま出る", () => {
  expect(md("<script>alert(1)</script>").textContent).toContain(
    "<script>alert(1)</script>",
  );
  expect(md("文中の <script>alert(1)</script> です").textContent).toContain(
    "<script>alert(1)</script>",
  );
});

it("AC-400 #804: <img src=x onerror=alert(1)> から img の要素は作られない", () => {
  const c = md("<img src=x onerror=alert(1)>");
  expect(c.querySelector("img")).toBeNull();
  expect(c.textContent).toContain("<img src=x onerror=alert(1)>");
  expect(
    md("a <img src=x onerror=alert(1)> b").querySelector("img"),
  ).toBeNull();
});

it("AC-401 #804: a & b < c は a & b < c と出る（&amp;・&lt; と出ない）", () => {
  const c = md("a & b < c");
  expect(c.textContent).toBe("a & b < c");
  expect(c.textContent).not.toContain("&amp;");
  expect(c.textContent).not.toContain("&lt;");
});

it("AC-402 #804: ~~文字~~ は書いてある文字のまま出る", () => {
  const c = md("~~文字~~");
  expect(c.textContent).toBe("~~文字~~");
  expect(c.querySelector("del, s")).toBeNull();
});

it("AC-403 #804: <https://example.com/> は行き先が https://example.com/ のリンクになる", () => {
  const a = md("<https://example.com/>").querySelector("a");
  expect(a?.getAttribute("href")).toBe("https://example.com/");
});

it("AC-404 #804: [文字](https://example.com/) は行き先が https://example.com/ のリンクになる（行き先を別の行に書く形も同じ）", () => {
  const a = md("[文字](https://example.com/)").querySelector("a");
  expect(a?.getAttribute("href")).toBe("https://example.com/");
  expect(a?.textContent).toBe("文字");
  const b = md("[文字][1]\n\n[1]: https://example.com/").querySelector("a");
  expect(b?.getAttribute("href")).toBe("https://example.com/");
});

it("AC-405 #804: 本文から作ったリンクは target=_blank と rel=noopener noreferrer を持つ", () => {
  const as = md("[a](https://e.com/) <http://e.org/>").querySelectorAll("a");
  expect(as).toHaveLength(2);
  for (const a of as) {
    expect(a).toHaveAttribute("target", "_blank");
    expect(a).toHaveAttribute("rel", "noopener noreferrer");
  }
});

it("AC-406 #804: [文字](javascript:alert(1)) からリンクの要素は作られない", () => {
  const c = md("[文字](javascript:alert(1))");
  expect(c.querySelector("a")).toBeNull();
  expect(c.textContent).toContain("文字");
});

it("AC-407 #804: ![代わりの文字](https://example.com/x.png) から img の要素は作られない", () => {
  const c = md("![代わりの文字](https://example.com/x.png)");
  expect(c.querySelector("img")).toBeNull();
  // 行き先が http・https なら、代わりの文字をリンクにする
  expect(c.querySelector("a")?.getAttribute("href")).toBe(
    "https://example.com/x.png",
  );
});

it("AC-408 #804: ![代わりの文字](https://example.com/x.png) の代わりの文字が出る", () => {
  expect(
    md("![代わりの文字](https://example.com/x.png)").textContent,
  ).toContain("代わりの文字");
  // 行き先が http・https でない画像は、文字だけでリンクにしない
  const c = md("![代わり](javascript:alert(1))");
  expect(c.textContent).toContain("代わり");
  expect(c.querySelector("a")).toBeNull();
});

it("AC-409 #804: 部品のソースは dangerouslySetInnerHTML・innerHTML を使っていない", () => {
  const src = readFileSync(
    resolve(process.cwd(), "src/markdown/Markdown.tsx"),
    "utf8",
  );
  // コメントの言及は除いて、コードとして書かれていないことを確かめる
  const code = src
    .split("\n")
    .filter((l) => !l.trim().startsWith("//"))
    .join("\n");
  expect(code).not.toMatch(/dangerouslySetInnerHTML/);
  expect(code).not.toMatch(/innerHTML/);
});

it("AC-414 #804: web/package.json の dependencies に marked がある", () => {
  const pkg = JSON.parse(
    readFileSync(resolve(process.cwd(), "package.json"), "utf8"),
  );
  expect(pkg.dependencies.marked).toMatch(/^\d+\.\d+\.\d+$/);
  expect(pkg.dependencies).not.toHaveProperty("dompurify");
  expect(pkg.dependencies).not.toHaveProperty("react-markdown");
});

it("AC-415 #804: ClampedText は、整えた本文（要素の並び）も包める。文字だけを渡す動きは ClampedText.test.tsx が守る", () => {
  const { container } = render(
    <ClampedText resetKey="k">
      <Markdown text={"## 見出し\n\n- a"} />
    </ClampedText>,
  );
  expect(container.querySelector('[data-clamped="true"] h4')).not.toBeNull();
  expect(container.querySelector("ul > li")?.textContent).toBe("a");
});

import { type Token, type Tokens, lexer } from "marked";
import { Fragment, type ReactNode } from "react";

// 本文を Markdown として整える部品。marked は字句解析（lexer）だけを使い、その並びから
// React の要素を組み立てる。HTML の文字列は作らない（要素に文字列を差し込む口は使わない）。
// - 生の HTML と、部品が組み立て方を持たない書き方（打ち消し線・脚注など）は、書いてある文字のまま出す。
// - リンクは行き先が http:・https: のものだけ。画像は読み込まず、代わりの文字を出す。

// 本文の見出し（# = 1）を、画面の見出しより下の段にそろえる。# は h3、###### 以降は h6。
const HEADING_OFFSET = 2;

function safeHref(href: string | null | undefined): string | null {
  if (!href) return null;
  try {
    const u = new URL(href);
    return u.protocol === "http:" || u.protocol === "https:" ? href : null;
  } catch {
    return null;
  }
}

// 子要素の key は、並びの位置で足りる（本文は不変の入力から毎回組み立て直す）
function k(i: number, prefix: string): string {
  return `${prefix}${i}`;
}

function inline(
  tokens: Token[] | undefined,
  inLink: boolean,
  prefix = "i",
): ReactNode[] {
  return (tokens ?? []).map((t, i) => (
    <Fragment key={k(i, prefix)}>{inlineOne(t, inLink)}</Fragment>
  ));
}

function inlineOne(t: Token, inLink: boolean): ReactNode {
  switch (t.type) {
    case "text": {
      const x = t as Tokens.Text;
      return x.tokens ? inline(x.tokens, inLink) : x.text;
    }
    case "escape":
      return (t as Tokens.Escape).text;
    case "strong":
      return <strong>{inline((t as Tokens.Strong).tokens, inLink)}</strong>;
    case "em":
      return <em>{inline((t as Tokens.Em).tokens, inLink)}</em>;
    case "codespan":
      return <code>{(t as Tokens.Codespan).text}</code>;
    case "br":
      return <br />;
    case "link": {
      const l = t as Tokens.Link;
      const href = safeHref(l.href);
      // 行き先が許されないリンク・リンクの中のリンクは、書いてある文字のまま出す
      if (href === null || inLink) return l.raw;
      return (
        <a href={href} target="_blank" rel="noopener noreferrer">
          {inline(l.tokens, true)}
        </a>
      );
    }
    case "image": {
      const im = t as Tokens.Image;
      const alt = im.text || im.raw;
      const href = safeHref(im.href);
      if (href === null || inLink) return alt;
      return (
        <a href={href} target="_blank" rel="noopener noreferrer">
          {alt}
        </a>
      );
    }
    default:
      // html・del など、組み立て方を持たないものは書いてある文字のまま
      return t.raw;
  }
}

// チェックつきの箇条書きの印（[ ]・[x]・[X]）は、書いてある文字のまま出す（marked は印を本文から外すので、
// 項目の元の文字から取り出す）
const TASK_MARK = /^\s*(?:[-*+]|\d+[.)])\s+(\[[ xX]\])/;

function listItem(item: Tokens.ListItem, i: number): ReactNode {
  const mark = item.task
    ? `${TASK_MARK.exec(item.raw)?.[1] ?? (item.checked ? "[x]" : "[ ]")} `
    : "";
  return (
    <li key={k(i, "li")}>
      {mark}
      {blocks(item.tokens, "b")}
    </li>
  );
}

function blocks(tokens: Token[] | undefined, prefix = "b"): ReactNode[] {
  return (tokens ?? []).map((t, i) => (
    <Fragment key={k(i, prefix)}>{blockOne(t)}</Fragment>
  ));
}

function blockOne(t: Token): ReactNode {
  switch (t.type) {
    case "space":
    case "def":
      return null;
    case "heading": {
      const h = t as Tokens.Heading;
      const level = Math.min(h.depth + HEADING_OFFSET, 6);
      const Tag = `h${level}` as "h3";
      return <Tag>{inline(h.tokens, false)}</Tag>;
    }
    case "paragraph":
      return <p>{inline((t as Tokens.Paragraph).tokens, false)}</p>;
    case "text": {
      // 箇条書きの項目の中の文（段落にしない）
      const x = t as Tokens.Text;
      return x.tokens ? inline(x.tokens, false) : x.text;
    }
    case "code":
      return (
        <pre>
          <code>{(t as Tokens.Code).text}</code>
        </pre>
      );
    case "blockquote":
      return (
        <blockquote>{blocks((t as Tokens.Blockquote).tokens, "q")}</blockquote>
      );
    case "hr":
      return <hr />;
    case "list": {
      const l = t as Tokens.List;
      const items = l.items.map(listItem);
      return l.ordered ? (
        <ol start={typeof l.start === "number" ? l.start : undefined}>
          {items}
        </ol>
      ) : (
        <ul>{items}</ul>
      );
    }
    case "table": {
      const tb = t as Tokens.Table;
      const align = (a: "left" | "right" | "center" | null) =>
        a ? { textAlign: a } : undefined;
      return (
        <table>
          <thead>
            <tr>
              {tb.header.map((c, i) => (
                <th key={k(i, "th")} style={align(c.align)}>
                  {inline(c.tokens, false)}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {tb.rows.map((row, r) => (
              <tr key={k(r, "tr")}>
                {row.map((c, i) => (
                  <td key={k(i, "td")} style={align(c.align)}>
                    {inline(c.tokens, false)}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      );
    }
    default:
      // 生の HTML など、組み立て方を持たないブロックは書いてある文字のまま
      return <p className="md-raw">{t.raw.replace(/\n+$/, "")}</p>;
  }
}

// 段落の中の 1 つの改行も、改行として出す（breaks）。本文は、Issue の説明や判断点が書いた文で、
// 改行がそのまま見える前提で書かれている（見直し前の画面も、改行をそのまま出していた）。
// marked は、渡した設定を既定の値と混ぜないので、gfm（表・自動のリンク）も一緒に渡す。
const LEXER_OPTIONS = { gfm: true, breaks: true };

export function Markdown({ text }: { text: string }) {
  return <div className="md">{blocks(lexer(text, LEXER_OPTIONS))}</div>;
}

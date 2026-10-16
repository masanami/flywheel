import { useState } from "react";

const CLAMP_LINES = 3;
const CLAMP_CHARS = 60;

// 外部由来の文字列はプレーンテキストのまま出す（HTML・Markdown として解釈しない）。
// 長い値は既定で数行に切り詰め、ボタンで全文を開く。
export function ClampedText({ text }: { text: string }) {
  const [open, setOpen] = useState(false);
  const long = text.length > CLAMP_CHARS || text.includes("\n");
  return (
    <span>
      <span
        data-clamped={!open ? "true" : "false"}
        style={
          !open
            ? {
                display: "-webkit-box",
                WebkitLineClamp: CLAMP_LINES,
                WebkitBoxOrient: "vertical",
                overflow: "hidden",
                whiteSpace: "pre-wrap",
              }
            : { whiteSpace: "pre-wrap" }
        }
      >
        {text}
      </span>
      {long ? (
        <button type="button" onClick={() => setOpen(!open)}>
          {open ? "折りたたむ" : "全文を開く"}
        </button>
      ) : null}
    </span>
  );
}

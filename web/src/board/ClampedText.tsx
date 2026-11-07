import { useLayoutEffect, useRef, useState } from "react";

const CLAMP_LINES = 3;

// 外部由来の文字列はプレーンテキストのまま出す（HTML・Markdown として解釈しない）。
// 長い値は既定で数行に切り詰め、実際に切れているときだけ、下線を引いた文字のリンクで全文を開ける。
// 切れているかは、畳んだ要素の scrollHeight > clientHeight で測る（文字数や改行では決めない）。
export function ClampedText({ text }: { text: string }) {
  const [open, setOpen] = useState(false);
  // 開く前に切れていたか。開いた状態では切れないので、開く前の測定を覚えておき、「折りたたむ」を出し続ける
  const [clipped, setClipped] = useState(false);
  const [seen, setSeen] = useState(text);
  const ref = useRef<HTMLSpanElement>(null);

  // 文字列が変わったら畳み直す（測り直せるように）
  if (seen !== text) {
    setSeen(text);
    setOpen(false);
  }

  // 描画のあと（開閉・文字列の変化）と、大きさが変わったときに測る。開いている間は測らない。
  // biome-ignore lint/correctness/useExhaustiveDependencies: text と open の変化が測り直しの合図
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = () => {
      if (el.dataset.clamped !== "true") return;
      setClipped(el.scrollHeight > el.clientHeight);
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [text, open]);

  return (
    <span>
      <span
        ref={ref}
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
      {clipped || open ? (
        <button
          type="button"
          className="clamped-text-toggle"
          onClick={() => setOpen(!open)}
        >
          {open ? "折りたたむ" : "全文を開く"}
        </button>
      ) : null}
    </span>
  );
}

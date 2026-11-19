import { useEffect, useState } from "react";
import { renderFigure } from "./loadMermaid.ts";

type State =
  | { status: "loading" }
  | { status: "svg"; svg: string }
  | { status: "text" };

// 図（Mermaid のテキスト）。null なら何も出さず、読み込みの関数も呼ばない。
// 描けなければ図のテキストをプレーンテキスト（React のテキストノード）で出す。
export function FigureView({ figure }: { figure: string | null }) {
  const [state, setState] = useState<State>({ status: "loading" });
  useEffect(() => {
    if (figure === null) return;
    let cancelled = false;
    setState({ status: "loading" });
    renderFigure(figure).then((r) => {
      if (cancelled) return;
      setState(r.ok ? { status: "svg", svg: r.svg } : { status: "text" });
    });
    return () => {
      cancelled = true;
    };
  }, [figure]);
  if (figure === null) return null;
  return (
    <section aria-label="図" data-stage="figure" className="card-detail-figure">
      <h3>図</h3>
      {state.status === "loading" && <p>図を読み込み中...</p>}
      {state.status === "svg" && (
        // svg は securityLevel: "strict" で描いた Mermaid の出力
        <div
          data-testid="figure-svg"
          // biome-ignore lint/security/noDangerouslySetInnerHtml: Mermaid の strict の出力（ラベルの HTML は解釈済みで無効化されている）
          dangerouslySetInnerHTML={{ __html: state.svg }}
        />
      )}
      {state.status === "text" && (
        <pre data-testid="figure-text" style={{ whiteSpace: "pre-wrap" }}>
          {figure}
        </pre>
      )}
    </section>
  );
}

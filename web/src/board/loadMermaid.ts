// Mermaid は別のチャンクに分け、図のある課題の詳細を開いたときだけ動的 import で読む。
// 読み込みの関数は差し替えられる（UI のテストが呼ばれたかどうか・受け取った設定を検べる）。

export type MermaidConfig = {
  startOnLoad: false;
  securityLevel: "strict";
  // 失敗時に Mermaid が body へ残すエラーの図を出さない（失敗はテキスト表示に任せる）
  suppressErrorRendering: true;
};

export interface MermaidLike {
  initialize(config: MermaidConfig): void;
  render(id: string, text: string): Promise<{ svg: string }>;
}

export type MermaidLoader = () => Promise<MermaidLike>;

const defaultLoader: MermaidLoader = async () =>
  (await import("mermaid")).default as unknown as MermaidLike;

let loader: MermaidLoader = defaultLoader;

// テスト用。引数なしで既定の読み込みに戻す。
export function setMermaidLoader(next?: MermaidLoader): void {
  loader = next ?? defaultLoader;
}

export const MERMAID_CONFIG: MermaidConfig = {
  startOnLoad: false,
  securityLevel: "strict",
  suppressErrorRendering: true,
};

export type RenderResult = { ok: true; svg: string } | { ok: false };

let counter = 0;

// 読み込み・初期化・描画のどれが失敗しても { ok: false }（呼び出し側が図のテキストを出す）。
export async function renderFigure(text: string): Promise<RenderResult> {
  try {
    const mermaid = await loader();
    mermaid.initialize(MERMAID_CONFIG);
    counter += 1;
    const { svg } = await mermaid.render(`figure-${counter}`, text);
    return { ok: true, svg };
  } catch {
    return { ok: false };
  }
}

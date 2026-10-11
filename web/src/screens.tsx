import type { ReactNode } from "react";
import type { LiveState } from "./live/useLive.ts";

export interface Screen {
  id: string;
  title: string;
  render: (live: LiveState) => ReactNode;
}

function Workspaces({ live }: { live: LiveState }) {
  return (
    <section aria-label="ワークスペース">
      <h2>ワークスペース</h2>
      <ul>
        {live.workspaces.map((w) => {
          const d = live.data[w.name];
          return (
            <li key={w.name}>
              <strong>{w.name}</strong> — {w.state}
              {w.error ? `（${w.error}）` : null}
              {d?.status ? (
                <span>
                  {" "}
                  対応可能 {d.status.actionable.challenges.length} 件 / 課題{" "}
                  {d.challenges?.challenges.length ?? 0} 件
                </span>
              ) : null}
              {d?.error ? <span> 取得に失敗: {d.error}</span> : null}
            </li>
          );
        })}
      </ul>
    </section>
  );
}

function NeedsHuman({ live }: { live: LiveState }) {
  return (
    <section aria-label="人間の対応待ち">
      <h2>人間の対応待ち</h2>
      <pre>{JSON.stringify(live.needsHuman, null, 2)}</pre>
      <h2>問い</h2>
      <pre>{JSON.stringify(live.questions, null, 2)}</pre>
    </section>
  );
}

// 画面の一覧。後続のチケットは画面をここへ足す。AC-123・AC-124 のテストはこの一覧を順に検査する。
export const screens: Screen[] = [
  {
    id: "workspaces",
    title: "ワークスペース",
    render: (live) => <Workspaces live={live} />,
  },
  {
    id: "needs-human",
    title: "人間の対応待ち",
    render: (live) => <NeedsHuman live={live} />,
  },
];

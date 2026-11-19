import { cycleStateText } from "../labels.ts";
import type { LiveState } from "../live/useLive.ts";
import { hrefOf } from "../shell/route.ts";
import {
  WAITING_LABELS,
  waitingCounts,
  waitingKindGroup,
  waitingTotal,
} from "../shell/waiting.ts";
import { waitingItems } from "./items.ts";

import { LOADING_TEXT } from "../shell/Shell.tsx";

export const NO_WAITING_TEXT = "あなたを待っているものはありません。";

// すべてのエージェント。横断で見るのは、あなたを待っているものだけ。区分と件数は API の値から数え、
// 画面の側に決まりを持たない（waitingCounts は core の区分の数え方の写し）。
export function AllScreen({ live }: { live: LiveState }) {
  const rows = live.workspaces.flatMap((w) => {
    const nh = live.needsHuman?.workspaces.find((x) => x.name === w.name);
    if (!nh) return [];
    const cards = live.data[w.name]?.challenges?.challenges ?? [];
    const titleOf = (id: string) => cards.find((c) => c.id === id)?.title;
    return waitingItems(
      w.name,
      nh.needs_human,
      titleOf,
      live.questions?.questions ?? [],
    ).map((item) => ({
      agent: w.name,
      item,
    }));
  });
  const totalOf = (name: string) => {
    const nh = live.needsHuman?.workspaces.find((x) => x.name === name);
    return nh ? waitingTotal(waitingCounts(nh.needs_human)) : 0;
  };
  const total = live.workspaces.reduce((a, w) => a + totalOf(w.name), 0);

  return (
    <section aria-labelledby="screen-title">
      <h2 id="screen-title">すべてのエージェント</h2>

      <section aria-labelledby="all-waiting-title" className="all-block">
        <h3 id="all-waiting-title">
          あなたを待っている（{live.needsHuman === null ? "—" : total} 件）
        </h3>
        {live.needsHuman === null ? (
          <p>{LOADING_TEXT}</p>
        ) : rows.length === 0 ? (
          <p>{NO_WAITING_TEXT}</p>
        ) : (
          <ul className="all-waiting" data-testid="all-waiting">
            {rows.map(({ agent, item }, i) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: 同じ種類・題名が並びうる。並びは API の順で安定
              <li key={i} data-testid="all-waiting-row">
                <b className="all-agent-name">{agent}</b>{" "}
                <span
                  className="action-pill"
                  data-kind-group={waitingKindGroup(item.kind)}
                >
                  {WAITING_LABELS[item.kind]}
                </span>{" "}
                {item.href !== null ? (
                  <a href={item.href}>{item.title}</a>
                ) : (
                  <span>{item.title}</span>
                )}
                {item.note ? ` ${item.note}` : null}
              </li>
            ))}
          </ul>
        )}
      </section>

      <section aria-labelledby="all-agents-title" className="all-block">
        <h3 id="all-agents-title">エージェント</h3>
        <ul className="all-agents" data-testid="all-agents">
          {live.workspaces.map((w) => {
            const readable = w.state === "ok";
            const state = live.data[w.name]?.cycle?.state;
            return (
              <li key={w.name} data-testid="all-agent-row">
                <a
                  className="all-agent-name"
                  href={hrefOf({ kind: "agent", name: w.name, screen: "now" })}
                >
                  {w.name}
                </a>{" "}
                {readable ? (
                  <>
                    <span data-testid="all-agent-cycle">
                      サイクル:{" "}
                      {state !== undefined
                        ? cycleStateText(state)
                        : live.data[w.name] === undefined
                          ? LOADING_TEXT
                          : "読めませんでした"}
                    </span>{" "}
                    <span data-testid="all-agent-waiting">
                      あなたを待っている{" "}
                      {live.needsHuman === null ? "—" : totalOf(w.name)} 件
                    </span>
                  </>
                ) : (
                  <span role="alert" data-testid="all-agent-unreadable">
                    読めません: {w.error ?? w.state}
                  </span>
                )}
              </li>
            );
          })}
        </ul>
      </section>
    </section>
  );
}

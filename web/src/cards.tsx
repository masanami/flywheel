import { type ReactNode, useContext, useState } from "react";
import type { Card, Challenge, ChallengeCard } from "./api/types.gen.ts";
import {
  ACTION_LABELS,
  MODIFIER_LABELS,
  NO_ACTION_LABEL,
  NO_PRIORITY_LABEL,
  STATE_LABELS,
  label,
} from "./labels.ts";
import { OpenDetailContext, WorkspaceNameContext } from "./nav.tsx";

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

export type CardSource = Pick<
  Challenge,
  "id" | "title" | "priority" | "status" | "status_label"
>;

// 課題 1 件のカード。S1 の項目はタイトル・いまの状態・次に人間がすること（＋ ID・優先度・待ちの印）。
export function ChallengeCardView({
  challenge,
  card,
  children,
}: {
  challenge: CardSource;
  card?: Card;
  children?: ReactNode;
}) {
  const open = useContext(OpenDetailContext);
  const workspace = useContext(WorkspaceNameContext);
  const waiting = (card?.next_human_actions.length ?? 0) > 0;
  const state = card?.state ?? challenge.status;
  return (
    <li data-testid={`card-${challenge.id}`} data-waiting={waiting}>
      <div>
        <strong>
          <ClampedText text={challenge.title} />
        </strong>
      </div>
      <div>
        <code>{challenge.id}</code>
        {" ・ "}
        <span data-field="priority">
          {challenge.priority ?? NO_PRIORITY_LABEL}
        </span>
        {waiting ? <mark data-field="waiting"> 人間の操作待ち</mark> : null}
      </div>
      <div data-field="state">
        いまの状態: {label(STATE_LABELS, state)}
        {card?.modifiers.map((m) => (
          <span
            key={m}
            data-modifier={m}
            style={m === "unresponsive" ? { color: "#b00020" } : undefined}
          >
            {" ["}
            {label(MODIFIER_LABELS, m)}
            {"]"}
          </span>
        ))}
      </div>
      <div data-field="next">
        次に人間がすること:{" "}
        {card === undefined
          ? "（カードの情報を取得できていません）"
          : card.next_human_actions.length === 0
            ? NO_ACTION_LABEL
            : card.next_human_actions
                .map((a) => label(ACTION_LABELS, a))
                .join("、")}
      </div>
      {open && workspace ? (
        <button type="button" onClick={() => open(workspace, challenge.id)}>
          詳細を開く
        </button>
      ) : null}
      {children}
    </li>
  );
}

export function cardIndex(
  cards: ChallengeCard[] | undefined,
): Map<string, ChallengeCard> {
  return new Map((cards ?? []).map((c) => [c.id, c]));
}

// id から課題のカードを引いて描く。一覧に無ければ手元の最低限の情報で描く。
export function CardById({
  id,
  fallback,
  index,
  children,
}: {
  id: string;
  fallback?: CardSource;
  index: Map<string, ChallengeCard>;
  children?: ReactNode;
}) {
  const c = index.get(id);
  if (c)
    return (
      <ChallengeCardView challenge={c} card={c.card}>
        {children}
      </ChallengeCardView>
    );
  return (
    <ChallengeCardView
      challenge={
        fallback ?? {
          id,
          title: id,
          priority: null,
          status: "",
          status_label: "",
        }
      }
    >
      {children}
    </ChallengeCardView>
  );
}

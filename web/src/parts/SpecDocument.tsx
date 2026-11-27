import { useRef, useState } from "react";
import { api } from "../api/client.ts";
import type { ChallengeSpecResponse } from "../api/types.gen.ts";
import { LOADING_TEXT, SPEC_UNREADABLE_TEXTS, label } from "../labels.ts";
import { Markdown } from "../markdown/Markdown.tsx";
import type { ScreenData } from "../shell/useScreenData.ts";

export const SPEC_OPEN_LABEL = "仕様の文書を開く";

// 「仕様の文書を開く」。最初は畳み、開くたびに本文を返す口を呼ぶ（画面を開いた時点では読まない。開いた後の読み直しもしない）。
// 読む場所は core が決めるので、送るのは課題の ID だけ。読めない応答は失敗にせず、訳を文で出す。
// 対象（課題・仕様の文書の場所）が変わったら畳み直せるように、呼び出し側が key を付ける。
export function SpecDocument({
  agentName,
  challengeId,
}: {
  agentName: string;
  challengeId: string;
}) {
  const [open, setOpen] = useState(false);
  const [result, setResult] = useState<ScreenData<ChallengeSpecResponse>>({
    state: "loading",
  });
  // 開閉のたびに進める。返る前に畳んで開き直したとき、前の呼び出しの応答を捨てる。
  const latest = useRef(0);

  const toggle = () => {
    const mine = ++latest.current;
    setOpen(!open);
    if (open) return;
    setResult({ state: "loading" });
    api.challengeSpec(agentName, challengeId).then(
      (data) => {
        if (latest.current === mine) setResult({ state: "ready", data });
      },
      (error) => {
        if (latest.current === mine) setResult({ state: "error", error });
      },
    );
  };

  return (
    <div className="spec-document">
      <button
        type="button"
        data-testid="spec-open"
        aria-expanded={open}
        onClick={toggle}
      >
        {SPEC_OPEN_LABEL}
      </button>
      {open && <SpecContent result={result} />}
    </div>
  );
}

function SpecContent({
  result,
}: {
  result: ScreenData<ChallengeSpecResponse>;
}) {
  if (result.state === "loading") return <p>{LOADING_TEXT}</p>;
  if (result.state === "error") {
    const e = result.error;
    return <p role="alert">{e instanceof Error ? e.message : String(e)}</p>;
  }
  const { body, unreadable_reason: reason } = result.data;
  if (body === null) {
    return (
      <p data-testid="spec-unreadable">
        {label(SPEC_UNREADABLE_TEXTS, reason ?? "unreadable")}
      </p>
    );
  }
  return (
    <div className="spec-body" data-testid="spec-body">
      <Markdown text={body} />
    </div>
  );
}

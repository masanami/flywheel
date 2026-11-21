import { useCallback, useEffect, useState } from "react";

// 画面の行き先は URL のハッシュに持つ（ルーティングのライブラリは使わない）。
//   #/all                                  すべて
//   #/a/<名前>/now|challenges|followups|cycle
//   #/a/<名前>/challenges/<課題>           課題の詳細
//   #/a/<名前>/challenges/<課題>/history|plan  課題の詳細（経緯・計画の本文）
//   #/a/<名前>/challenges/<課題>/approve-plan  計画の承認
//   #/a/<名前>/challenges/<課題>/approve-completion  完了の承認
//   #/a/<名前>/challenges/<課題>/tasks/<タスクの ID>  タスクの詳細
// `#` の後ろは最初の `?` で道と問い合わせに分ける（フォローアップの ?status=・?challenge=）。
export const AGENT_SCREENS = [
  "now",
  "challenges",
  "followups",
  "cycle",
] as const;
export type AgentScreen = (typeof AGENT_SCREENS)[number];

export const CHALLENGE_VIEWS = ["tree", "history", "plan"] as const;
export type ChallengeView = (typeof CHALLENGE_VIEWS)[number];

export type Query = Record<string, string>;

export type Route =
  | { kind: "all" }
  | { kind: "agent"; name: string; screen: AgentScreen; query?: Query }
  | { kind: "challenge"; name: string; id: string; view: ChallengeView }
  | { kind: "approve_plan"; name: string; id: string }
  | { kind: "approve_completion"; name: string; id: string }
  | { kind: "task"; name: string; id: string; taskId: string }
  | { kind: "unknown" };

const dec = (s: string): string => {
  try {
    return decodeURIComponent(s);
  } catch {
    return s;
  }
};
const enc = encodeURIComponent;

// ハッシュを行き先にする。ハッシュが無い（空・#・#/）ときは null（既定の行き先を呼び出し側が決める）。
export function parseHash(hash: string): Route | null {
  const raw = hash.replace(/^#/, "");
  const q = raw.indexOf("?");
  const h = q < 0 ? raw : raw.slice(0, q);
  const query: Query = {};
  if (q >= 0) {
    for (const [k, v] of new URLSearchParams(raw.slice(q + 1))) query[k] = v;
  }
  if (h === "" || h === "/") return null;
  const parts = h.split("/").slice(1).map(dec);
  if (parts[0] === "all" && parts.length === 1) return { kind: "all" };
  if (parts[0] !== "a" || parts[1] === undefined || parts[1] === "") {
    return { kind: "unknown" };
  }
  const name = parts[1];
  const screen = parts[2];
  if (parts.length === 3 && AGENT_SCREENS.some((s) => s === screen)) {
    return {
      kind: "agent",
      name,
      screen: screen as AgentScreen,
      ...(Object.keys(query).length > 0 ? { query } : {}),
    };
  }
  const id = parts[3];
  if (screen === "challenges" && id !== undefined && id !== "") {
    if (parts.length === 4)
      return { kind: "challenge", name, id, view: "tree" };
    if (parts.length === 5 && (parts[4] === "history" || parts[4] === "plan")) {
      return { kind: "challenge", name, id, view: parts[4] };
    }
    if (parts.length === 5 && parts[4] === "approve-plan") {
      return { kind: "approve_plan", name, id };
    }
    if (parts.length === 5 && parts[4] === "approve-completion") {
      return { kind: "approve_completion", name, id };
    }
    const taskId = parts[5];
    if (
      parts.length === 6 &&
      parts[4] === "tasks" &&
      taskId !== undefined &&
      taskId !== ""
    ) {
      return { kind: "task", name, id, taskId };
    }
  }
  return { kind: "unknown" };
}

export function hrefOf(r: Route): string {
  switch (r.kind) {
    case "all":
      return "#/all";
    case "agent": {
      const qs = new URLSearchParams(r.query ?? {}).toString();
      return `#/a/${enc(r.name)}/${r.screen}${qs ? `?${qs}` : ""}`;
    }
    case "challenge":
      return `#/a/${enc(r.name)}/challenges/${enc(r.id)}${r.view === "tree" ? "" : `/${r.view}`}`;
    case "approve_plan":
      return `#/a/${enc(r.name)}/challenges/${enc(r.id)}/approve-plan`;
    case "approve_completion":
      return `#/a/${enc(r.name)}/challenges/${enc(r.id)}/approve-completion`;
    case "task":
      return `#/a/${enc(r.name)}/challenges/${enc(r.id)}/tasks/${enc(r.taskId)}`;
    case "unknown":
      return "#/";
  }
}

// 現在のハッシュの行き先と、移る関数（履歴に積むので、ブラウザの「戻る」で前の画面へ戻る）。
// replace を真にすると履歴を増やさずに URL を書き換える（画面の中の切り替え用）。
export function useHashRoute(): [
  Route | null,
  (r: Route, opts?: { replace?: boolean }) => void,
] {
  const [route, setRoute] = useState<Route | null>(() =>
    parseHash(window.location.hash),
  );
  useEffect(() => {
    const on = () => setRoute(parseHash(window.location.hash));
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  const go = useCallback((r: Route, opts?: { replace?: boolean }) => {
    if (opts?.replace) {
      window.history.replaceState(null, "", hrefOf(r));
      setRoute(r);
    } else {
      window.location.hash = hrefOf(r);
    }
  }, []);
  return [route, go];
}

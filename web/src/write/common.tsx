import { type ReactNode, useEffect, useId, useRef, useState } from "react";
import type { Workspace } from "../api/types.gen.ts";
import { ApiError } from "../api/write.ts";

export function errorText(e: unknown): string {
  if (e instanceof ApiError) return `${e.code}: ${e.message}`;
  return e instanceof Error ? e.message : String(e);
}

export function isConflict(e: unknown): boolean {
  return e instanceof ApiError && e.code === "conflict";
}

// state が ok のワークスペースから 1 つ選ぶ。
export function useWorkspaceChoice(workspaces: Workspace[]) {
  const ok = workspaces.filter((w) => w.state === "ok");
  const [picked, setPicked] = useState<string | null>(null);
  const current = ok.find((w) => w.name === picked) ?? ok[0] ?? null;
  return { ok, current, setPicked };
}

export function WorkspaceSelect({
  workspaces,
  value,
  onChange,
}: {
  workspaces: Workspace[];
  value: string | null;
  onChange: (name: string) => void;
}) {
  return (
    <label className="field-inline">
      ワークスペース
      <select value={value ?? ""} onChange={(e) => onChange(e.target.value)}>
        {workspaces.map((w) => (
          <option key={w.name} value={w.name}>
            {w.name}
          </option>
        ))}
      </select>
    </label>
  );
}

export function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: ReactNode;
}) {
  return (
    // biome-ignore lint/a11y/noLabelWithoutControl: 入力部品は children として渡される
    <label className="field">
      <span className="field-label">{label}</span>
      {children}
      {hint ? <span className="field-hint">{hint}</span> : null}
    </label>
  );
}

// パスキーの確認ダイアログ。見た目だけで、本物の WebAuthn は呼ばない（M4H1: 操作のたびに求める）。
// 「パスキーで確認する」を押すと確認中の表示になり、少し待ってから onApprove を呼ぶ。
export function PasskeyDialog({
  title,
  children,
  error,
  busy,
  approveLabel,
  verifyMs = 700,
  onApprove,
  onCancel,
}: {
  title: string;
  children: ReactNode;
  error: string | null;
  busy: boolean;
  approveLabel: string;
  verifyMs?: number;
  onApprove: () => void;
  onCancel: () => void;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  const [verifying, setVerifying] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const cancelRef = useRef(onCancel);
  cancelRef.current = onCancel;

  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (!d.open) {
      if (typeof d.showModal === "function") d.showModal();
      else d.setAttribute("open", "");
    }
    const onClose = () => cancelRef.current();
    d.addEventListener("close", onClose);
    return () => {
      d.removeEventListener("close", onClose);
      clearTimeout(timer.current);
    };
  }, []);

  const start = () => {
    setVerifying(true);
    timer.current = setTimeout(() => {
      setVerifying(false);
      onApprove();
    }, verifyMs);
  };
  const cancel = () => {
    clearTimeout(timer.current);
    setVerifying(false);
    cancelRef.current();
  };

  return (
    <dialog
      ref={ref}
      className="passkey-dialog"
      aria-labelledby={titleId}
      data-testid="passkey-dialog"
    >
      <h2 id={titleId}>{title}</h2>
      <div className="passkey-body">{children}</div>
      {error ? (
        <p role="alert" className="form-error">
          {error}
        </p>
      ) : null}
      <p className="passkey-note">
        この確認は見た目だけです（本物のパスキー〔WebAuthn〕は呼びません）。本物では操作のたびに端末のパスキーを求めます。
      </p>
      {verifying ? (
        <output className="passkey-verifying">
          パスキーを確認しています…（端末の認証を待っている表示）
        </output>
      ) : null}
      <div className="passkey-actions">
        <button type="button" onClick={cancel} disabled={busy}>
          やめる
        </button>
        <button
          type="button"
          className="primary"
          onClick={start}
          disabled={busy || verifying}
        >
          {approveLabel}
        </button>
      </div>
    </dialog>
  );
}

import { useState } from "react";
import { api } from "../api/client.ts";
import type { DecisionPrepared, DecisionRequest } from "../api/write.ts";
import { ClampedText } from "../cards.tsx";
import { PasskeyDialog, errorText, isConflict } from "./common.tsx";

const ACTION_TITLE = {
  reject: "差し戻し",
  answer: "保留への回答",
  increase_budget: "予算の増額",
} as const;

// 差し戻し・回答・増額は 1 件ずつ、操作のたびにパスキーを求める（M4P21）。
// 要求を渡すと ① 要約を得て確認ダイアログを出し、確認できたら ② を呼ぶ。
export function DecisionAction({
  workspace,
  request,
  label,
  disabled,
  onDone,
}: {
  workspace: string;
  request: DecisionRequest;
  label: string;
  disabled?: boolean;
  onDone?: (message: string) => void;
}) {
  const [prepared, setPrepared] = useState<DecisionPrepared | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [dialogError, setDialogError] = useState<string | null>(null);

  const prepare = async () => {
    setBusy(true);
    setError(null);
    setDialogError(null);
    try {
      setPrepared(await api.prepareDecision(workspace, request));
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const execute = async () => {
    if (!prepared) return;
    setBusy(true);
    setDialogError(null);
    try {
      const r = await api.executeDecision(workspace, {
        sign_id: prepared.sign_id,
        version: prepared.version,
        attestation: "mock-passkey",
      });
      setPrepared(null);
      onDone?.(`${r.challenge_id}: ${ACTION_TITLE[r.action]}を行いました`);
    } catch (e) {
      if (isConflict(e)) {
        try {
          setPrepared(await api.prepareDecision(workspace, request));
          setDialogError(
            `${errorText(e)} 最新の内容を読み直しました。もう一度確認してください。`,
          );
        } catch (e2) {
          setPrepared(null);
          setError(`${errorText(e)} / 読み直しにも失敗: ${errorText(e2)}`);
        }
      } else {
        setDialogError(errorText(e));
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <button
        type="button"
        disabled={disabled || busy}
        onClick={() => void prepare()}
      >
        {label}
      </button>
      {error ? (
        <p role="alert" className="form-error">
          {error}
        </p>
      ) : null}
      {prepared ? (
        <PasskeyDialog
          title={`${ACTION_TITLE[prepared.action]}: ${prepared.challenge_id}`}
          error={dialogError}
          busy={busy}
          approveLabel="パスキーで確認して実行する"
          onApprove={() => void execute()}
          onCancel={() => {
            setPrepared(null);
            setDialogError(null);
          }}
        >
          <p>
            <ClampedText text={prepared.title} />
          </p>
          <p className="form-meta">対象の版: v{prepared.version}</p>
          <p>
            <ClampedText text={prepared.summary} />
          </p>
        </PasskeyDialog>
      ) : null}
    </>
  );
}

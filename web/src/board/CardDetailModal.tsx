import { type ReactNode, useContext, useEffect, useRef, useState } from "react";
import { DetailSections, type Loaded, load } from "../detail.tsx";
import { RefreshKeyContext } from "../nav.tsx";
import { ApprovalGuide } from "./ApprovalGuide.tsx";
import { CardFields } from "./CardFields.tsx";
import type { Challenge } from "./types.ts";

type CardDetailModalProps = {
  challenge: Challenge;
  agentName: string;
  workspacePath: string;
  // 4 項目の下に足す欄（画面ごとの案の欄を詳細に置くための口）
  extraFields?: ReactNode;
  onClose: () => void;
};

type LoadState =
  | { status: "loading" }
  | { status: "success"; loaded: Loaded }
  | { status: "error" };

export function CardDetailModal({
  challenge,
  agentName,
  workspacePath,
  extraFields,
  onClose,
}: CardDetailModalProps) {
  const [loadState, setLoadState] = useState<LoadState>({ status: "loading" });
  const refreshKey = useContext(RefreshKeyContext);
  const dialogRef = useRef<HTMLDialogElement>(null);
  const mouseDownOnDialog = useRef(false);
  const onCloseRef = useRef(onClose);
  useEffect(() => {
    onCloseRef.current = onClose;
  }, [onClose]);

  // <dialog> の showModal() で開く（Esc・フォーカストラップ・背景の不活性化はブラウザの標準動作に任せる）。
  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) {
      return;
    }
    if (!dialog.open && typeof dialog.showModal === "function") {
      dialog.showModal();
    }
    const handleClose = () => onCloseRef.current();
    dialog.addEventListener("close", handleClose);
    return () => dialog.removeEventListener("close", handleClose);
  }, []);

  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshKey は再取得の合図
  useEffect(() => {
    let cancelled = false;
    load(agentName, challenge.id).then(
      (loaded) => {
        if (!cancelled) setLoadState({ status: "success", loaded });
      },
      () => {
        if (!cancelled) setLoadState({ status: "error" });
      },
    );
    return () => {
      cancelled = true;
    };
  }, [agentName, challenge.id, refreshKey]);

  const close = () => {
    const dialog = dialogRef.current;
    if (dialog?.open) {
      dialog.close();
    } else {
      onCloseRef.current();
    }
  };

  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: 背景クリックで閉じるのは補助。キーボードの Esc は <dialog> が標準で閉じる
    <dialog
      ref={dialogRef}
      className="card-detail-modal"
      data-testid="modal-overlay"
      aria-labelledby="card-detail-modal-title"
      onMouseDown={(event) => {
        mouseDownOnDialog.current = event.target === event.currentTarget;
      }}
      onClick={(event) => {
        if (mouseDownOnDialog.current && event.target === event.currentTarget) {
          close();
        }
        mouseDownOnDialog.current = false;
      }}
    >
      <div
        className="card-detail-modal-content"
        data-testid="card-detail-content"
      >
        <div className="card-detail-modal-header">
          <h2 id="card-detail-modal-title" className="card-detail-modal-title">
            {challenge.title}
          </h2>
          <button type="button" className="modal-close-button" onClick={close}>
            閉じる
          </button>
        </div>

        <CardFields
          challenge={challenge}
          className="card-detail-card-fields"
          testId="card-detail-card-fields"
        />

        {extraFields}

        <dl className="card-detail-fields">
          <dt>ID</dt>
          <dd>{challenge.id}</dd>
          <dt>タイトル</dt>
          <dd>{challenge.title}</dd>
          <dt>ステータス</dt>
          <dd>{challenge.status}</dd>
          <dt>優先度</dt>
          <dd>{challenge.priority ?? "-"}</dd>
          <dt>要対応</dt>
          <dd>{challenge.needsHuman ? "はい" : "いいえ"}</dd>
          <dt>要約</dt>
          <dd>{challenge.summary ?? "-"}</dd>
        </dl>

        <section className="card-detail-ledger-join" data-testid="ledger-join">
          <h3 className="card-detail-ledger-join-heading">課題</h3>
          <dl className="card-detail-fields">
            <dt>完了条件</dt>
            <dd
              className="card-detail-multiline"
              data-testid="ledger-completion-criteria"
            >
              {challenge.completionCriteria ?? "-"}
            </dd>
            <dt>説明</dt>
            <dd
              className="card-detail-multiline card-detail-description"
              data-testid="ledger-description"
            >
              {challenge.description ?? "-"}
            </dd>
          </dl>
        </section>

        {loadState.status === "loading" && (
          <div className="card-detail-log-loading">読み込み中...</div>
        )}
        {loadState.status === "error" && (
          <div className="card-detail-log-error">詳細の取得に失敗しました</div>
        )}
        {loadState.status === "success" && (
          <DetailSections l={loadState.loaded} />
        )}

        <ApprovalGuide
          challenge={challenge}
          workspacePath={workspacePath}
          className="card-detail-modal-footer"
        />
      </div>
    </dialog>
  );
}

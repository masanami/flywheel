package core

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
)

// このファイルは、課題の仕様の文書の本文を読む口を持つ。読む場所は課題と下書きづくりの記録から決め、呼び出し元からは
// 受け取らない。ストアもファイルも変えない。

// SpecUnreadableReason は仕様の文書の本文を読めない訳の閉集合。
type SpecUnreadableReason string

// SpecUnreadableReason の5値。
const (
	SpecUnreadableMissing        SpecUnreadableReason = "missing"
	SpecUnreadableInvalidPath    SpecUnreadableReason = "invalid_path"
	SpecUnreadableNotRegularFile SpecUnreadableReason = "not_regular_file"
	SpecUnreadableTooLarge       SpecUnreadableReason = "too_large"
	SpecUnreadableOther          SpecUnreadableReason = "unreadable"
)

// specUnreadableReasonOf は readSpecFile の誤りを訳の値へ写す。誤りの文は手元の絶対パスを含みうるので、外へは訳の値だけを出す。
func specUnreadableReasonOf(err error) SpecUnreadableReason {
	switch {
	// 場所の検査の誤りはファイルの系の誤りを包みうるので、先に見る。
	case errors.Is(err, errSpecPathAbsolute), errors.Is(err, errSpecPathOutside), errors.Is(err, errSpecPathInvalid):
		return SpecUnreadableInvalidPath
	case errors.Is(err, fs.ErrNotExist):
		return SpecUnreadableMissing
	case errors.Is(err, errSpecNotRegular):
		return SpecUnreadableNotRegularFile
	case errors.Is(err, errSpecTooLarge):
		return SpecUnreadableTooLarge
	}
	return SpecUnreadableOther
}

// ChallengeSpec は課題の仕様の文書の本文。Body と UnreadableReason は、どちらか一方だけが非 nil。
type ChallengeSpec struct {
	// Path は読んだ場所（ワークスペースから見た相対のパス）。IsDraft はそれが下書きの置き場の下か。
	Path    string
	IsDraft bool
	// Body は本文の全文（空の文書は空の文字列）。
	Body             *string
	UnreadableReason *SpecUnreadableReason
}

// ReadChallengeSpec は課題 id の仕様の文書の本文を返す。読む場所は、課題の仕様の文書の場所。無ければ、途中の下書きづくりの
// 記録の下書きの場所。どちらも無い課題と、無い課題は ErrNotFound。場所の文書を読めないことは誤りにせず、
// UnreadableReason に訳を入れる。受入基準の有無は問わない。
func (s *Store) ReadChallengeSpec(ctx context.Context, id string) (*ChallengeSpec, error) {
	cid, ok := parseChallengeID(id)
	if !ok {
		return nil, ErrNotFound
	}
	var location string
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		c, err := loadChallenge(ctx, tx, cid)
		if err != nil {
			return err
		}
		if c.SpecPath != nil {
			location = *c.SpecPath
			return nil
		}
		d, err := loadSpecDraft(ctx, tx, cid)
		if err != nil {
			return err
		}
		// 途中の状態は、計画を作る判断点の対象から外す状態と同じ。
		if d == nil || !d.State.blocksPlanning() {
			return ErrNotFound
		}
		location = d.Path
		return nil
	})
	if err = classifyReadWriteErr(err); err != nil {
		return nil, err
	}
	out := &ChallengeSpec{Path: location, IsDraft: isSpecDraftPath(location)}
	data, err := readSpecFile(s.workspace, location)
	if err != nil {
		reason := specUnreadableReasonOf(err)
		out.UnreadableReason = &reason
		return out, nil
	}
	body := string(data)
	out.Body = &body
	return out, nil
}

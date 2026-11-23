package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// maxScreenBody は画面のボタンの書き込みの口（取り込み・フォローアップ）の要求本文の上限。
const maxScreenBody = 64 << 10

// decodeScreenBody は要求本文を v へ厳密に読む（未知の欄・本文の後ろの続きは断る）。読めなければ 400 validation_failed を書いて false を返す。
func decodeScreenBody(w http.ResponseWriter, r *http.Request, v any) bool {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxScreenBody))
	if err == nil {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err = dec.Decode(v); err == nil {
			if _, terr := dec.Token(); !errors.Is(terr, io.EOF) {
				err = errors.New("trailing data")
			}
		}
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "本文が正しい形ではない")
		return false
	}
	return true
}

// distinctNonEmpty は ids が 1 件以上で、重複が無いことを返す（ターミナルの followup done・drop が同じ ID の 2 度の指定を断るのと同じ）。
func distinctNonEmpty(ids []string) bool {
	if len(ids) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

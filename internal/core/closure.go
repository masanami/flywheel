package core

// このファイルは遷移表・語彙の閉包を検査する純粋関数を持つ。table・vocab を
// 引数として受け取る設計にしているのは、テストが実際の Table・StatusVocabulary
// だけでなく、意図的に壊した合成データに対しても検出ロジック自体を単体で
// 検査できるようにするため（internal/core/closure_test.go）。
// AC-25・AC-26・AC-27（docs/features/m1-core.md §状態機械・§受入基準）に対応する。

// UnknownStatusesReferencedByTable は table の中で From・固定 Target に
// 使われている状態のうち、vocab に無いものを返す（AC-25）。重複は1回だけ報告する。
//
// NoStatus（空文字列）の扱いは From と Target で非対称にする: From での
// NoStatus は T1（遷移元が無い create）の正当な表現として許可するが、
// ReturnToPrecedingHoldStatus でない行の Target.Fixed が NoStatus であることは
// 遷移先の設定漏れ（データの欠落）であり、常に違反として報告する。
// ReturnToPrecedingHoldStatus の行自体は固定の
// Target を持たないため対象外（HoldEntrySources 経由で From 側から別途検査される）。
func UnknownStatusesReferencedByTable(table []Transition, vocab []StatusVocabEntry) []Status {
	known := map[Status]bool{}
	for _, e := range vocab {
		known[e.Code] = true
	}

	seen := map[Status]bool{}
	var out []Status
	report := func(s Status, allowNoStatus bool) {
		if allowNoStatus && s == NoStatus {
			return
		}
		if known[s] || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}

	for _, tr := range table {
		report(tr.From, true)
		if !tr.Target.ReturnToPrecedingHoldStatus {
			report(tr.Target.Fixed, false)
		}
	}
	return out
}

// UnreachableFromCreate は vocab の状態のうち、T1（From が NoStatus の行）を
// 起点に table を辿っても到達できないものを返す（AC-26）。
//
// ReturnToPrecedingHoldStatus の行（T12 相当）は、この閉包計算では新たな
// 到達性を生まない（no-op）として扱う。理由: T12（answer）は「保留に入る
// 直前の状態へ戻る」遷移であり、その直前状態 S は、そもそも T10・T11
// （S --hold--> 人間対応待ち）を辿れた時点で既に reached[S] が真でなければ
// ならない（hold は「今 S にいる」ことが前提の遷移であり、結果ではない）。
// そのため T12 を「人間対応待ちが reached になった時点で HoldEntrySources を
// 無条件に reached にする」と実装すると、S 自身への正規の到達経路
// （T2・T5・T7 など）が壊れていても、hold 経由で S が reached に見えてしまう
// 偽陰性を生む（T2 を遷移表から除いても classified が unreachable と
// 検出されない）。よって T12 の行は歩かない（continue するだけ）。
func UnreachableFromCreate(table []Transition, vocab []StatusVocabEntry) []Status {
	reached := map[Status]bool{}
	for {
		before := len(reached)
		for _, tr := range table {
			if tr.Target.ReturnToPrecedingHoldStatus {
				continue
			}
			// T1（From=NoStatus）は無条件に起点として辿れる。それ以外は
			// From がすでに到達済みでなければまだ辿れない。
			if tr.From != NoStatus && !reached[tr.From] {
				continue
			}
			reached[tr.Target.Fixed] = true
		}
		if len(reached) == before {
			break
		}
	}

	var out []Status
	for _, e := range vocab {
		if !reached[e.Code] {
			out = append(out, e.Code)
		}
	}
	return out
}

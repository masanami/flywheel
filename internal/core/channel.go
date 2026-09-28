package core

// Channel は core へ変更を要求してきた経路（docs/features/m1-core.md
// §クリティカル設計決定 1「登録簿方式」の (channel, verification) の組のうち
// channel 側）。#9 の時点では CLI からの呼び出ししか無いため ChannelCLI の
// 1 値だけを定義する。承認（#12）で登録簿と Verification を組み合わせて使う。
type Channel string

// ChannelCLI は CLI から呼び出されたことを表す唯一の経路（#9 時点）。
const ChannelCLI Channel = "cli"

// ChannelInvoker は判断点の出力によって core の遷移が呼ばれたことを表す経路
// （#84。docs/features/m3-invoker-delegation.md §判断点の共通の規則
// 「判断点の出力による変更の作業ログは、経路を invoker…とし」）。
// verificationRegistry（verification.go）にこの経路の本人確認つきの組み合わせを
// 一切登録しないことで、「経路 invoker からは本人確認つきの操作（承認・
// 差し戻し・保留への回答）を受け付けない」（同節）を、Verify の登録簿検査
// （既存の fail-closed 機構）だけで満たす。新しい特別扱いのコードは足さない。
const ChannelInvoker Channel = "invoker"

// Verification は本人確認の方式（§データモデル activity.verification /
// approval.verification）。本人確認のない操作は VerificationNone を使う
// （「本人確認のない操作は本人確認の方式を none として記録する」＝仕様）。
type Verification string

// VerificationNone は本人確認が行われていないことを表す。#9 の操作
// （create・edit）はすべて本人確認を要さないため、core 内部でこの値に固定する。
const VerificationNone Verification = "none"

// VerificationTTYConfirm は端末での本人確認の方式（#11。
// docs/features/m1-core.md §クリティカル設計決定 1）。M1 の登録簿は
// (ChannelCLI, VerificationTTYConfirm) の 1 組だけを許可する。
const VerificationTTYConfirm Verification = "tty_confirm"

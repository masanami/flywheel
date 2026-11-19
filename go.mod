module github.com/masanami/flywheel

// 1.26.11 以上を求める。1.26.5 より前の 1.26 系には、macOS（Apple シリコン）で -race を付けたテストが
// 子のプロセスを起動するとき、まれに落ちる・固まる不具合がある（golang/go#79804。直しは 1.26.5 から。
// flywheel#768）。1.26.5 より下げない。1.26.11 は、上げた時点で手元で確かめた修正版。
go 1.26.11

require (
	github.com/creack/pty v1.1.24
	golang.org/x/term v0.46.0
	modernc.org/sqlite v1.59.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.48.0 // indirect
	modernc.org/libc v1.75.7 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

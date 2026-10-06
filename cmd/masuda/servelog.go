package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// serveLogName はmasuda serveのログのファイル名。データディレクトリのlogs/の下に置く。
const serveLogName = "masuda-serve.log"

// openServeLog はserveのログのファイルを開く。flagValueが"-"ならnilを返し、標準エラー出力のままにする。
// 回すのは起動のときだけ（前のファイルを.1にする）。動いている間に回すと、書き込みの途中で行がちぎれる・
// 開き直しに失敗してログが止まる、といった壊れ方がありうるので、しない。量は1日あたり数百KBで足りる。
// 開けないときは、ログを失わないよう標準エラー出力のままにする（その旨をwarnに書く）。
func openServeLog(flagValue, dataDir string, warn func(format string, a ...any)) (string, *os.File) {
	if flagValue == "-" {
		return "", nil
	}
	path := flagValue
	if path == "" {
		path = filepath.Join(dataDir, "logs", serveLogName)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		warn("masuda: cannot create the log directory (%v); logging to stderr\n", err)
		return "", nil
	}
	if err := os.Rename(path, path+".1"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		warn("masuda: cannot rotate %s (%v); appending to it\n", path, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		warn("masuda: cannot open the log file (%v); logging to stderr\n", err)
		return "", nil
	}
	return path, f
}

// warnTo はwに書くwarnを返す。
func warnTo(w *os.File) func(format string, a ...any) {
	return func(format string, a ...any) { fmt.Fprintf(w, format, a...) }
}

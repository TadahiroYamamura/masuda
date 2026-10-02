package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// ServeConfig は`$XDG_CONFIG_HOME/masuda/config.json`の形。リポジトリに依らない`masuda serve`
// 全体の設定を置く（リポジトリごとの設定は`.masuda/settings.json`・`settings.local.json`）。
type ServeConfig struct {
	// Listen はUDSに加えてConnectを待ち受けるループバックのアドレス（例 "127.0.0.1:7788"）。
	// 空ならUDSだけ。ブラウザのGUIはUDSに繋げないので、これを設定して使う。
	Listen string `json:"listen,omitempty"`
	// SandboxSocket は`masuda-sandbox serve`のUDSのパス。空なら`$XDG_RUNTIME_DIR/masuda-sandbox.sock`。
	SandboxSocket string `json:"sandboxSocket,omitempty"`
	// StallAfter は無活動のしきい値の既定（Goのduration）。リポジトリのsettings.local.jsonの
	// stallAfterがあればそちらが、`masuda serve --stall-after`があればさらにそちらが勝つ。
	StallAfter string `json:"stallAfter,omitempty"`
	// DiskWarnBytes はワークスペース置き場の使用量の警告のしきい値（バイト）。0なら既定。
	// 置き場はserve全体で1つなので、ここだけに置く。
	DiskWarnBytes int64 `json:"diskWarnBytes,omitempty"`
}

// ServeConfigPath はconfig.jsonの既定の場所（`$XDG_CONFIG_HOME/masuda/config.json`、
// 未設定なら`~/.config/masuda/config.json`）。
func ServeConfigPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "masuda", "config.json")
}

// LoadServe はpathのconfig.jsonを読んで検査する。無ければゼロ値（すべて既定）。
func LoadServe(path string) (ServeConfig, error) {
	var c ServeConfig
	if path == "" {
		return c, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	} else if err != nil {
		return c, err
	}
	if err := decodeStrict(data, &c); err != nil {
		return c, fmt.Errorf("parsing %s: %w", path, err)
	}
	if c.Listen != "" {
		if err := CheckLoopback(c.Listen); err != nil {
			return c, fmt.Errorf("%s: listen: %w", path, err)
		}
	}
	if _, err := c.StallAfterDuration(); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if c.DiskWarnBytes < 0 {
		return c, fmt.Errorf("%s: diskWarnBytes must not be negative", path)
	}
	return c, nil
}

// StallAfterDuration はStallAfterを読む。空なら0（既定に任せる）。
func (c ServeConfig) StallAfterDuration() (time.Duration, error) {
	if c.StallAfter == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(c.StallAfter)
	if err != nil {
		return 0, fmt.Errorf("stallAfter %q: %w", c.StallAfter, err)
	}
	if d <= 0 {
		return 0, errors.New("stallAfter must be positive")
	}
	return d, nil
}

// DiskWarnThreshold はDiskWarnBytesの既定を埋めた値を返す。
func (c ServeConfig) DiskWarnThreshold() int64 {
	if c.DiskWarnBytes <= 0 {
		return DefaultDiskWarnBytes
	}
	return c.DiskWarnBytes
}

// CheckLoopback はaddrが`<ループバックのIP>:<ポート>`であることを確かめる。公開APIは認証を
// 持たない（同じホストアカウントのプロセスを信頼する）ので、外へ向けて待ち受けさせない。
func CheckLoopback(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if port == "" {
		return fmt.Errorf("%q has no port", addr)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%q is not a loopback address (use 127.0.0.1:<port>)", addr)
	}
	return nil
}

package serve

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/internal/guest"
)

// wakeRetryAfter・wakeMaxSends は、送ってもnext_taskが来ないときに送り直すまでの間と、送る回数の上限。
// 送った文が入力欄で失われた場合に備えて送り直すが、Claude Codeが応えない状態を回数で打ち切る。
const (
	wakeRetryAfter = 60 * time.Second
	wakeMaxSends   = 3
)

// fakeWakesFile はフェイクのsandboxで、送る代わりに起こした記録を書くファイル（契約テストが読む）。
const fakeWakesFile = "wakes"

// requestWake はメインセッションが`waiting`を受け取った後なら、起こすよう頼む。
func (c *runCtl) requestWake() {
	c.seqMu.Lock()
	ok := c.waiting
	if ok {
		c.wantWake = true
	}
	c.seqMu.Unlock()
	if ok {
		go c.tryWake()
	}
}

// tryWake は起こすよう頼まれていれば送る。送っている途中、送り直すには早い、回数を使い切った、
// のどれかなら何もしない。見回り（patrol）からも呼び、アタッチ中で送れなかった分と送り直しを拾う。
func (c *runCtl) tryWake() {
	c.seqMu.Lock()
	now := time.Now()
	if !c.waiting || !c.wantWake || c.waking || c.wakeSends >= wakeMaxSends ||
		(c.wakeSends > 0 && now.Sub(c.wakeSentAt) < wakeRetryAfter) {
		c.seqMu.Unlock()
		return
	}
	c.waking = true
	c.seqMu.Unlock()

	attached, err := c.b.sendWake(c.ctx, c.id)

	c.seqMu.Lock()
	c.waking = false
	sent := err == nil && !attached
	if sent {
		c.wakeSends++
		c.wakeSentAt = time.Now()
	}
	n := c.wakeSends
	c.seqMu.Unlock()

	var detail string
	switch {
	case err != nil:
		if c.ctx.Err() != nil {
			return
		}
		detail = "waking the main session failed: " + err.Error()
	case attached:
		detail = "起こす待ち（chat中）"
	default:
		detail = fmt.Sprintf("woke the main session (%d/%d)", n, wakeMaxSends)
	}
	c.b.acts.update(c.id, func(a *activity) { a.detail = detail })
	c.b.statusChanged(c.id)
}

// sendWake はゲストのメインセッションのtmuxへguest.WakeTextを送る。誰かがアタッチしている間は、
// 打っている途中の入力に混ざるので送らずにattachedを返す。
func (b *backend) sendWake(ctx context.Context, id string) (attached bool, err error) {
	if b.fake {
		// フェイクのExecはホストで動くので、tmuxを打つとホストのtmuxに届いてしまう。
		f, err := os.OpenFile(filepath.Join(FakeDir(b.dataDir), id, fakeWakesFile), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			return false, err
		}
		defer f.Close()
		_, err = f.WriteString(time.Now().UTC().Format(time.RFC3339Nano) + "\n")
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tmux := func(args ...string) ([]byte, error) {
		res, err := guest.Exec(ctx, b.sandbox, &sandboxv1.ExecRequest{
			Id: id, Argv: append([]string{"/usr/bin/tmux"}, args...), Cwd: "/",
		})
		if err != nil {
			return nil, err
		}
		if res.ExitCode != 0 {
			return nil, fmt.Errorf("tmux %s: exit %d: %s", args[0], res.ExitCode, bytes.TrimSpace(res.Stderr))
		}
		return res.Stdout, nil
	}
	clients, err := tmux("list-clients", "-t", guest.TmuxSession)
	if err != nil {
		return false, err
	}
	if len(bytes.TrimSpace(clients)) > 0 {
		return true, nil
	}
	// 文と改行を分けて送る。-lで文字どおりに打ち、Enterはキーとして押す（文の中の語を
	// キー名と解釈させないため）。
	if _, err := tmux("send-keys", "-t", guest.TmuxSession, "-l", guest.WakeText); err != nil {
		return false, err
	}
	_, err = tmux("send-keys", "-t", guest.TmuxSession, "Enter")
	return false, err
}

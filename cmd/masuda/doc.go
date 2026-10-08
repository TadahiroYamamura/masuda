package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"github.com/TadahiroYamamura/masuda/internal/docsembed"
)

// docsSite は公開サイトの根。版ごとのディレクトリ（0.3・dev等）がこの下にある（docs.yml）。
const docsSite = "https://tadahiroyamamura.github.io/masuda/"

// runDoc は`masuda doc`。埋め込んだ文書（リリースのビルドだけが持つ）から、ページの一覧・
// ページ・節を出す。--serveなら埋め込んだサイトを127.0.0.1で公開する。埋め込みの無いビルドでは
// 使っている版の公開サイトのURLを案内する。
func runDoc(args []string) error {
	c := newCommand("doc", "doc [<page>[#<id>]] [--serve]")
	serveSite := c.fs.Bool("serve", false, "serve the embedded site on 127.0.0.1 until interrupted")
	pos, err := c.parse(args, 0, 1)
	if err != nil {
		return err
	}
	if *serveSite {
		site, ok := docsembed.Site()
		if !ok {
			fmt.Printf("this build has no embedded documents; read them at %s\n", docURL(version, "", ""))
			return nil
		}
		return serveDocs(site)
	}
	md, ok := docsembed.Markdown()
	if !ok {
		ref, id := "", ""
		if len(pos) == 1 {
			ref, id = splitDocRef(pos[0])
		}
		fmt.Printf("this build has no embedded documents; read them at %s\n", docURL(version, ref, id))
		return nil
	}
	if len(pos) == 0 {
		pages, err := docPages(md)
		if err != nil {
			return err
		}
		fmt.Println("# a link like troubleshooting.md#stalled in a page reads as: masuda doc <dir of the page>/troubleshooting#stalled")
		tw := newTable(os.Stdout)
		for _, p := range pages {
			fmt.Fprintf(tw, "%s\t%s\n", p.ref, p.title)
		}
		return tw.Flush()
	}
	text, err := readDoc(md, pos[0])
	if err != nil {
		return err
	}
	fmt.Print(text)
	return nil
}

type docPage struct{ ref, title string }

var headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*$`)

// attrIDRe は見出しの末尾に明示したid（`{#stalled}`）。
var attrIDRe = regexp.MustCompile(`\s*\{#([A-Za-z0-9_-]+)\}\s*$`)

// docPages はMarkdownのページを、拡張子を除いたパスと最初の見出し（ページの題）で返す。
func docPages(md fs.FS) ([]docPage, error) {
	var pages []docPage
	err := fs.WalkDir(md, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		f, err := md.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		title := ""
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if m := headingRe.FindStringSubmatch(sc.Text()); m != nil {
				title = attrIDRe.ReplaceAllString(m[2], "")
				break
			}
		}
		pages = append(pages, docPage{ref: strings.TrimSuffix(p, ".md"), title: title})
		return sc.Err()
	})
	sort.Slice(pages, func(i, j int) bool { return pages[i].ref < pages[j].ref })
	return pages, err
}

// splitDocRef は`<page>[#<id>]`をページとidに分け、ページの`.md`と先頭の`docs/`を落とす
// （文書内のリンクや、リポジトリのパスをそのまま渡されても引けるように）。
func splitDocRef(arg string) (ref, id string) {
	ref, id, _ = strings.Cut(arg, "#")
	ref = strings.TrimPrefix(strings.TrimSuffix(path.Clean(ref), ".md"), "docs/")
	if ref == "." {
		ref = ""
	}
	return ref, id
}

// readDoc はページ（idがあればその節だけ）のMarkdownを返す。
func readDoc(md fs.FS, arg string) (string, error) {
	ref, id := splitDocRef(arg)
	b, err := fs.ReadFile(md, ref+".md")
	if errors.Is(err, fs.ErrNotExist) {
		b, err = fs.ReadFile(md, path.Join(ref, "index.md"))
	}
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("no page %q; run 'masuda doc' for the list of pages", ref)
	}
	if err != nil {
		return "", err
	}
	if id == "" {
		return string(b), nil
	}
	sec, ok := docSection(string(b), id)
	if !ok {
		return "", fmt.Errorf("page %s has no section {#%s}; only ids written in the page can be used", ref, id)
	}
	return sec, nil
}

// docSection は`{#id}`を明示した見出しから、同じか浅い見出しの手前までを返す。
// コードブロックの中の`#`で始まる行（シェルのコメント等）は見出しとみなさない。
func docSection(md, id string) (string, bool) {
	lines := strings.SplitAfter(md, "\n")
	start, level := -1, 0
	inFence := false
	for i, l := range lines {
		t := strings.TrimRight(l, "\n")
		if strings.HasPrefix(strings.TrimLeft(t, " "), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		m := headingRe.FindStringSubmatch(t)
		if m == nil {
			continue
		}
		if start < 0 {
			if a := attrIDRe.FindStringSubmatch(m[2]); a != nil && a[1] == id {
				start, level = i, len(m[1])
			}
			continue
		}
		if len(m[1]) <= level {
			return strings.Join(lines[start:i], ""), true
		}
	}
	if start < 0 {
		return "", false
	}
	return strings.Join(lines[start:], ""), true
}

// docURL は使っている版の公開サイトでのページのURL。リリースの版（X.Y.Z）ならX.Y、それ以外
// （手元のビルドのdev等）は開発版のdev。サイトはディレクトリのURL（user/install/）で出る。
func docURL(ver, ref, id string) string {
	dir := "dev"
	if m := regexp.MustCompile(`^v?(\d+)\.(\d+)\.\d+$`).FindStringSubmatch(ver); m != nil {
		dir = m[1] + "." + m[2]
	}
	u := docsSite + dir + "/"
	if ref != "" && ref != "index" {
		u += strings.TrimSuffix(ref, "/index") + "/"
	}
	if id != "" {
		u += "#" + id
	}
	return u
}

// docRef はエラーや案内の文から文書を指す書き方。埋め込んだページがあれば`masuda doc`の形、
// 無ければ版のURL。
func docRef(ref, id string) string {
	if md, ok := docsembed.Markdown(); ok {
		if _, err := fs.Stat(md, ref+".md"); err == nil {
			if id != "" {
				return "masuda doc " + ref + "#" + id
			}
			return "masuda doc " + ref
		}
	}
	return docURL(version, ref, id)
}

// serveDocs は埋め込んだサイトを127.0.0.1の空いたポートで公開し、割り込みまで続ける。
// 中身は公開済みの静的な文書なので、待ち受けにトークンは付けない。
func serveDocs(site fs.FS) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{Handler: http.FileServerFS(site)}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	fmt.Printf("serving the documents at http://%s/ (Ctrl-C to stop)\n", ln.Addr())
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

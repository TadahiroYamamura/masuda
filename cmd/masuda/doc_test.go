package main

import (
	"strings"
	"testing"
	"testing/fstest"
)

const troubleshootingMD = `# トラブルシューティング

前置き

## 進まない {#stalled}

- stalled の説明

` + "```sh" + `
## これはコードの中なので見出しではない
masuda chat <id>
` + "```" + `

### 下の見出しは節に含む {#deeper}

詳細

## 次の節 {#next}

次
`

func docFS() fstest.MapFS {
	return fstest.MapFS{
		"index.md":                  {Data: []byte("# masuda\n")},
		"user/troubleshooting.md":   {Data: []byte(troubleshootingMD)},
		"user/install.md":           {Data: []byte("!!! note \"x\"\n    本文\n\n# インストール {#install}\n")},
		"api/index.md":              {Data: []byte("# 公開API\n")},
		"user/reference/schema.png": {Data: []byte("not markdown")},
	}
}

func TestDocPagesはページを拡張子なしのパスと最初の見出しで並べる(t *testing.T) {
	pages, err := docPages(docFS())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range pages {
		got = append(got, p.ref+"="+p.title)
	}
	want := "api/index=公開API,index=masuda,user/install=インストール,user/troubleshooting=トラブルシューティング"
	if strings.Join(got, ",") != want {
		t.Fatalf("pages = %v, want %s", got, want)
	}
}

func TestReadDocはページか明示したidの節を返す(t *testing.T) {
	cases := []struct {
		name, arg, want, wantErr string
	}{
		{"ページ全体", "user/troubleshooting", troubleshootingMD, ""},
		{"拡張子とdocs/が付いていても引ける", "docs/user/troubleshooting.md", troubleshootingMD, ""},
		{"ディレクトリならindex", "api", "# 公開API\n", ""},
		{"節は同じ深さの次の見出しの手前まで（深い見出しとコードの中の#は含む）", "user/troubleshooting#stalled",
			"## 進まない {#stalled}\n\n- stalled の説明\n\n```sh\n## これはコードの中なので見出しではない\nmasuda chat <id>\n```\n\n### 下の見出しは節に含む {#deeper}\n\n詳細\n\n", ""},
		{"深い節は浅い見出しで終わる", "user/troubleshooting#deeper", "### 下の見出しは節に含む {#deeper}\n\n詳細\n\n", ""},
		{"最後の節はページの終わりまで", "user/troubleshooting#next", "## 次の節 {#next}\n\n次\n", ""},
		{"無いページ", "user/nope", "", `no page "user/nope"`},
		{"無いid", "user/troubleshooting#nope", "", "has no section {#nope}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := readDoc(docFS(), c.arg)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("got:\n%q\nwant:\n%q", got, c.want)
			}
		})
	}
}

func TestDocURLはリリースの版ならその版のサイトを開発版ならdevを指す(t *testing.T) {
	cases := []struct{ ver, ref, id, want string }{
		{"0.3.0", "user/troubleshooting", "stalled", "https://tadahiroyamamura.github.io/masuda/0.3/user/troubleshooting/#stalled"},
		{"v1.12.4", "", "", "https://tadahiroyamamura.github.io/masuda/1.12/"},
		{"dev", "user/install", "", "https://tadahiroyamamura.github.io/masuda/dev/user/install/"},
		{"0.3.0-rc1", "api/index", "", "https://tadahiroyamamura.github.io/masuda/dev/api/"},
		{"0.3.0", "index", "", "https://tadahiroyamamura.github.io/masuda/0.3/"},
	}
	for _, c := range cases {
		if got := docURL(c.ver, c.ref, c.id); got != c.want {
			t.Errorf("docURL(%q, %q, %q) = %q, want %q", c.ver, c.ref, c.id, got, c.want)
		}
	}
}

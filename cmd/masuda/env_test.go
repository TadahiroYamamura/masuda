package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/api/v1/apiv1connect"
	"github.com/TadahiroYamamura/masuda/internal/config"
)

func TestParseDotenvの書式(t *testing.T) {
	data := "# comment\n" +
		"\n" +
		"   # indented comment\n" +
		"PLAIN=abc\n" +
		"export EXPORTED=e1\n" +
		"export\tTABBED=e2\n" +
		"  SPACED  =   padded value   \n" +
		"INLINE=val # trailing comment\n" +
		"HASH=a#b\n" +
		"ONLYCOMMENT= # nothing\n" +
		"EMPTY=\n" +
		"SINGLE=' keep \\n \"as is\" # '\n" +
		"DOUBLE=\"a\\nb\\tc\\\\d\\\"e\\$f\\`g\\rh\" # after\n" +
		"DQHASH=\"x # y\"\n" +
		"CRLF=crlf\r\n" +
		"export=notkeyword\n"
	got, err := parseDotenv([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	want := []envEntry{
		{"PLAIN", "abc", 4},
		{"EXPORTED", "e1", 5},
		{"TABBED", "e2", 6},
		{"SPACED", "padded value", 7},
		{"INLINE", "val", 8},
		{"HASH", "a#b", 9},
		{"ONLYCOMMENT", "", 10},
		{"EMPTY", "", 11},
		{"SINGLE", ` keep \n "as is" # `, 12},
		{"DOUBLE", "a\nb\tc\\d\"e$f`g\rh", 13},
		{"DQHASH", "x # y", 14},
		{"CRLF", "crlf", 15},
		{"export", "notkeyword", 16},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseDotenvは誤りを行番号付きで返し値を含めない(t *testing.T) {
	const secret = "s3cr3t-VALUE"
	cases := []struct {
		name, data, want string
	}{
		{"イコールが無い行", "A=1\n" + secret + "\n", "line 2: expected NAME=VALUE"},
		{"名前の形が誤っている", "A=1\n\n1BAD=" + secret + "\n", "line 3: invalid variable name"},
		{"名前が空", "=" + secret + "\n", "line 1: invalid variable name"},
		{"シングルクォートが閉じていない", "K='" + secret + "\n", "line 1: K: unterminated single quote"},
		{"ダブルクォートが閉じていない", "# c\nK=\"" + secret + "\n", "line 2: K: unterminated double quote"},
		{"ダブルクォートが末尾のバックスラッシュで終わる", "K=\"" + secret + "\\", "line 1: K: unterminated double quote"},
		{"許していないエスケープ", "K=\"" + secret + "\\x\"\n", "line 1: K: unsupported escape"},
		{"閉じたクォートの後に文字がある", "K='" + secret + "'tail\n", "line 1: K: unexpected text after the closing quote"},
		{"同じ名前が2回出る", "K=one\nL=x\nK=" + secret + "\n", "line 3: K is already set on line 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseDotenv([]byte(tc.data))
			if err == nil {
				t.Fatal("no error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want it to contain %q", err, tc.want)
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("err contains the value: %q", err)
			}
		})
	}
}

// fakeConfigService はSetSecretだけを受けるmasuda serveの代わり。
type fakeConfigService struct {
	apiv1connect.UnimplementedConfigServiceHandler
	mu     sync.Mutex
	set    []*apiv1.SetSecretRequest
	failOn string
}

func (f *fakeConfigService) SetSecret(_ context.Context, req *connect.Request[apiv1.SetSecretRequest]) (*connect.Response[apiv1.ListSecretsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.Msg.Name == f.failOn {
		return nil, connect.NewError(connect.CodeInternal, errors.New("store is broken"))
	}
	f.set = append(f.set, req.Msg)
	return connect.NewResponse(&apiv1.ListSecretsResponse{}), nil
}

func (f *fakeConfigService) setNames() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for _, r := range f.set {
		out[r.Name] = r.RepoRoot + "|" + r.Value
	}
	return out
}

func startFakeConfig(t *testing.T) (*fakeConfigService, apiv1connect.ConfigServiceClient) {
	t.Helper()
	f := &fakeConfigService{}
	sock := filepath.Join(t.TempDir(), "masuda.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(apiv1connect.NewConfigServiceHandler(f))
	srv := &http.Server{Handler: h2c.NewHandler(mux, &http2.Server{})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return f, dial(sock).config
}

const envSettings = `{
  "egress": ["api.linear.app"],
  "secrets": [
    {"name": "LINEAR_API_KEY", "hosts": ["api.linear.app"]},
    {"name": "LEGACY_KEY", "mode": "plaintext"},
    {"name": "UNUSED_SECRET", "hosts": ["api.linear.app"]}
  ],
  "envFiles": [
    {"path": ".env", "vars": ["LINEAR_API_KEY", "APP_ENV"]},
    {"path": "sub/.env", "vars": ["DEBUG", "LEGACY_KEY"]}
  ]
}`

const envLocal = `{
  "egressApproved": ["api.linear.app"],
  "secretsApproved": ["LEGACY_KEY"],
  "claudeToken": "OTHER_CLAUDE_TOKEN",
  "vars": {"KEEP": "kept", "APP_ENV": "old"},
  "stallAfter": "15m"
}`

// envRepo は秘密とenvFilesを宣言し、settings.local.jsonに既存の項目を置いたリポジトリを作る。
func envRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	writeFile(t, config.SettingsPath(repo), envSettings, 0o644)
	writeFile(t, config.SettingsLocalPath(repo), envLocal, 0o600)
	return repo
}

type envRun struct {
	err            error
	stdout, stderr string
}

func runImportEnv(t *testing.T, client apiv1connect.ConfigServiceClient, repo, data string) envRun {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := importEnv(context.Background(), repo, ".env", []byte(data), client, &stdout, &stderr)
	return envRun{err, stdout.String(), stderr.String()}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const envValues = "LINEAR_API_KEY=lin-VALUE-1\n" +
	"export LEGACY_KEY='legacy-VALUE-2'\n" +
	"APP_ENV=\"dev-VALUE-3\"\n" +
	"DEBUG=\n" +
	"STRAY=stray-VALUE-4\n" +
	"CLAUDE_CODE_OAUTH_TOKEN=claude-VALUE-5\n" +
	"OTHER_CLAUDE_TOKEN=claude-VALUE-6\n"

var envValueStrings = []string{"lin-VALUE-1", "legacy-VALUE-2", "dev-VALUE-3", "stray-VALUE-4", "claude-VALUE-5", "claude-VALUE-6"}

func assertNoValues(t *testing.T, r envRun) {
	t.Helper()
	all := r.stdout + r.stderr
	if r.err != nil {
		all += r.err.Error()
	}
	for _, v := range envValueStrings {
		if strings.Contains(all, v) {
			t.Errorf("output contains the value %q:\n%s", v, all)
		}
	}
}

func TestImportEnvの振り分け(t *testing.T) {
	fake, client := startFakeConfig(t)
	repo := envRepo(t)
	r := runImportEnv(t, client, repo, envValues)
	if r.err != nil {
		t.Fatal(r.err)
	}

	t.Run("宣言済みの秘密はplaintextも含めてリポジトリ単位でSetSecretに渡る", func(t *testing.T) {
		got := fake.setNames()
		want := map[string]string{"LINEAR_API_KEY": repo + "|lin-VALUE-1", "LEGACY_KEY": repo + "|legacy-VALUE-2"}
		if len(got) != len(want) {
			t.Fatalf("SetSecret = %v, want %v", got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("SetSecret %s = %q, want %q", k, got[k], v)
			}
		}
	})

	t.Run("envFilesのvarsの名前はsettings.local.jsonのvarsに書かれ既存の項目は保たれる", func(t *testing.T) {
		local, err := config.LoadLocal(repo)
		if err != nil {
			t.Fatal(err)
		}
		wantVars := map[string]string{"KEEP": "kept", "APP_ENV": "dev-VALUE-3", "DEBUG": ""}
		if len(local.Vars) != len(wantVars) {
			t.Errorf("vars = %v, want %v", local.Vars, wantVars)
		}
		for k, v := range wantVars {
			if got, ok := local.Vars[k]; !ok || got != v {
				t.Errorf("vars[%s] = %q (present %v), want %q", k, got, ok, v)
			}
		}
		if len(local.EgressApproved) != 1 || len(local.SecretsApproved) != 1 || local.ClaudeToken != "OTHER_CLAUDE_TOKEN" || local.StallAfter != "15m" {
			t.Errorf("other fields changed: %+v", local)
		}
		st, err := os.Stat(config.SettingsLocalPath(repo))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v", st.Mode().Perm())
		}
	})

	t.Run("名前ごとの行き先を出し宣言の無い名前とClaudeのトークンは取り込まずに知らせる", func(t *testing.T) {
		for _, line := range []string{
			"LINEAR_API_KEY           secret",
			"LEGACY_KEY               secret",
			"APP_ENV                  var",
			"DEBUG                    var",
			"STRAY                    skipped: not declared",
			"CLAUDE_CODE_OAUTH_TOKEN  skipped: Claude token",
			"OTHER_CLAUDE_TOKEN       skipped: Claude token",
		} {
			if !strings.Contains(r.stdout, line) {
				t.Errorf("stdout lacks %q:\n%s", line, r.stdout)
			}
		}
		if !strings.Contains(r.stderr, "not imported: STRAY\n") {
			t.Errorf("stderr = %q", r.stderr)
		}
		if _, ok := fake.setNames()["CLAUDE_CODE_OAUTH_TOKEN"]; ok {
			t.Error("the Claude token was imported")
		}
	})

	t.Run("値は標準出力にも標準エラーにも出ない", func(t *testing.T) {
		assertNoValues(t, r)
	})
}

func TestImportEnvは秘密が無ければserveに接続しない(t *testing.T) {
	repo := envRepo(t)
	client := dial(filepath.Join(t.TempDir(), "absent.sock")).config
	r := runImportEnv(t, client, repo, "APP_ENV=prod\n")
	if r.err != nil {
		t.Fatal(r.err)
	}
	local, err := config.LoadLocal(repo)
	if err != nil {
		t.Fatal(err)
	}
	if local.Vars["APP_ENV"] != "prod" {
		t.Errorf("vars = %v", local.Vars)
	}
}

func TestImportEnvは誤りがあれば何も書かない(t *testing.T) {
	cases := []struct {
		name, data, want string
	}{
		{"書式の誤り", envValues + "BROKEN\n", ".env: line 8: expected NAME=VALUE"},
		{"同じ名前が2回出る", envValues + "APP_ENV=again\n", ".env: line 8: APP_ENV is already set on line 3"},
		{"宣言済みの秘密の値が空", envValues + "UNUSED_SECRET=\n", ".env: line 8: UNUSED_SECRET is a declared secret but its value is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, client := startFakeConfig(t)
			repo := envRepo(t)
			r := runImportEnv(t, client, repo, tc.data)
			if r.err == nil || !strings.Contains(r.err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", r.err, tc.want)
			}
			if got := fake.setNames(); len(got) != 0 {
				t.Errorf("SetSecret was called: %v", got)
			}
			if got := readFileString(t, config.SettingsLocalPath(repo)); got != envLocal {
				t.Errorf("settings.local.json changed:\n%s", got)
			}
			assertNoValues(t, r)
		})
	}
}

func TestImportEnvは秘密の登録が途中で失敗したら残りを書かずに名前で知らせる(t *testing.T) {
	fake, client := startFakeConfig(t)
	fake.failOn = "LEGACY_KEY"
	repo := envRepo(t)
	r := runImportEnv(t, client, repo, envValues+"UNUSED_SECRET=unused-VALUE\n")
	if r.err == nil || !strings.Contains(r.err.Error(), "setting secret LEGACY_KEY") {
		t.Fatalf("err = %v", r.err)
	}
	if got := fake.setNames(); len(got) != 1 || got["LINEAR_API_KEY"] == "" {
		t.Errorf("SetSecret = %v", got)
	}
	if got := readFileString(t, config.SettingsLocalPath(repo)); got != envLocal {
		t.Errorf("settings.local.json changed:\n%s", got)
	}
	for _, line := range []string{
		"LINEAR_API_KEY           secret",
		"LEGACY_KEY               failed",
		"APP_ENV                  not imported",
		"DEBUG                    not imported",
		"STRAY                    skipped: not declared",
		"UNUSED_SECRET            not imported",
	} {
		if !strings.Contains(r.stdout, line) {
			t.Errorf("stdout lacks %q:\n%s", line, r.stdout)
		}
	}
	assertNoValues(t, r)
}

func TestImportEnvは作業ツリーのトップ以外を断る(t *testing.T) {
	_, client := startFakeConfig(t)
	repo := envRepo(t)
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	r := runImportEnv(t, client, sub, "APP_ENV=prod\n")
	if r.err == nil || !strings.Contains(r.err.Error(), "is not the top of its work tree") {
		t.Fatalf("err = %v", r.err)
	}
	if _, err := os.Stat(config.SettingsLocalPath(sub)); !os.IsNotExist(err) {
		t.Errorf("settings.local.json was created in the subdirectory: %v", err)
	}
}

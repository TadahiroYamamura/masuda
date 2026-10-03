package pitfalls

import (
	"errors"
	"strings"
	"testing"
)

const valid = `{"id":"tz-naive","category":"data","trigger":"日時を保存・比較する変更","question":"タイムゾーン無しの日時が混ざらないか","background":"2025年に夏時間の切り替えで集計がずれた"}`

func TestParseSkipsBlankAndCommentLines(t *testing.T) {
	in := "# チームの落とし穴\n\n" + valid + "\n   \n" + strings.Replace(valid, "tz-naive", "tz-2", 1) + "\n"
	got, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	want := valid + "\n" + strings.Replace(valid, "tz-naive", "tz-2", 1) + "\n"
	if string(got) != want {
		t.Fatalf("Parse = %q, want %q", got, want)
	}
}

func TestParseEmptyFile(t *testing.T) {
	got, err := Parse([]byte("# none yet\n"))
	if err != nil || len(got) != 0 {
		t.Fatalf("Parse = %q, %v", got, err)
	}
}

func TestParseReportsEveryBadLineWithItsNumber(t *testing.T) {
	cases := map[string]string{
		`not json`: "not a JSON object",
		strings.Replace(valid, `"id":"tz-naive"`, `"id":"tz naive"`, 1):        "must not contain whitespace",
		strings.Replace(valid, `"category":"data"`, `"category":"edge"`, 1):    `category "edge" is not one of`,
		strings.Replace(valid, `,"background":"2025年に夏時間の切り替えで集計がずれた"`, ``, 1): `"background" is required`,
		strings.Replace(valid, `"trigger":"日時を保存・比較する変更"`, `"trigger":" "`, 1): `"trigger" is required`,
		strings.Replace(valid, `}`, `,"severity":"high"}`, 1):                  "unknown field",
		valid + ` {}`: "more than one JSON value",
		`["a"]`:       "not a JSON object",
	}
	for line, want := range cases {
		_, err := Parse([]byte(valid + "\n" + line + "\n"))
		var le *LineError
		if !errors.As(err, &le) || le.Line != 2 || !strings.Contains(le.Reason, want) {
			t.Errorf("Parse(%q) = %v, want line 2 with %q", line, err, want)
		}
	}
	_, err := Parse([]byte("x\n" + valid + "\ny\n"))
	if err == nil || !strings.Contains(err.Error(), "line 1:") || !strings.Contains(err.Error(), "line 3:") {
		t.Fatalf("every bad line must be reported: %v", err)
	}
}

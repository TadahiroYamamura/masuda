// Package pitfalls は対象リポジトリの`.masuda/pitfalls.jsonl`（プロジェクト固有の落とし穴）を
// 読んで検査する。形式はmasudaが所有し、engine同梱のplan-questionsがゲストの
// `/masuda/pitfalls.jsonl`から読んで、計画への問いを立てる手がかりにする。
package pitfalls

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// FileName は`.masuda/`の下のファイル名。ゲストでは`/masuda/`の下に同じ名前で置く。
const FileName = "pitfalls.jsonl"

// Categories はcategoryに書ける値。plan-questionsの問いの分類（見落とすと起きる被害の種類）と同じ。
var Categories = []string{"spec", "security", "data", "release", "regression", "performance", "maintainability", "other"}

// LineError は1行の誤り。Lineは1から数える。
type LineError struct {
	Line   int
	Reason string
}

func (e *LineError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Reason) }

// Parse はpitfalls.jsonlの中身を1行ずつ検査し、空行と`#`で始まる行を除いた中身を返す。
// ゲストへはこの戻り値を置く（plan-questionsは1行1件のJSONとして読むので、注釈の行を渡さない）。
// 誤りがあれば、すべての行の誤りを*LineErrorとしてerrors.Joinで返す。
func Parse(b []byte) ([]byte, error) {
	var out bytes.Buffer
	var errs []error
	for i, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if reason := check(trimmed); reason != "" {
			errs = append(errs, &LineError{Line: i + 1, Reason: reason})
			continue
		}
		out.WriteString(trimmed + "\n")
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out.Bytes(), nil
}

// check は1行を検査し、誤りの理由を返す（正しければ空）。項目の欠落と空文字列を区別せず
// 「必須」として扱うのは、空のtrigger・questionでは問いを立てる手がかりにならないため。
func check(line string) string {
	dec := json.NewDecoder(strings.NewReader(line))
	dec.DisallowUnknownFields()
	var p struct {
		ID         *string `json:"id"`
		Category   *string `json:"category"`
		Trigger    *string `json:"trigger"`
		Question   *string `json:"question"`
		Background *string `json:"background"`
	}
	if err := dec.Decode(&p); err != nil {
		return "not a JSON object of {id, category, trigger, question, background}: " + err.Error()
	}
	if dec.More() {
		return "more than one JSON value on the line"
	}
	for _, f := range []struct {
		name string
		v    *string
	}{{"id", p.ID}, {"category", p.Category}, {"trigger", p.Trigger}, {"question", p.Question}, {"background", p.Background}} {
		if f.v == nil || strings.TrimSpace(*f.v) == "" {
			return fmt.Sprintf("%q is required", f.name)
		}
	}
	if strings.IndexFunc(*p.ID, unicode.IsSpace) >= 0 {
		return fmt.Sprintf("id %q must not contain whitespace", *p.ID)
	}
	if !slices.Contains(Categories, *p.Category) {
		return fmt.Sprintf("category %q is not one of %s", *p.Category, strings.Join(Categories, ", "))
	}
	return ""
}

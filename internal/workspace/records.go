package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// GateRecord は開いたゲート1つの記録（`records/gates/<occ>-<seq>.json`）。
// engineは同じ出現からdeviationゲートを何度も開けるので、出現ごとの通し番号Seqで区別する。
type GateRecord struct {
	Occurrence string `json:"occurrence"`
	Seq        int    `json:"seq"`
	Gate       string `json:"gate"`
	Target     string `json:"target,omitempty"`
	TargetHash string `json:"targetHash"`
	Subject    []byte `json:"subject,omitempty"`
	// StagingCommit はtarget=diffのゲートを開いた時点のrefs/heads/<branch>、target=step-diffなら
	// 承認対象を作った作業ツリーのスナップショット（親はその時点のブランチ先頭、refs/masuda/gates/<occ>）。
	StagingCommit string          `json:"stagingCommit,omitempty"`
	OpenedAt      time.Time       `json:"openedAt"`
	Decision      *DecisionRecord `json:"decision,omitempty"`
}

// DecisionRecord はゲートへの人間の判断。
type DecisionRecord struct {
	Outcome       string    `json:"outcome"`
	Comment       string    `json:"comment,omitempty"`
	TargetHash    string    `json:"targetHash,omitempty"`
	ApprovedFiles []string  `json:"approvedFiles,omitempty"`
	DecidedAt     time.Time `json:"decidedAt"`
}

// QuestionItem は質問1つ。
type QuestionItem struct {
	ID      string   `json:"id"`
	Text    string   `json:"text"`
	Options []string `json:"options,omitempty"`
}

// QuestionRecord は開いた質問の記録（`records/questions/<occ>-<seq>.json`）。
// role付きquestionノードのエージェントは1つの出現で何度も聞けるのでSeqで区別する。
type QuestionRecord struct {
	Occurrence string            `json:"occurrence"`
	Seq        int               `json:"seq"`
	Questions  []QuestionItem    `json:"questions"`
	OpenedAt   time.Time         `json:"openedAt"`
	Answers    map[string]string `json:"answers,omitempty"`
	AnsweredAt *time.Time        `json:"answeredAt,omitempty"`
	// DiscardedAt・DiscardReason は答えを待たずに閉じた質問（ask_humanで聞いている間に止めて
	// 再開した等）。閉じた質問には答えられない。
	DiscardedAt   *time.Time `json:"discardedAt,omitempty"`
	DiscardReason string     `json:"discardReason,omitempty"`
}

// occPattern はengineの出現ID（"0003"、foreachのフレーム"0003.1"等）の形。
// 記録のファイル名に使うので、パスを壊す文字を通さない。
var occPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]*$`)

// recordsMu は同じプロセス内で同じワークスペースの記録を並行に書き換えないためのロック。
// ゲートの記録はengineの進行（OpenGate）とAPI（Decide）の両方から書かれる。
var recordsMu sync.Mutex

func (w *Workspace) gatesDir() string     { return filepath.Join(w.RecordsDir(), "gates") }
func (w *Workspace) questionsDir() string { return filepath.Join(w.RecordsDir(), "questions") }

// AddGate はgの通し番号を決めて保存する。
func (w *Workspace) AddGate(g *GateRecord) error {
	if !occPattern.MatchString(g.Occurrence) {
		return fmt.Errorf("occurrence %q is not a valid id", g.Occurrence)
	}
	recordsMu.Lock()
	defer recordsMu.Unlock()
	gs, err := w.gatesLocked()
	if err != nil {
		return err
	}
	g.Seq = 1
	for _, o := range gs {
		if o.Occurrence == g.Occurrence && o.Seq >= g.Seq {
			g.Seq = o.Seq + 1
		}
	}
	return writeJSON(w.gatesDir(), fmt.Sprintf("%s-%d.json", g.Occurrence, g.Seq), g)
}

// SaveGate は既存のゲートの記録を書き戻す。
func (w *Workspace) SaveGate(g *GateRecord) error {
	if !occPattern.MatchString(g.Occurrence) {
		return fmt.Errorf("occurrence %q is not a valid id", g.Occurrence)
	}
	recordsMu.Lock()
	defer recordsMu.Unlock()
	return writeJSON(w.gatesDir(), fmt.Sprintf("%s-%d.json", g.Occurrence, g.Seq), g)
}

// OutcomeSuperseded は人間が判断する前にengineが閉じたゲート（triageで入り直した出現の
// 古いゲート）の判断。
const OutcomeSuperseded = "superseded"

// SupersedeGate はoccの最後のゲートが未判断なら、判断（OutcomeSuperseded）を書いて閉じる。
// engineは出現ごとに最後のゲートだけを閉じるので、それに合わせる。閉じたらtrueを返す。
func (w *Workspace) SupersedeGate(occ, comment string, at time.Time) (bool, error) {
	recordsMu.Lock()
	defer recordsMu.Unlock()
	gs, err := w.gatesLocked()
	if err != nil {
		return false, err
	}
	var last *GateRecord
	for _, g := range gs {
		if g.Occurrence == occ {
			last = g
		}
	}
	if last == nil || last.Decision != nil {
		return false, nil
	}
	last.Decision = &DecisionRecord{Outcome: OutcomeSuperseded, Comment: comment, DecidedAt: at}
	return true, writeJSON(w.gatesDir(), fmt.Sprintf("%s-%d.json", last.Occurrence, last.Seq), last)
}

// Gates は記録されたゲートを開いた順に返す。
func (w *Workspace) Gates() ([]*GateRecord, error) {
	recordsMu.Lock()
	defer recordsMu.Unlock()
	return w.gatesLocked()
}

func (w *Workspace) gatesLocked() ([]*GateRecord, error) {
	var out []*GateRecord
	if err := readJSONDir(w.gatesDir(), func(b []byte) error {
		var g GateRecord
		if err := json.Unmarshal(b, &g); err != nil {
			return err
		}
		out = append(out, &g)
		return nil
	}); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Occurrence != out[j].Occurrence {
			return out[i].Occurrence < out[j].Occurrence
		}
		return out[i].Seq < out[j].Seq
	})
	return out, nil
}

// OpenGates は判断がまだ無いゲートを返す。
func (w *Workspace) OpenGates() ([]*GateRecord, error) {
	gs, err := w.Gates()
	if err != nil {
		return nil, err
	}
	var out []*GateRecord
	for _, g := range gs {
		if g.Decision == nil {
			out = append(out, g)
		}
	}
	return out, nil
}

// AddQuestion はqの通し番号を決めて保存する。
func (w *Workspace) AddQuestion(q *QuestionRecord) error {
	if !occPattern.MatchString(q.Occurrence) {
		return fmt.Errorf("occurrence %q is not a valid id", q.Occurrence)
	}
	recordsMu.Lock()
	defer recordsMu.Unlock()
	qs, err := w.questionsLocked()
	if err != nil {
		return err
	}
	q.Seq = 1
	for _, o := range qs {
		if o.Occurrence == q.Occurrence && o.Seq >= q.Seq {
			q.Seq = o.Seq + 1
		}
	}
	return writeJSON(w.questionsDir(), fmt.Sprintf("%s-%d.json", q.Occurrence, q.Seq), q)
}

// SaveQuestion は既存の質問の記録を書き戻す。
func (w *Workspace) SaveQuestion(q *QuestionRecord) error {
	if !occPattern.MatchString(q.Occurrence) {
		return fmt.Errorf("occurrence %q is not a valid id", q.Occurrence)
	}
	recordsMu.Lock()
	defer recordsMu.Unlock()
	return writeJSON(w.questionsDir(), fmt.Sprintf("%s-%d.json", q.Occurrence, q.Seq), q)
}

// Questions は記録された質問を開いた順に返す。
func (w *Workspace) Questions() ([]*QuestionRecord, error) {
	recordsMu.Lock()
	defer recordsMu.Unlock()
	return w.questionsLocked()
}

func (w *Workspace) questionsLocked() ([]*QuestionRecord, error) {
	var out []*QuestionRecord
	if err := readJSONDir(w.questionsDir(), func(b []byte) error {
		var q QuestionRecord
		if err := json.Unmarshal(b, &q); err != nil {
			return err
		}
		out = append(out, &q)
		return nil
	}); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Occurrence != out[j].Occurrence {
			return out[i].Occurrence < out[j].Occurrence
		}
		return out[i].Seq < out[j].Seq
	})
	return out, nil
}

// OpenQuestions は答えがまだ無く、閉じてもいない質問を返す。
func (w *Workspace) OpenQuestions() ([]*QuestionRecord, error) {
	qs, err := w.Questions()
	if err != nil {
		return nil, err
	}
	var out []*QuestionRecord
	for _, q := range qs {
		if q.Answers == nil && q.DiscardedAt == nil {
			out = append(out, q)
		}
	}
	return out, nil
}

func writeJSON(dir, name string, v any) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+name+"-")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}

func readJSONDir(dir string, f func([]byte) error) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		if err := f(b); err != nil {
			return fmt.Errorf("%s: %w", e.Name(), err)
		}
	}
	return nil
}

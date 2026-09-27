// Package data keeps what agents write through the write_output tool and
// what the engine derives from it (ADR-0073, ADR-0082). Every output is a
// file under the workspace's state directory, filed by the occurrence that
// wrote it, so "the latest value of a name" is found by looking, not kept
// as separate state.
package data

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/TadahiroYamamura/masuda/internal/workflow/def"
)

// Store is the output area of one workspace: <dir>/out/<occurrence>/<name>.
type Store struct {
	Dir string
}

// fileName maps a data name to the file it is stored in. Engine-read data
// get a fixed extension so humans and tools can open them directly; the
// extension is an implementation detail, never written in a workflow
// (ADR-0082).
func fileName(name string) string {
	switch name {
	case def.DataPlan, def.DataFindings, def.DataSelectedPerspectives:
		return name + ".json"
	case def.DataCommitMessage:
		return name + ".txt"
	}
	return name + ".md"
}

// Write validates and stores one output. The error is meant for the agent:
// it says what is wrong so the agent can write it again (ADR-0073).
func (s Store) Write(occurrence, name string, content []byte) (string, error) {
	if def.EngineComputed[name] || name == def.DataInstructions {
		return "", fmt.Errorf("%q is provided by the engine and cannot be written", name)
	}
	if err := Validate(name, content); err != nil {
		return "", err
	}
	dir := filepath.Join(s.Dir, "out", occurrence)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, fileName(name))
	if err := os.WriteFile(p, content, 0o644); err != nil {
		return "", err
	}
	return p, nil
}

// Has reports whether an occurrence wrote a non-empty value for name.
func (s Store) Has(occurrence, name string) bool {
	st, err := os.Stat(filepath.Join(s.Dir, "out", occurrence, fileName(name)))
	return err == nil && st.Size() > 0
}

// Latest returns the path of the most recent value written for name, by
// occurrence order.
func (s Store) Latest(name string) (string, bool, error) {
	occs, err := s.occurrences()
	if err != nil {
		return "", false, err
	}
	for i := len(occs) - 1; i >= 0; i-- {
		p := filepath.Join(s.Dir, "out", occs[i], fileName(name))
		if _, err := os.Stat(p); err == nil {
			return p, true, nil
		}
	}
	return "", false, nil
}

// Path returns where an occurrence's value for name is (or would be).
func (s Store) Path(occurrence, name string) string {
	return filepath.Join(s.Dir, "out", occurrence, fileName(name))
}

func (s Store) occurrences() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.Dir, "out"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	// Occurrence IDs are zero-padded, so name order is occurrence order.
	sort.Strings(out)
	return out, nil
}

// Validate checks an engine-read output against its fixed schema. Other
// names only need to be non-empty: the engine never reads them.
func Validate(name string, content []byte) error {
	if len(strings.TrimSpace(string(content))) == 0 {
		return fmt.Errorf("%s is empty", name)
	}
	switch name {
	case def.DataPlan:
		_, err := ParsePlan(content)
		return err
	case def.DataFindings:
		_, err := ParseFindings(content)
		return err
	case def.DataSelectedPerspectives:
		var names []string
		if err := strictJSON(content, &names); err != nil {
			return fmt.Errorf("selected-perspectives must be a JSON array of perspective names: %w", err)
		}
	case def.DataCommitMessage:
		first, _, _ := strings.Cut(strings.TrimSpace(string(content)), "\n")
		if strings.TrimSpace(first) == "" {
			return errors.New("commit-message needs a summary on its first line")
		}
	}
	return nil
}

func strictJSON(b []byte, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data after the JSON value")
	}
	return nil
}

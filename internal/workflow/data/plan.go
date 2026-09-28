package data

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
)

// Plan is the engine-read form of a plan (ADR-0026, ADR-0028, ADR-0073 ②).
// The summary travels in the same document so the planner writes the plan
// in one write_output call; the engine never interprets it.
type Plan struct {
	Summary            string   `json:"summary"`
	Steps              []Step   `json:"steps"`
	ExpectedByproducts []string `json:"expected_byproducts,omitempty"`
}

type Step struct {
	Description string     `json:"description"`
	Files       []PlanFile `json:"files"`
}

type PlanFile struct {
	Path        string `json:"path"`
	Description string `json:"description"`
}

// ParsePlan validates a plan. Every step must name the files it changes:
// commit scope and the deviation check are computed from them, so a plan
// without files would treat every change as a deviation (ADR-0073).
func ParsePlan(b []byte) (*Plan, error) {
	var p Plan
	if err := strictJSON(b, &p); err != nil {
		return nil, fmt.Errorf("plan must be a JSON object {summary, steps, expected_byproducts}: %w", err)
	}
	var problems []string
	if strings.TrimSpace(p.Summary) == "" {
		problems = append(problems, "summary is empty")
	}
	if len(p.Steps) == 0 {
		problems = append(problems, "steps is empty")
	}
	for i, s := range p.Steps {
		if strings.TrimSpace(s.Description) == "" {
			problems = append(problems, fmt.Sprintf("steps[%d].description is empty", i))
		}
		if len(s.Files) == 0 {
			problems = append(problems, fmt.Sprintf("steps[%d].files is empty", i))
		}
		for j, f := range s.Files {
			if !cleanRelPath(f.Path) {
				problems = append(problems, fmt.Sprintf("steps[%d].files[%d].path %q must be a relative path inside the repository", i, j, f.Path))
			}
		}
	}
	for i, g := range p.ExpectedByproducts {
		if _, err := path.Match(strings.ReplaceAll(g, "**", "*"), ""); err != nil || g == "" {
			problems = append(problems, fmt.Sprintf("expected_byproducts[%d] %q is not a valid glob", i, g))
		}
	}
	if len(problems) > 0 {
		return nil, errors.New("plan is invalid: " + strings.Join(problems, "; "))
	}
	return &p, nil
}

func cleanRelPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") {
		return false
	}
	c := path.Clean(p)
	return c == p && c != "." && !strings.HasPrefix(c, "../") && c != ".."
}

// ReadPlan reads and validates a stored plan, returning its hash too: the
// hash identifies the approved plan for gate decisions and step tags
// (ADR-0066, ADR-0080).
func ReadPlan(file string) (*Plan, string, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, "", err
	}
	p, err := ParsePlan(b)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(b)
	return p, hex.EncodeToString(sum[:]), nil
}

// Files returns the files a commit may include: one step's (index >= 0)
// or the whole plan's (index < 0).
func (p *Plan) Files(index int) []string {
	var out []string
	seen := map[string]bool{}
	for i, s := range p.Steps {
		if index >= 0 && i != index {
			continue
		}
		for _, f := range s.Files {
			if !seen[f.Path] {
				seen[f.Path] = true
				out = append(out, f.Path)
			}
		}
	}
	return out
}

// Byproduct reports whether file matches one of the plan's expected
// byproduct globs (ADR-0028). `**` matches any number of directories.
func (p *Plan) Byproduct(file string) bool {
	for _, g := range p.ExpectedByproducts {
		if globMatch(g, file) {
			return true
		}
	}
	return false
}

func globMatch(pattern, name string) bool {
	if !strings.Contains(pattern, "**") {
		ok, _ := path.Match(pattern, name)
		return ok
	}
	pp := strings.Split(pattern, "/")
	np := strings.Split(name, "/")
	var match func(i, j int) bool
	match = func(i, j int) bool {
		if i == len(pp) {
			return j == len(np)
		}
		if pp[i] == "**" {
			for k := j; k <= len(np); k++ {
				if match(i+1, k) {
					return true
				}
			}
			return false
		}
		if j == len(np) {
			return false
		}
		ok, _ := path.Match(pp[i], np[j])
		return ok && match(i+1, j+1)
	}
	return match(0, 0)
}

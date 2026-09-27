package def

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
)

// Origin says where a definition file was found.
type Origin string

const (
	OriginRepo    Origin = "repo"
	OriginBundled Origin = "bundled"
)

// Source resolves reference paths to files. A path found in Repo wins over
// Bundled, and replaces it whole (ADR-0064): there is no merging.
type Source struct {
	// Repo is the repository's .masuda/ directory; nil if there is none.
	Repo fs.FS
	// Bundled holds the defaults shipped in the binary.
	Bundled fs.FS
}

// Read returns the file behind a reference path such as
// "workflows/develop" (read as workflows/develop.yaml) or "agents/planner"
// (agents/planner.md).
func (s Source) Read(ref string) ([]byte, Origin, error) {
	name, err := fileName(ref)
	if err != nil {
		return nil, "", err
	}
	if s.Repo != nil {
		b, err := fs.ReadFile(s.Repo, name)
		if err == nil {
			return b, OriginRepo, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, "", err
		}
	}
	if s.Bundled != nil {
		b, err := fs.ReadFile(s.Bundled, name)
		if err == nil {
			return b, OriginBundled, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("%s: %w", ref, fs.ErrNotExist)
}

func fileName(ref string) (string, error) {
	switch {
	case validRef(ref, "workflows/"):
		return ref + ".yaml", nil
	case validRef(ref, "agents/"):
		return ref + ".md", nil
	}
	return "", fmt.Errorf("%q is not a reference path (workflows/... or agents/..., without extension)", ref)
}

// Set is a workflow together with every workflow and agent definition it
// reaches. The checks and the engine work on a Set, never on files.
type Set struct {
	Root      string
	Workflows map[string]*Workflow
	Agents    map[string]*Agent
	Origins   map[string]Origin
}

// pending is a reference waiting to be loaded, with where it was
// referenced from for error messages.
type pending struct {
	ref  string
	from Pos
}

// Load reads root and everything it references, following workflow calls
// and foreach bodies transitively. Problems in individual files are
// returned together; a missing or unparsable file does not stop the rest
// from loading, so one run reports as much as possible.
func Load(src Source, root string) (*Set, []*Error) {
	set := &Set{Root: root, Workflows: map[string]*Workflow{}, Agents: map[string]*Agent{}, Origins: map[string]Origin{}}
	var errs []*Error
	seen := map[string]bool{}
	queue := []pending{{root, Pos{}}}
	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]
		if seen[item.ref] {
			continue
		}
		seen[item.ref] = true
		b, origin, err := src.Read(item.ref)
		if err != nil {
			pos := item.from
			if pos.File == "" {
				pos = Pos{File: item.ref}
			}
			errs = append(errs, &Error{Pos: pos, Msg: fmt.Sprintf("cannot read %s: %v", item.ref, err)})
			continue
		}
		set.Origins[item.ref] = origin
		if validRef(item.ref, "agents/") {
			a, aerrs := ParseAgent(item.ref, b)
			errs = append(errs, aerrs...)
			if a != nil {
				set.Agents[item.ref] = a
			}
			continue
		}
		w, werrs := ParseWorkflow(item.ref, b)
		errs = append(errs, werrs...)
		if w == nil {
			continue
		}
		set.Workflows[item.ref] = w
		for _, id := range w.Order {
			n := w.Nodes[id]
			from := Pos{File: w.Path, Node: id}
			if n.Role != "" {
				queue = append(queue, pending{n.Role, from})
			}
			if c := n.Callee(); c != "" {
				queue = append(queue, pending{c, from})
			}
		}
	}
	return set, errs
}

// SortedWorkflows returns the set's workflow paths in a stable order, root
// first.
func (s *Set) SortedWorkflows() []string {
	var out []string
	for p := range s.Workflows {
		if p != s.Root {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	if _, ok := s.Workflows[s.Root]; ok {
		out = append([]string{s.Root}, out...)
	}
	return out
}

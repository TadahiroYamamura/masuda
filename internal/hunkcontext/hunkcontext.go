// Package hunkcontext converts masuda's review findings (review_results/,
// written by orchestrator/implement_review_graph.py) into Hunk's
// --agent-context sidecar JSON schema, so `masuda review hunk` can render
// them as inline annotations on the reviewed diff (ADR-0019, ADR-0020).
package hunkcontext

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// reviewState mirrors .masuda-review-state.json
// (orchestrator/implement_review_graph.py's REVIEW_STATE_JSON).
type reviewState struct {
	RedoCounts map[string]int `json:"redo_counts"`
	Unresolved []struct {
		Idx    int    `json:"idx"`
		Reason string `json:"reason"`
	} `json:"unresolved"`
}

// issue mirrors one element of a 14-perspective result_*.json's "issues"
// array, post ADR-0020 schema (file/startLine/endLine instead of location).
type issue struct {
	Severity    string `json:"severity"`
	File        string `json:"file"`
	StartLine   int    `json:"startLine"`
	EndLine     int    `json:"endLine"`
	Description string `json:"description"`
	Suggestion  string `json:"suggestion"`
}

// reviewResult mirrors a review_results/result_{idx}_attempt{N}.json file.
type reviewResult struct {
	PerspectiveName string  `json:"perspective_name"`
	Issues          []issue `json:"issues"`
}

// crossCuttingFinding mirrors one element of cross_cutting_verified.json,
// post ADR-0020 schema.
type crossCuttingFinding struct {
	Description string `json:"description"`
	File        string `json:"file"`
	StartLine   int    `json:"startLine"`
	EndLine     int    `json:"endLine"`
	Severity    string `json:"severity"`
}

// annotation, fileEntry and agentContext model Hunk's agent-context.json
// sidecar schema.
type annotation struct {
	NewRange  [2]int `json:"newRange"`
	Summary   string `json:"summary"`
	Rationale string `json:"rationale,omitempty"`
	Author    string `json:"author,omitempty"`
}

type fileEntry struct {
	Path        string       `json:"path"`
	Summary     string       `json:"summary"`
	Annotations []annotation `json:"annotations"`
}

type agentContext struct {
	Version int         `json:"version"`
	Summary string      `json:"summary"`
	Files   []fileEntry `json:"files"`
}

const crossCuttingAuthor = "横断的チェック"

// Build reads stateDir's review_results and converts unresolved 14-perspective
// issues plus confirmed cross-cutting findings into Hunk's agent-context.json
// sidecar format, writing it to review_results/hunk-context.json and
// returning its path.
//
// Only unresolved perspectives are included: a perspective in the "fixed"
// list already had its issues resolved in the diff itself, and its
// result_*.json is never rewritten after a fix (see
// _synthesize_task in orchestrator/implement_review_graph.py), so annotating
// it would point at code that no longer looks like that. Cross-cutting
// findings have no such fix loop (ADR-0011), so every entry in
// cross_cutting_verified.json is included unconditionally.
func Build(stateDir string) (string, error) {
	rs, err := readReviewState(stateDir)
	if err != nil {
		return "", err
	}

	byFile := map[string][]annotation{}
	total := 0

	for _, u := range rs.Unresolved {
		attempt := rs.RedoCounts[strconv.Itoa(u.Idx)] + 1
		path := filepath.Join(stateDir, "review_results", fmt.Sprintf("result_%d_attempt%d.json", u.Idx, attempt))
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("reading %s: %w", path, err)
		}
		var result reviewResult
		if err := json.Unmarshal(data, &result); err != nil {
			return "", fmt.Errorf("parsing %s: %w", path, err)
		}
		for _, is := range result.Issues {
			if is.File == "" {
				continue
			}
			byFile[is.File] = append(byFile[is.File], annotation{
				NewRange:  [2]int{is.StartLine, is.EndLine},
				Summary:   is.Description,
				Rationale: rationale(is.Severity, is.Suggestion),
				Author:    result.PerspectiveName,
			})
			total++
		}
	}

	ccPath := filepath.Join(stateDir, "review_results", "cross_cutting_verified.json")
	data, err := os.ReadFile(ccPath)
	switch {
	case err == nil:
		var findings []crossCuttingFinding
		if err := json.Unmarshal(data, &findings); err != nil {
			return "", fmt.Errorf("parsing %s: %w", ccPath, err)
		}
		for _, f := range findings {
			if f.File == "" {
				continue
			}
			byFile[f.File] = append(byFile[f.File], annotation{
				NewRange:  [2]int{f.StartLine, f.EndLine},
				Summary:   f.Description,
				Rationale: rationale(f.Severity, ""),
				Author:    crossCuttingAuthor,
			})
			total++
		}
	case !os.IsNotExist(err):
		return "", fmt.Errorf("reading %s: %w", ccPath, err)
	}

	paths := make([]string, 0, len(byFile))
	for p := range byFile {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	files := make([]fileEntry, 0, len(paths))
	for _, p := range paths {
		anns := byFile[p]
		files = append(files, fileEntry{
			Path:        p,
			Summary:     fmt.Sprintf("%d件の指摘", len(anns)),
			Annotations: anns,
		})
	}

	ctx := agentContext{
		Version: 1,
		Summary: fmt.Sprintf("masuda review: %d件の指摘", total),
		Files:   files,
	}
	out, err := json.MarshalIndent(ctx, "", "  ")
	if err != nil {
		return "", err
	}
	outPath := filepath.Join(stateDir, "review_results", "hunk-context.json")
	if err := os.WriteFile(outPath, out, 0o644); err != nil {
		return "", err
	}
	return outPath, nil
}

func rationale(severity, suggestion string) string {
	if suggestion == "" {
		return fmt.Sprintf("[%s]", severity)
	}
	return fmt.Sprintf("[%s] %s", severity, suggestion)
}

func readReviewState(stateDir string) (reviewState, error) {
	path := filepath.Join(stateDir, ".masuda-review-state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return reviewState{}, fmt.Errorf("reading %s (has review run yet?): %w", path, err)
	}
	var rs reviewState
	if err := json.Unmarshal(data, &rs); err != nil {
		return reviewState{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return rs, nil
}

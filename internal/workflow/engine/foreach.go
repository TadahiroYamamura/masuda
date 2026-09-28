package engine

import (
	"fmt"
	"strings"

	"github.com/TadahiroYamamura/masuda/internal/workflow/def"
)

// foreach runs the body once per item, in order (ADR-0072). Each
// iteration is its own frame, so entry counts and records stay per item.
func (e *Engine) foreach(r *records, cur *Occurrence, n *def.Node, fr Frame) (Status, bool, error) {
	if !cur.ItemsFixed {
		from := ""
		if n.OverFrom != "" {
			for _, o := range r.byFrame[fr.ID] {
				if o.Node == n.OverFrom {
					from = o.ID
				}
			}
		}
		since := ""
		if occs := r.byFrame[fr.ID]; len(occs) > 0 {
			since = occs[0].ID
		}
		items, err := e.Env.Items(n.Over, from, since)
		if err != nil {
			return Status{}, false, err
		}
		cur.Items, cur.ItemsFixed = items, true
		return Status{}, true, putJSON(e.Store, prefixOcc+cur.ID, cur)
	}
	body := e.Set.Workflows[n.Body]
	var incomplete []string
	// Only the first iteration this entry starts gets the feedback sent to
	// the loop: after a stuck step and its approval, that is the step
	// being resumed, and the later ones have nothing to act on in it.
	feedback := cur.Feedback
	for i, item := range cur.Items {
		frameID := iterFrame(cur.ID, i)
		if _, started := e.Store.Get(prefixFrame + frameID); started {
			feedback = ""
		}
		if out, ended := e.frameEnd(frameID); ended {
			if !e.itemRecorded(frameID) {
				if err := e.Env.ItemFinished(n.Over, item.Key, out); err != nil {
					return Status{}, false, err
				}
				if err := e.Store.Put(prefixItemDone+frameID, []byte(out)); err != nil {
					return Status{}, false, err
				}
			}
			if out == def.OutcomeDone {
				continue
			}
			if n.OnIncomplete != "continue" {
				return Status{}, true, e.finish(cur, out, "")
			}
			incomplete = append(incomplete, fmt.Sprintf("- %s: %s", item.Key, out))
			continue
		}
		if _, started := e.Store.Get(prefixFrame + frameID); !started {
			if n.Over == def.OverSteps {
				// A step committed under the current plan is not redone,
				// even after the loop is entered again (ADR-0080).
				done, err := e.Env.StepDone(item.Key)
				if err != nil {
					return Status{}, false, err
				}
				if done {
					continue
				}
			}
			inputs, err := e.bind(fr, n, body, &item)
			if err != nil {
				return Status{}, false, err
			}
			frame := Frame{ID: frameID, Workflow: body.Path, Inputs: inputs, Feedback: feedback}
			if n.Over == def.OverFindings {
				if frame.Tree, err = e.Env.TreeSnapshot(); err != nil {
					return Status{}, false, err
				}
			}
			return Status{}, true, putJSON(e.Store, prefixFrame+frameID, frame)
		}
		return e.stepFrame(r, frameID)
	}
	if len(incomplete) > 0 {
		return Status{}, true, e.finish(cur, def.OutcomeIncomplete, "次の項目が完了しなかった:\n"+strings.Join(incomplete, "\n"))
	}
	return Status{}, true, e.finish(cur, def.OutcomeDone, "")
}

const prefixItemDone = "wf:item-done/"

func (e *Engine) itemRecorded(frameID string) bool {
	_, ok := e.Store.Get(prefixItemDone + frameID)
	return ok
}

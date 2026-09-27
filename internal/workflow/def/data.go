package def

// Data names the engine fixes (ADR-0082). Users may define other names;
// the engine treats those as documents it never reads (ADR-0073 ③).
const (
	DataInstructions         = "instructions"
	DataInvestigation        = "investigation"
	DataPlan                 = "plan"
	DataDiff                 = "diff"
	DataStepDiff             = "step-diff"
	DataFindings             = "findings"
	DataCommitMessage        = "commit-message"
	DataSelectedPerspectives = "selected-perspectives"
	DataReport               = "report"
)

// EngineComputed are data the engine can produce at any point, so an input
// bound to them is always available.
var EngineComputed = map[string]bool{
	DataDiff:     true,
	DataStepDiff: true,
}

// EngineRead are outputs whose content the engine reads and validates
// against a fixed schema, whoever writes them (ADR-0073 ②).
var EngineRead = map[string]bool{
	DataPlan:                 true,
	DataFindings:             true,
	DataCommitMessage:        true,
	DataSelectedPerspectives: true,
}

// StageContract is what the engine guarantees to and requires from the
// workflow that fills a stage (ADR-0082).
type StageContract struct {
	// Inputs are provided by the engine on every entry.
	Inputs []string
	// Outputs are available to later nodes once the stage ends done.
	Outputs []string
	// MustWrite: every path to `end` (outcome done) must pass an agent
	// that declares each of Outputs. Review is exempt because finding
	// nothing is a correct result.
	MustWrite bool
	// RequiresPlan: the content starts with an approved plan and must
	// leave no uncommitted changes when it ends with `end`.
	RequiresPlan bool
	// ReadOnly: the content must not contain a write-capable agent.
	ReadOnly bool
}

// StageContracts lists every stage kind.
var StageContracts = map[Type]StageContract{
	TypeInvestigate: {Inputs: []string{DataInstructions}, Outputs: []string{DataInvestigation}, MustWrite: true},
	TypePlan:        {Inputs: []string{DataInstructions, DataInvestigation}, Outputs: []string{DataPlan}, MustWrite: true},
	TypeImplement:   {Inputs: []string{DataPlan}, RequiresPlan: true},
	TypeReview:      {Inputs: []string{DataDiff}, Outputs: []string{DataFindings}, ReadOnly: true},
}

// Iteration sets a foreach can run over, and the input name each item is
// passed under (ADR-0072, ADR-0077).
const (
	OverSteps        = "steps"
	OverPerspectives = "perspectives"
	OverFindings     = "findings"
)

var OverItemInput = map[string]string{
	OverSteps:        "step",
	OverPerspectives: "perspective",
	OverFindings:     "finding",
}

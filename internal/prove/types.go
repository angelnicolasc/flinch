package prove

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/angelnicolasc/flinch/internal/finding"
	"github.com/angelnicolasc/flinch/internal/reflex"
)

// Verdict is the three-way outcome of a candidate proof (§5.3).
type Verdict string

const (
	VerdictVerified     Verdict = "verified"
	VerdictRefuted      Verdict = "refuted"
	VerdictInconclusive Verdict = "inconclusive"
)

// FailureReason provides a machine-readable reason when a proof does not verify.
type FailureReason string

const (
	ReasonNone                    FailureReason = ""
	ReasonMissingSelector         FailureReason = "missing_selector"
	ReasonMissingAllowedCase      FailureReason = "missing_allowed_case"
	ReasonPassedOnBroken          FailureReason = "passed_on_broken"
	ReasonFailedOnFixed           FailureReason = "failed_on_fixed"
	ReasonCompileOnly             FailureReason = "compile_only"
	ReasonFixedCompileError       FailureReason = "fixed_compile_error"
	ReasonSkippedOnBroken         FailureReason = "skipped_on_broken"
	ReasonSkippedOnFixed          FailureReason = "skipped_on_fixed"
	ReasonNeighborFailed          FailureReason = "neighbor_failed"
	ReasonNeighborSkipped         FailureReason = "neighbor_skipped"
	ReasonNeighborNotExecuted     FailureReason = "neighbor_not_executed"
	ReasonFlaky                   FailureReason = "flaky"
	ReasonSelectorNotExecuted     FailureReason = "selector_not_executed"
	ReasonUnexpectedFailureSignal FailureReason = "unexpected_failure_signal"
	ReasonNoRecognizedAssertion   FailureReason = "no_recognized_assertion"
	ReasonMissedBrokenIncident    FailureReason = "missed_broken_incident"
	ReasonFlaggedFixedCode        FailureReason = "flagged_fixed_code"
	ReasonFlaggedAllowedCase      FailureReason = "flagged_allowed_case"
	ReasonAllowedCaseOutOfScope   FailureReason = "allowed_case_out_of_scope"
	ReasonAmbiguousSiteMatch      FailureReason = "ambiguous_site_match"
	ReasonTimeout                 FailureReason = "timeout"
	ReasonSetupError              FailureReason = "setup_error"
)

// AllowedCase specifies a legitimate near-miss case (D4, P4).
// For lint/structure proofs, TargetPath + Content/SourceFile materializes a file in scope.
// For test proofs, Selector names an executable legitimate neighboring test case.
type AllowedCase struct {
	Name       string `yaml:"name,omitempty" json:"name,omitempty"`
	TargetPath string `yaml:"target_path,omitempty" json:"target_path,omitempty"`
	SourceFile string `yaml:"source_file,omitempty" json:"source_file,omitempty"`
	Content    string `yaml:"content,omitempty" json:"content,omitempty"`
	Selector   string `yaml:"selector,omitempty" json:"selector,omitempty"`
}

// CandidateSpec defines the candidate protection and its proof inputs (D4, §5.1).
type CandidateSpec struct {
	ID                   string            `yaml:"id" json:"id"`
	Version              int               `yaml:"version" json:"version"`
	Rung                 reflex.Rung       `yaml:"rung,omitempty" json:"rung,omitempty"`
	Obligation           string            `yaml:"obligation" json:"obligation"`
	Scope                string            `yaml:"scope,omitempty" json:"scope,omitempty"`
	Exclude              []string          `yaml:"exclude,omitempty" json:"exclude,omitempty"`
	Artifact             reflex.Artifact   `yaml:"artifact" json:"artifact"`
	BrokenRef            string            `yaml:"broken_ref" json:"broken_ref"`
	FixedRef             string            `yaml:"fixed_ref" json:"fixed_ref"` // Git ref or "WORKTREE"
	Patch                string            `yaml:"patch,omitempty" json:"patch,omitempty"`
	OverlayFiles         map[string]string `yaml:"overlay_files,omitempty" json:"overlay_files,omitempty"`
	NeighborSelector     string            `yaml:"neighbor_selector,omitempty" json:"neighbor_selector,omitempty"`
	AllowedCases         []AllowedCase     `yaml:"allowed_cases,omitempty" json:"allowed_cases,omitempty"`
	TestPackages         []string          `yaml:"test_packages,omitempty" json:"test_packages,omitempty"`
	TestSupportGlobs     []string          `yaml:"test_support_globs,omitempty" json:"test_support_globs,omitempty"`
	ExplicitProductFiles []string          `yaml:"explicit_product_files,omitempty" json:"explicit_product_files,omitempty"`
	ExplicitTestFiles    []string          `yaml:"explicit_test_files,omitempty" json:"explicit_test_files,omitempty"`
	ExplicitSupportFiles []string          `yaml:"explicit_support_files,omitempty" json:"explicit_support_files,omitempty"`
}

// CandidateHash computes a deterministic SHA-256 hash of the candidate specification.
func (c CandidateSpec) CandidateHash() string {
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// AllowedCaseResult records the proof outcome of one legitimate near-miss case.
type AllowedCaseResult struct {
	Name       string            `json:"name,omitempty"`
	TargetPath string            `json:"target_path"`
	InScope    bool              `json:"in_scope"`
	Passed     bool              `json:"passed"`
	Findings   []finding.Finding `json:"findings,omitempty"`
	Detail     string            `json:"detail,omitempty"`
}

// VerificationReport is the durable, reproducible artifact returned by `flinch prove` (§5.3, §9).
type VerificationReport struct {
	Schema           int                 `json:"schema"`
	CandidateID      string              `json:"candidate_id"`
	CandidateVersion int                 `json:"candidate_version"`
	CandidateHash    string              `json:"candidate_hash"`
	Rung             reflex.Rung         `json:"rung"`
	Obligation       string              `json:"obligation"`
	Selector         string              `json:"selector,omitempty"`
	BrokenRef        string              `json:"broken_ref"`
	FixedRef         string              `json:"fixed_ref"`
	Runner           string              `json:"runner"`
	RunnerVersion    string              `json:"runner_version,omitempty"`
	Verdict          Verdict             `json:"verdict"`
	Reason           FailureReason       `json:"reason,omitempty"`
	ActionableError  string              `json:"actionable_error,omitempty"`
	ExpectedSignal   string              `json:"expected_signal,omitempty"`
	ObservedSignal   string              `json:"observed_signal,omitempty"`
	MatchedBroken    []finding.Finding   `json:"matched_broken,omitempty"`
	RemainingFixed   []finding.Finding   `json:"remaining_fixed,omitempty"`
	AllowedResults   []AllowedCaseResult `json:"allowed_results,omitempty"`
	Census           int                 `json:"census"`
	FixedRuntimeMS   int64               `json:"fixed_runtime_ms,omitempty"`
	BrokenRuntimeMS  int64               `json:"broken_runtime_ms,omitempty"`
	TotalRuntimeMS   int64               `json:"total_runtime_ms"`
	VerifiedAt       time.Time           `json:"verified_at"`
}

package check

import (
	"fmt"
	"time"
)

// TimeoutLimit values identify the budget that bounded a test execution.
const (
	LimitMaxTestRuntime = "max_test_runtime"
	LimitTurnDeadline   = "turn_deadline"
	LimitTestBudget     = "test_budget"
)

type turnTestBudgets struct {
	deadline   time.Duration
	testBudget time.Duration
	maxRuntime time.Duration
}

type turnTestLimit struct {
	remaining time.Duration
	total     time.Duration
	kind      string
}

func (b turnTestBudgets) limit(turnLeft, budgetLeft time.Duration) turnTestLimit {
	remaining := min(turnLeft, budgetLeft, b.maxRuntime)
	switch remaining {
	case turnLeft:
		return turnTestLimit{remaining, b.deadline, LimitTurnDeadline}
	case budgetLeft:
		return turnTestLimit{remaining, b.testBudget, LimitTestBudget}
	default:
		return turnTestLimit{remaining, b.maxRuntime, LimitMaxTestRuntime}
	}
}

func (l turnTestLimit) stopped(id string) SkippedControl {
	label := l.kind
	switch l.kind {
	case LimitTurnDeadline:
		label = "the turn deadline"
	case LimitTestBudget:
		label = "the logical test budget"
	}
	return SkippedControl{
		ReflexID:     id,
		TimeoutLimit: l.kind,
		Reason:       fmt.Sprintf("test stopped at %s (%s); deferred to CI", label, l.total),
	}
}

// RunTurn works on one phase at a time. Sum repeated runs before rounding.
type turnPhases struct {
	elapsed map[string]time.Duration
	active  string
	started time.Time
}

func (p *turnPhases) start(phase string) {
	p.active = phase
	p.started = time.Now()
}

func (p *turnPhases) finish() time.Duration {
	duration := time.Since(p.started)
	p.elapsed[p.active] += duration
	p.active = ""
	return duration
}

func (p *turnPhases) milliseconds() map[string]int64 {
	if p.active != "" {
		p.finish()
	}
	if len(p.elapsed) == 0 {
		return nil
	}
	ms := make(map[string]int64, len(p.elapsed))
	for phase, duration := range p.elapsed {
		ms[phase] = duration.Milliseconds()
	}
	return ms
}

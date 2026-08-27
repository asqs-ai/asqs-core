package retrieval

import "testing"

// Churn weight ships at ZERO, and the term is then ABSENT — not merely scaled to nothing.
//
// This is the repository's standing rule for ranking defaults: a term earns its value through a
// measured comparison (CP16), never through a plausible story. Defaulting it on would change every
// plan in every deployment on the strength of an argument nobody has tested, and the change would be
// invisible — plans would just come out differently.
//
// Weight 0 also means no query: the churn map is fetched once per plan and only when the weight is
// positive, so an operator who has not opted in pays nothing for the feature.
func TestChurnWeightDefaultsToZero(t *testing.T) {
	var opts PlanOptions
	if opts.ChurnWeight != 0 {
		t.Errorf("ChurnWeight default = %d, want 0; a ranking term must earn a nonzero default "+
			"through a measured comparison", opts.ChurnWeight)
	}
	// There is deliberately no config key either: the knob arrives with the measurement that
	// justifies a value for it, not before.
}

// churnCap bounds one symbol's contribution so a single hot file cannot monopolize the plan. The
// window is what makes "recently" mean something.
func TestChurnConstantsAreBounded(t *testing.T) {
	if churnCap <= 0 {
		t.Errorf("churnCap = %d; an unbounded churn term lets one hot file take every slot", churnCap)
	}
	if churnWindow <= 0 {
		t.Errorf("churnWindow = %v; without a window, churn measures all history equally and never decays", churnWindow)
	}
}

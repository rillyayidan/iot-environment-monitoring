package alert

import "testing"

func TestPolicy(t *testing.T) {
	for _, test := range []struct {
		operator  string
		threshold float64
		recovery  float64
		trigger   float64
		gap       float64
	}{
		{"gt", 1000, 900, 1001, 1000},
		{"gte", 1000, 900, 1000, 999},
		{"lt", 10, 20, 9, 10},
		{"lte", 10, 20, 10, 11},
	} {
		t.Run(test.operator, func(t *testing.T) {
			policy := Policy{Operator: test.operator, Threshold: test.threshold, Recovery: test.recovery, Severity: "warning"}
			if err := policy.validate(); err != nil {
				t.Fatal(err)
			}
			if !policy.Triggers(test.trigger) || !policy.Recovers(test.recovery) || policy.Triggers(test.gap) || policy.Recovers(test.gap) {
				t.Fatal("incorrect trigger, hysteresis or recovery boundary")
			}
			policy.Recovery = test.trigger
			if policy.validate() == nil {
				t.Fatal("overlapping trigger and recovery ranges were accepted")
			}
		})
	}
}

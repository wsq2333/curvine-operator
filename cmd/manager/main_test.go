package main

import (
	"github.com/wsq2333/curvine-operator/internal/curvine/v054"
	"github.com/wsq2333/curvine-operator/internal/observe"
	"github.com/wsq2333/curvine-operator/internal/workload"
	"testing"
)

func TestObserverSelection(t *testing.T) {
	for _, tc := range []struct {
		runtime, observer string
		release, fail     bool
	}{
		{"", "", false, false}, {workload.Contract, "", false, false}, {v054.Contract, "", true, false}, {"", v054.Contract, true, false}, {v054.Contract, v054.Contract, true, false}, {workload.Contract, v054.Contract, false, true}, {"", "other", false, true}, {"other", "", false, true},
	} {
		o, err := selectObserver(tc.runtime, tc.observer)
		if (err != nil) != tc.fail {
			t.Fatalf("%+v err=%v", tc, err)
		}
		if tc.fail {
			continue
		}
		_, release := o.(*v054.Observer)
		_, unsupported := o.(observe.Unsupported)
		if release != tc.release || (!release && !unsupported) {
			t.Fatalf("wrong observer for %+v", tc)
		}
	}
}

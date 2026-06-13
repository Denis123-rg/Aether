package main

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestABProvisionalCreditMetric(t *testing.T) {
	before := testutil.ToFloat64(abSelectorCreditsTotal.WithLabelValues("provisional", "titan"))
	recordABProvisionalCredit("titan")
	after := testutil.ToFloat64(abSelectorCreditsTotal.WithLabelValues("provisional", "titan"))
	if after != before+1 {
		t.Fatalf("before=%v after=%v", before, after)
	}
}

func TestABCorrectionMetric(t *testing.T) {
	before := testutil.ToFloat64(abSelectorCorrectionsTotal.WithLabelValues("flashbots"))
	recordABCorrection("flashbots")
	after := testutil.ToFloat64(abSelectorCorrectionsTotal.WithLabelValues("flashbots"))
	if after != before+1 {
		t.Fatalf("before=%v after=%v", before, after)
	}
}

func TestABMetricLabels(t *testing.T) {
	recordABProvisionalCredit("eden")
	v := testutil.ToFloat64(abSelectorCreditsTotal.WithLabelValues("provisional", "eden"))
	if v < 1 {
		t.Fatal("eden label not set")
	}
}

func TestABNoCorrectionOnMatch(t *testing.T) {
	// Provisional credit without correction — both metrics independent.
	before := testutil.ToFloat64(abSelectorCorrectionsTotal.WithLabelValues("titan"))
	recordABProvisionalCredit("titan")
	after := testutil.ToFloat64(abSelectorCorrectionsTotal.WithLabelValues("titan"))
	if after != before {
		t.Fatal("correction should not increment on provisional only")
	}
}

func TestABCorrectionIncrementsOnReconcile(t *testing.T) {
	recordABCorrection("rsync")
	if testutil.ToFloat64(abSelectorCorrectionsTotal.WithLabelValues("rsync")) < 1 {
		t.Fatal("expected correction")
	}
}

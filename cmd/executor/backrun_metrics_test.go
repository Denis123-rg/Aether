package main

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordBackrunRevert_IncrementsCounter(t *testing.T) {
	before := testutil.ToFloat64(backrunRevertCount)
	recordBackrunRevert()
	after := testutil.ToFloat64(backrunRevertCount)
	if after != before+1 {
		t.Fatalf("counter before=%v after=%v", before, after)
	}
}

func TestRecordBackrunShadow_AllSources(t *testing.T) {
	for _, src := range []string{SourceBlockDriven, SourceMempoolBackrun, "custom"} {
		before := testutil.ToFloat64(backrunShadowTotal.WithLabelValues(src))
		recordBackrunShadow(src)
		after := testutil.ToFloat64(backrunShadowTotal.WithLabelValues(src))
		if after != before+1 {
			t.Fatalf("source %s before=%v after=%v", src, before, after)
		}
	}
}

func TestRecordBackrunLive_AllSources(t *testing.T) {
	for _, src := range []string{SourceBlockDriven, SourceMempoolBackrun} {
		before := testutil.ToFloat64(backrunLiveTotal.WithLabelValues(src))
		recordBackrunLive(src)
		after := testutil.ToFloat64(backrunLiveTotal.WithLabelValues(src))
		if after != before+1 {
			t.Fatalf("source %s before=%v after=%v", src, before, after)
		}
	}
}

func TestRecordBackrunPromoted_Increments(t *testing.T) {
	before := testutil.ToFloat64(backrunPromotedTotal)
	recordBackrunPromoted()
	after := testutil.ToFloat64(backrunPromotedTotal)
	if after != before+1 {
		t.Fatalf("before=%v after=%v", before, after)
	}
}

func TestRecordABProvisionalCredit_BuilderLabel(t *testing.T) {
	builder := "flashbots"
	before := testutil.ToFloat64(abSelectorCreditsTotal.WithLabelValues("provisional", builder))
	recordABProvisionalCredit(builder)
	after := testutil.ToFloat64(abSelectorCreditsTotal.WithLabelValues("provisional", builder))
	if after != before+1 {
		t.Fatalf("before=%v after=%v", before, after)
	}
}

func TestRecordABCorrection_BuilderLabel(t *testing.T) {
	builder := "titan"
	before := testutil.ToFloat64(abSelectorCorrectionsTotal.WithLabelValues(builder))
	recordABCorrection(builder)
	after := testutil.ToFloat64(abSelectorCorrectionsTotal.WithLabelValues(builder))
	if after != before+1 {
		t.Fatalf("before=%v after=%v", before, after)
	}
}

func TestRecordBackrunRevert_MultipleCalls(t *testing.T) {
	before := testutil.ToFloat64(backrunRevertCount)
	recordBackrunRevert()
	recordBackrunRevert()
	after := testutil.ToFloat64(backrunRevertCount)
	if after != before+2 {
		t.Fatalf("expected +2, before=%v after=%v", before, after)
	}
}

func TestRecordBackrunShadow_EmptySource(t *testing.T) {
	before := testutil.ToFloat64(backrunShadowTotal.WithLabelValues(""))
	recordBackrunShadow("")
	after := testutil.ToFloat64(backrunShadowTotal.WithLabelValues(""))
	if after != before+1 {
		t.Fatalf("before=%v after=%v", before, after)
	}
}

func TestRecordBackrunLive_BlockDriven(t *testing.T) {
	before := testutil.ToFloat64(backrunLiveTotal.WithLabelValues(SourceBlockDriven))
	recordBackrunLive(SourceBlockDriven)
	after := testutil.ToFloat64(backrunLiveTotal.WithLabelValues(SourceBlockDriven))
	if after != before+1 {
		t.Fatalf("before=%v after=%v", before, after)
	}
}

func TestRecordABProvisionalCredit_MultipleBuilders(t *testing.T) {
	for _, b := range []string{"eden", "rsync", "builderx"} {
		recordABProvisionalCredit(b)
		if testutil.ToFloat64(abSelectorCreditsTotal.WithLabelValues("provisional", b)) < 1 {
			t.Fatalf("builder %s not credited", b)
		}
	}
}

package main

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	backrunShadowTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "aether_executor_backrun_shadow_total",
		Help: "Mempool backrun bundles processed in shadow (logged, not submitted)",
	}, []string{"source"})
	backrunLiveTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "aether_executor_backrun_live_total",
		Help: "Mempool backrun bundles submitted live to builders",
	}, []string{"source"})
	backrunRevertCount = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "aether_executor_backrun_revert_count",
		Help: "Mempool backrun bundles that reverted on-chain",
	})
	backrunPromotedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "aether_executor_backrun_promoted_total",
		Help: "Times mempool backrun mode was promoted to live_only",
	})
	abSelectorCreditsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "aether_executor_ab_selector_credits_total",
		Help: "Provisional profit credits to builders at submit ACK time",
	}, []string{"status", "builder"})
	abSelectorCorrectionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "aether_executor_ab_selector_corrections_total",
		Help: "Attribution corrections after inclusion poll reconciliation",
	}, []string{"builder"})
)

func init() {
	prometheus.MustRegister(
		backrunShadowTotal,
		backrunLiveTotal,
		backrunRevertCount,
		backrunPromotedTotal,
		abSelectorCreditsTotal,
		abSelectorCorrectionsTotal,
	)
	for _, s := range []string{SourceBlockDriven, SourceMempoolBackrun} {
		backrunShadowTotal.WithLabelValues(s)
		backrunLiveTotal.WithLabelValues(s)
	}
}

func recordBackrunShadow(source string) {
	backrunShadowTotal.WithLabelValues(source).Inc()
}

func recordBackrunLive(source string) {
	backrunLiveTotal.WithLabelValues(source).Inc()
}

func recordBackrunRevert() {
	backrunRevertCount.Inc()
}

func recordBackrunPromoted() {
	backrunPromotedTotal.Inc()
}

func recordABProvisionalCredit(builder string) {
	abSelectorCreditsTotal.WithLabelValues("provisional", builder).Inc()
}

func recordABCorrection(builder string) {
	abSelectorCorrectionsTotal.WithLabelValues(builder).Inc()
}

// Package strategy implements deterministic A/B builder selection for the
// executor's bundle-submission path.
//
// There is NO machine learning here by design. Builder routing is a classic
// explore/exploit problem solved with fixed, auditable arithmetic: we track
// the realized outcome of every bundle we route to each block builder
// (Flashbots, Titan, Eden, …), score each builder by its expected value per
// attempt, and shift volume toward the best performer while reserving a fixed
// exploration floor so an unlucky-but-viable builder is never starved to zero
// (which would also starve us of fresh signal).
//
// The selector is intentionally a pure, side-effect-free component: it owns no
// network, no clock, and no persistence. The executor records outcomes into it
// and reads back an allocation/ranking; wiring lives in cmd/executor. Keeping
// it pure makes the policy fully unit-testable and keeps the hot path free of
// surprises.
package strategy

import (
	"math/big"
	"sort"
	"sync"
)

// weiPerEth is the wei→ETH divisor used to keep scores in human-scale ETH
// units. Float64 here is fine: scores are a *ranking* signal, not an accounting
// value, and the ledger (NUMERIC(78,0)) remains the source of truth for money.
var weiPerEth = new(big.Float).SetFloat64(1e18)

// Outcome is the realized result of a single bundle submission to one builder.
//
// ProfitWei is the net profit attributed to the attempt: positive when the
// bundle landed profitably, zero for a miss. A nil ProfitWei is treated as
// zero so callers can record a miss with `Outcome{Included: false}` and omit
// the field entirely.
type Outcome struct {
	Included  bool
	ProfitWei *big.Int
}

// BuilderStats is an exported, copy-safe snapshot of one builder's running
// totals. Returned by Snapshot for dashboards and the Telegram /pools view.
type BuilderStats struct {
	Builder    string
	Attempts   int64
	Inclusions int64
	// ProfitWei is a defensive copy — callers may mutate it freely.
	ProfitWei *big.Int
	// WinRate is inclusions/attempts WITHOUT the smoothing prior, i.e. the raw
	// observed rate. Reported for operators; the ranking uses the smoothed rate.
	WinRate float64
	// ScoreEthPerAttempt is the expected value (in ETH) of routing one bundle
	// to this builder, the primary ranking key.
	ScoreEthPerAttempt float64
	// Allocation is the fraction of next-window volume this builder receives,
	// in [0,1]; the map returned by Snapshot sums to ~1 across enabled builders.
	Allocation float64
}

type builderState struct {
	attempts   int64
	inclusions int64
	profitWei  *big.Int // cumulative realized net profit
}

// Config tunes the explore/exploit trade-off. Zero values are replaced with
// safe defaults in New, so `New(builders, Config{})` is valid.
type Config struct {
	// ExplorationFloor is the total fraction of volume (in [0,1)) split evenly
	// across all builders regardless of performance. The remaining (1-floor) is
	// allocated proportionally to score. A floor of 0.15 across 3 builders
	// guarantees each gets at least 5% of traffic. Must be < 1.
	ExplorationFloor float64
	// PriorAttempts seeds a smoothing prior on the denominator of the
	// expected-value score: a builder that won its single 1-ETH attempt scores
	// 1/(1+PriorAttempts) ETH/attempt, not a full 1 ETH, so it cannot leapfrog
	// a builder with a long, slightly-lower-mean track record. Defaults to a
	// weak prior of 1 — enough to damp tiny samples without materially biasing
	// a builder that already has real history.
	PriorAttempts float64
}

const (
	defaultExplorationFloor = 0.15
	defaultPriorAttempts    = 1.0
)

// Selector is a thread-safe A/B builder selector. Construct with New; safe for
// concurrent Record / read calls from the executor's submission goroutines.
type Selector struct {
	mu    sync.RWMutex
	order []string // builders in stable, configured order (ranking tie-break)
	stats map[string]*builderState
	cfg   Config
}

// New creates a Selector over the given builder names. Duplicate and empty
// names are dropped; the surviving order is preserved as the deterministic
// tie-break for equal scores. Config zero values fall back to defaults.
func New(builders []string, cfg Config) *Selector {
	if cfg.ExplorationFloor <= 0 || cfg.ExplorationFloor >= 1 {
		cfg.ExplorationFloor = defaultExplorationFloor
	}
	if cfg.PriorAttempts <= 0 {
		cfg.PriorAttempts = defaultPriorAttempts
	}

	s := &Selector{
		stats: make(map[string]*builderState),
		cfg:   cfg,
	}
	seen := make(map[string]struct{}, len(builders))
	for _, b := range builders {
		if b == "" {
			continue
		}
		if _, dup := seen[b]; dup {
			continue
		}
		seen[b] = struct{}{}
		s.order = append(s.order, b)
		s.stats[b] = &builderState{profitWei: new(big.Int)}
	}
	return s
}

// Record folds one submission outcome into the named builder's running totals.
// Unknown builders are ignored (the builder set is fixed at construction) so a
// stray result from a decommissioned builder cannot resurrect it.
func (s *Selector) Record(builder string, o Outcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.stats[builder]
	if !ok {
		return
	}
	st.attempts++
	if o.Included {
		st.inclusions++
	}
	if o.ProfitWei != nil && o.ProfitWei.Sign() != 0 {
		st.profitWei.Add(st.profitWei, o.ProfitWei)
	}
}

// score returns the expected ETH profit per attempt for a builder, applying
// the smoothing prior to the attempt count so sparse samples regress toward a
// conservative estimate. Callers must hold at least a read lock.
func (s *Selector) score(st *builderState) float64 {
	if st == nil {
		return 0
	}
	smoothedAttempts := float64(st.attempts) + s.cfg.PriorAttempts
	if smoothedAttempts <= 0 {
		return 0
	}
	profitEth, _ := new(big.Float).Quo(new(big.Float).SetInt(st.profitWei), weiPerEth).Float64()
	return profitEth / smoothedAttempts
}

// rawWinRate is the unsmoothed inclusions/attempts; 0 when no attempts yet.
func rawWinRate(st *builderState) float64 {
	if st == nil || st.attempts == 0 {
		return 0
	}
	return float64(st.inclusions) / float64(st.attempts)
}

// Scores returns the current expected-ETH-per-attempt score for every builder.
func (s *Selector) Scores() map[string]float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]float64, len(s.order))
	for _, b := range s.order {
		out[b] = s.score(s.stats[b])
	}
	return out
}

// Rank returns builders ordered best-first by score. Ties break first on the
// raw win-rate, then on the original (configured) order so the result is fully
// deterministic and stable across calls.
func (s *Selector) Rank() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rankLocked()
}

func (s *Selector) rankLocked() []string {
	ranked := make([]string, len(s.order))
	copy(ranked, s.order)
	idx := make(map[string]int, len(s.order))
	for i, b := range s.order {
		idx[b] = i
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		bi, bj := ranked[i], ranked[j]
		si, sj := s.score(s.stats[bi]), s.score(s.stats[bj])
		if si != sj {
			return si > sj
		}
		wi, wj := rawWinRate(s.stats[bi]), rawWinRate(s.stats[bj])
		if wi != wj {
			return wi > wj
		}
		return idx[bi] < idx[bj]
	})
	return ranked
}

// Best returns the single highest-scoring builder, or "" when the selector has
// no builders configured.
func (s *Selector) Best() string {
	r := s.Rank()
	if len(r) == 0 {
		return ""
	}
	return r[0]
}

// Allocation returns the fraction of next-window submission volume each builder
// should receive, summing to 1 across all builders. Every builder is guaranteed
// at least ExplorationFloor/N (the exploration reserve, split evenly); the
// remaining (1-floor) is divided in proportion to non-negative score. When no
// builder has positive score yet (cold start) the split is uniform.
func (s *Selector) Allocation() map[string]float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.allocationLocked()
}

// Snapshot returns a per-builder view of the current state for dashboards and
// operator commands. The returned ProfitWei values are defensive copies.
func (s *Selector) Snapshot() map[string]BuilderStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	alloc := s.allocationLocked()
	out := make(map[string]BuilderStats, len(s.order))
	for _, b := range s.order {
		st := s.stats[b]
		out[b] = BuilderStats{
			Builder:            b,
			Attempts:           st.attempts,
			Inclusions:         st.inclusions,
			ProfitWei:          new(big.Int).Set(st.profitWei),
			WinRate:            rawWinRate(st),
			ScoreEthPerAttempt: s.score(st),
			Allocation:         alloc[b],
		}
	}
	return out
}

// allocationLocked is the shared allocation math used by both Allocation and
// Snapshot; callers must already hold at least a read lock. Splitting it out
// keeps the two public readers from double-locking.
func (s *Selector) allocationLocked() map[string]float64 {
	n := len(s.order)
	out := make(map[string]float64, n)
	if n == 0 {
		return out
	}
	floorEach := s.cfg.ExplorationFloor / float64(n)
	var sumPos float64
	pos := make(map[string]float64, n)
	for _, b := range s.order {
		sc := s.score(s.stats[b])
		if sc > 0 {
			pos[b] = sc
			sumPos += sc
		}
	}
	if sumPos == 0 {
		for _, b := range s.order {
			out[b] = 1.0 / float64(n)
		}
		return out
	}
	remaining := 1.0 - s.cfg.ExplorationFloor
	for _, b := range s.order {
		out[b] = floorEach + remaining*(pos[b]/sumPos)
	}
	return out
}

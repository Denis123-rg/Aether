# Aether — Transaction Ordering Strategy Research Report

**Audience:** Aether searcher team
**Question:** How should we position our transactions relative to victim transactions, and how do we land bundles in the right slot of builder blocks?
**Scope:** Backrun, Frontrun, Sandwich (Classes 1-3). JIT-LP and CEX-DEX briefly noted.
**Date of synthesis:** May 2026

---

## 1. Backrun (`[victim_tx, our_arb, our_tip]`)

### 1.1 Economics

**Expected EV per opportunity.** Atomic arbitrage on Ethereum L1 in 2025 is a mature, oligopolistic market. The Extropy / HackMD survey of 2024–2025 arbitrage describes "fewer than 20 core entities" capturing the bulk of L1 arb, with per-opportunity gross profits ranging from low-single-digit USD to outlier days in the tens of thousands. The well-cited figure from Blockworks/EigenPhi — "$2.65M extracted across 59 blocks" during a volatility episode — is illustrative of the spread: typical median backrun ~$5–$50 net, with a fat tail driven by liquidations, large CoW-style fills, and stablecoin depeg events.

**Hit rate.** A "detected" opportunity in Aether's graph is not the same as a profitable inclusion. Realistic conversion stages (estimate based on Flashbots Hindsight repo and public searcher post-mortems):
- Detection → passes revm sim: ~30–50% (the rest are stale, slippage-killed, or path-not-realisable on-chain)
- Sim pass → bundle submitted: ~95%
- Submitted → included on-chain: 5–25% for top-of-block sensitive backruns; higher (40%+) for "any-position-in-block" backruns where another tx in the same block doesn't preempt the path
- Included → net positive after tip: 60–80% (the rest are won at a tip that ate the margin)

End-to-end **detected-to-profitable-inclusion conversion: ~1–5%** is a reasonable working number for an Aether-class operator. Top-quartile searchers reportedly push the included-when-submitted rate above 30% via private-orderflow integrations (Flashbots MEV-Share, BuilderNet, MEV Blocker hints).

**Capital model.** Backruns are almost entirely flashloan-funded today.
- **Aave V3:** 0.05% (5bps) on the borrowed amount, reduced from 9bps in V2. Source: Aave docs / governance forum.
- **Balancer V2 Vault:** 0 bps. The Flashbots tutorial explicitly recommends Balancer for backrun-arb to avoid the Aave fee. Caveat: the Vault must hold the token you need; depth varies by asset.
- **Morpho:** No standardised flashloan primitive across markets; case-by-case via Morpho Blue callbacks. Not a meaningful flashloan source today (estimate based on available 2025 docs).
- **Uniswap V3 flash callbacks:** Free aside from gas, but constrained to tokens in a specific pool.

For Aether: **default to Balancer Vault for any token in its set; fall back to Aave V3 for long-tail tokens.** Skip Morpho until/unless a dedicated flash market emerges.

**Tip share at equilibrium.** Empirical 2024–2025 data (Eigenphi, MDPI/arxiv "From Competition to Centralization"): top builders pay validators 85–90% of bid value, and within the searcher↔builder layer, **searchers pay >90% of revenue to the proposer/builder stack on competitive backruns.** For atomic backruns where 2+ searchers see the same opportunity in the public mempool, equilibrium tip routinely hits **97–99% of gross profit**. The realistic searcher take is the last 1–3% — which is why volume × hit-rate matter more than per-trade margin. For private/MEV-Share backruns where the searcher is the only one with the hint, equilibrium drops to ~50–70%.

**Top-quartile monthly revenue.** Cross-referencing the Eigenphi data ("$30M, 72% of Searchers' MEV Revenue Went to Validators in 2 Months"), Frontier.tech's "Builder Dominance and Searcher Dependence", and the Extropy 2025 analysis: a top-quartile L1 backrunner takes home **~$200k–$1M/month net** depending on the month's volatility regime. Aether-band (advertised as $3–10M/yr revenue) sits in that bracket; bear in mind ~$180M/mo total Ethereum MEV revenue (Extropy) gets divided across ~20 core operators after the 90% builder share.

### 1.2 Inclusion mechanics

**Builder acceptance.** Every major builder accepts backrun bundles unconditionally — they are universally regarded as non-extractive and pro-user.
- **BuilderNet** (Flashbots + Beaverbuild + Nethermind, since Dec 2024; v1.2 Feb 2025): fully accepts. The BuilderNet refund-by-marginal-contribution model is *favourable* to backruns because their attribution is clean.
- **Titan:** explicit `eth_sendBundle` API with `refundPercent` (0–99%). Their docs state ordering is not pure effective-gas-price — bundles compete on contribution to total block value via parallel merging algorithms. Ultra-low-priority queue deprioritises searchers with chronically low inclusion rates.
- **Beaverbuild:** part of BuilderNet; also runs MEV Blocker (with CoW DAO + Agnostic Relay).
- **Rsync (~1% market share):** accepts. Mostly used as a fallback / tip surface.
- **BuildAI, Penguin, Manifold, Eden:** marginal share each; accept but inclusion expectancy is low.
- **Top concentration (April 2025):** Beaverbuild + Titan ≈ 90% of blocks (MDPI/arxiv).

**`eth_sendBundle` vs `mev_sendBundle`.** Backruns should use **both paths in parallel**:
- `eth_sendBundle` to builders directly for public-mempool victims — fan-out across Flashbots/Titan/Beaver/Rsync.
- `mev_sendBundle` to Flashbots MEV-Share for private-orderflow victims. MEV-Share Nodes "only accept backruns" (Flashbots docs) — this is exactly the strategy class Aether wants to plug into. Note the **Oct 20 2025 change: MEV-Share bundles can now only contain one backrun tx.**

**`revertingTxHashes`.** For pure backruns this is irrelevant — the victim is already on-chain or will be by definition. Use the `mev_sendBundle` `canRevert: false` flag on our own arb tx so a failing arb doesn't pollute the block.

**MEV-Share 90% refund.** This is the killer detail for backrun economics. Under MEV-Share, **90% of the bid value goes back to the originating user**, 10% to the validator/builder. If we bid $100 in a sandwich-style game, $90 leaves us; in MEV-Share we structure that $100 as the post-arb residual and effectively pay only $10 to the auction — *but* we are bidding against other searchers who saw the same hint. Equilibrium is competitive auction on the 10% remainder, so net to searcher is ~30–70% of gross (vs 1–3% in public-mempool backrun PGAs). **This is a strong economic argument to prioritise MEV-Share integration over raw public-mempool backrun racing.**

### 1.3 Detection-to-submit latency budget

**Total budget.** Once a victim tx is visible in the mempool, the *useful* window is bounded by when builders stop accepting bundles for the next slot. Blocknative's "Anatomy of a Slot" notes that bundle submission becomes risky in the last 1–2s of the 12s slot. Practically:
- If victim appears at t=0 in mempool, our bundle must reach Titan/Beaver by ~t=10s (relative to slot start) to make slot N+1. That's a comfortable budget *if* we caught the tx early.
- The hard floor is set by *other searchers*: top-quartile inter-searcher latency floor on backrun races is **estimated 5–20ms from mempool-see to builder-receive** (NY5 colo ↔ Frankfurt/Tokyo builder endpoints adds 30–60ms RTT, so locally fast searchers with US-East builder peering beat globally distributed ones).

**Aether's 15ms hot-path target** is competitive but not best-in-class. Allocation:
- WS/IPC decode + dispatch: 1ms
- Pool state update + dirty edge flag: 0.5ms
- Bellman-Ford on subgraph: 2–3ms
- Ternary search optimiser: 1–2ms
- revm simulation: 3–5ms (this is your biggest knob)
- gRPC handoff Rust→Go: 0.3ms (UDS)
- Bundle build + sign: 1–2ms
- Submit fan-out: 1–2ms local, 30–60ms WAN to builder
- **Total useful + network: ~50–80ms NY5 → Titan/Beaver.**

The race is fundamentally won by *who saw the victim tx first*, then by who has the lowest sim latency. NY5 colo with Reth IPC is at the latency floor; the work to do is shaving revm sim from 5ms → 1ms (state caching, code preloading, EthersDB → local snapshot).

### 1.4 Risk

- **Counter-frontrun:** Low. Another searcher seeing the same victim races us to backrun position — we lose the auction but our arb tx still works if included; the issue is they bid higher. Mitigated by MEV-Share private hints.
- **Bundle leakage:** Backrun bundles are low-leakage targets — there's no `our_open` for a builder to copy. The Peraire-Bueno case (DOJ trial Oct 2025, mistrial Nov 2025 — hung jury) involved relay-side bundle observation, not backrun-specific leakage. BuilderNet's TEE design (Intel TDX) explicitly addresses this.
- **Mempool visibility variance:** Most arb-eligible victim txs sit in the mempool 100ms–2s before inclusion (estimate based on Blocknative mempool archive characterisations). Heavily-tipped txs go sub-200ms; CEX-routed retail can sit for 4–8s.
- **Slippage on victim:** Our prediction depends on the victim's `minAmountOut`. If their tx reverts (insufficient out) or their amount-in is lower than we modelled, the post-state changes and our arb may net less or revert. revm sim against the *exact* victim calldata + current state catches >95% of these.
- **PGA risk:** Lower for backruns than sandwich — we are *not* fighting for top-of-block, only post-victim-tx position. Builders will fit us in as long as our tip clears the marginal block-value contribution.

### 1.5 Builder relationships

- All major relays (Flashbots, Ultrasound, Agnostic, Aestus, BloXroute Max-Profit, BloXroute Regulated) accept backrun bundles.
- BuilderNet's refund rule pays orderflow providers by marginal contribution — backrun bundles fit cleanly into this model.
- Non-censoring relays (Ultrasound, Agnostic, Aestus) and OFAC-compliant (Flashbots, BloXroute Regulated) all handle backruns identically.

### 1.6 Policy / brand

- **Reputation:** Net positive. Backruns are widely framed as "good MEV" — they recycle price discovery, give users their fair fill, and are the primary mechanism through which CoW Swap, 1inch Fusion, MEV Blocker generate user refunds. Brand exposure: zero.
- **Regulation:** No active enforcement targeting backrunners. Peraire-Bueno is the only MEV-related criminal prosecution to date, and that case targets relay-bundle manipulation, not strategy class. DOJ posture (Steptoe analysis) does not generalise to backrun searchers.
- **RPC ToS:** Alchemy MEV-Protection is *consumer-side* (they protect *their users*' txs from MEV); their searcher endpoints have no anti-backrun clause. Infura/MetaMask same. No public-mempool provider ToS prohibits backrunning at the searcher level.

---

## 2. Frontrun (`[our_swap, victim_tx, our_close, our_tip]`)

*Note: "pure frontrun" in this sense — buy ahead of a known buy, sell at the higher price the victim creates without sandwiching them on the close — is structurally rare in 2025. Once you've committed to the open leg, not closing is leaving money on the table; the close is typically itself the backrun. So "pure frontrun" in practice is sandwich-minus-the-close-discipline. Treating it that way below.*

### 2.1 Economics

**Expected EV.** Worse than sandwich (less control over price), worse than backrun (capital at risk on the open leg). The strategy is rarely run pure in 2025 — most "frontrun" classifications in Brontes / mev-inspect are actually sandwich variants.

**Hit rate.** Lower than backrun: requires the victim tx to actually land *behind* our open. If a builder doesn't honour our requested ordering, we are left holding inventory with no triggering swap. Estimate: ~40–60% included-and-monetisable when submitted as a bundle.

**Capital model.** Cannot be flashloan-only — flashloans require atomic close. A pure frontrun where the close is in a separate tx requires *working capital* equal to the open size. This is a structural disadvantage versus backrun and sandwich. Hybrid model: flashloan within bundle if the close is bundled (then it's a sandwich).

**Tip share.** Competitive with sandwich because the open leg occupies top-of-block, which is the most-bid surface. Equilibrium 85–95% (estimate, lower than sandwich because fewer searchers run pure frontrun).

**Monthly revenue.** Sparse data. Brontes classifies most "frontruns" as a sub-pattern of sandwich. A pure-frontrun specialist is **estimated $50k–$300k/month** at best, with much higher inventory risk than backrun.

### 2.2 Inclusion mechanics

- Same builder universe as sandwich (see §3.2), but the ordering requirement (`our_swap` before `victim_tx`) is identical to sandwich.
- `eth_sendBundle` only — MEV-Share does not accept frontruns.
- `revertingTxHashes` MUST include the victim hash if we're willing to land the open without the victim (rare — usually we want the bundle to drop entirely if victim isn't there, to avoid stuck inventory).
- No MEV-Share refund applies.

### 2.3 Latency

Tighter than backrun: we need to insert *ahead* of a tx that builders can already see in their own mempools. If we're not the first to spot the victim and submit, the builder orders us behind. Realistic budget: **30–50ms from mempool-see to bundle-on-builder** to credibly contest top-of-block.

### 2.4 Risk

- **Inventory risk:** Real. If the victim never lands, we own the open-leg position at unfavourable price. Closing later costs spread + gas + tax footprint.
- **Counter-frontrun:** High. Another searcher can sandwich us. This is why pure frontrun is rarely run.
- **Bundle leakage:** Higher than backrun because a builder copying our open-leg can profitably mimic. Documented historically (pre-PBS); modern TEE-based builders (BuilderNet) and relay-only flow reduce this materially, but **estimate non-zero on long-tail builders**.

### 2.5–2.6 Builders / policy / brand

Treat as identical to sandwich (§3.5–§3.6). All sandwich-filter risk applies — pure frontrun is read as "predatory" by every filter that classes sandwich as predatory.

**Verdict:** Pure frontrun is dominated by sandwich on every axis except policy-gating (sandwich is policy-gated for us; frontrun is not). Given that the close leg is mandatory to monetise the open, "pure frontrun" inside Aether's policy = "running sandwich without bundling the close, accepting inventory risk to avoid the sandwich label." This is strictly worse economics with the same brand exposure as sandwich (because Brontes/mev-inspect will classify many of these as sandwich anyway). **Do not pursue as a distinct class.**

---

## 3. Sandwich (`[our_open, victim_tx, our_close, our_tip]`)

**Reminder: policy-gated at the team level.** Below is descriptive analysis for completeness.

### 3.1 Economics

**Expected EV.** Dramatically down vs 2023–2024. EigenPhi via Cointelegraph and Phemex: **average net profit per sandwich attack ~$3 in 2025.** Monthly extraction fell from ~$10M (late 2024) → ~$2.5M (Oct 2025) while DEX volume rose from $65B (Q1) to >$100B (Q3) — i.e. sandwich efficiency collapsed.

Top of distribution: a single January 2025 sandwich generated >$800k; >95,000 sandwiches recorded Nov 2024 – Oct 2025; sandwiches were 51% of "MEV volume" but a small fraction of *net* profit.

**Hit rate.** Per EigenPhi: of ~100 distinct active sandwich bots in 2025, ~30% recorded net losses, ~33% operated at +/-$10 (effectively breakeven), and only 6 bots cleared >$10k total profit for the year. **The strategy is structurally underwater for the median operator.**

**Capital model.** Flashloan-compatible: open + close are atomic inside the bundle. Aave V3 0.05% or Balancer 0bps as in §1.

**Tip share.** Equilibrium 90–98% at top-of-block. The sandwich PGA is the most competitive part of the auction — searchers race tips up to the point where one decimal of margin separates winner from loser.

**Monthly revenue.** Top-quartile sandwich operator: ~$30k–$100k/month net in 2025 (back-solved from EigenPhi $260k/month total net profits across 100 bots). The aggregate is dominated by a handful of operators; the marginal bot loses money. This is **a worse economic profile than backrun** for an Aether-band operator.

### 3.2 Inclusion mechanics

**Builder acceptance — split.**
- **BuilderNet (Flashbots + Beaverbuild operators):** Accepts sandwich bundles in practice (Beaverbuild has historically been the most sandwich-tolerant top builder) but BuilderNet's published refund rules and TEE-based design reduce sandwich profitability by sharing orderflow across operators, which means the protected user's flow that you would have sandwiched is more likely to land via MEV Blocker / Protect path before you see it.
- **Titan:** Accepts. No documented "no sandwich" filter. Reorders by block value, which sandwiches contribute to via tip.
- **Rsync:** Accepts.
- **MEV-Share:** Refuses — backruns only. **Sandwich cannot use the MEV-Share refund mechanism.**
- **Public-mempool victims only:** sandwich requires the victim to be visible — meaning private-flow protocols (MEV Blocker, 1inch Fusion, CoW, Flashbots Protect) are out of reach. Private-channel share grew from 31.8% (Nov 2024) → 50.1% (Feb 2025), so **the sandwichable surface has halved in ~15 months and continues to shrink.**

**`eth_sendBundle` only.** Cannot be sent via `mev_sendBundle`.

**`revertingTxHashes`:** Must NOT include the victim — sandwich fails if victim reverts (no price impact = inventory stuck at adverse price). Must NOT include our own legs — they must both succeed atomically or revert the whole bundle.

### 3.3 Latency

**Tightest deadline of the three classes.** We need:
- Mempool-see → simulate → predict slippage → build 2-leg bundle → sign → submit, ahead of every other sandwich bot AND ahead of any backrunner that might disrupt our close. Realistic budget: **15–40ms from victim-tx-broadcast to bundle-at-builder** to contend for top-of-block.

Aether's 15ms hot path is borderline. Sub-10ms is achievable only with co-located Reth, kernel-bypass networking (DPDK/XDP), and ahead-of-time bundle templates.

### 3.4 Risk

- **Counter-frontrun by other sandwichers:** Constant. The PGA mid-block collapse happens when 3+ searchers spot the same victim; tips converge on gross profit until one searcher zeroes out.
- **Bundle leakage:** Highest risk class. A builder seeing the calldata for our open leg can simulate the close and decide to extract themselves. Beaver and Titan have publicly disclaimed this behaviour; private-orderflow leakage between builders has been documented as a research problem (EmergentMind: "Detecting private order flow leakage across builders"). BuilderNet TEE design addresses this *for participants*, but trust assumptions remain.
- **Victim slippage:** The victim's `minAmountOut` may cause their tx to revert if our open moves price too far; revm sim catches this but at the tip-equilibrium price, the safety margin is thin.
- **Private-flow erosion:** The single biggest structural risk. As MEV Blocker, CoW, 1inch Fusion, and Flashbots Protect coverage expand, sandwichable surface continues to shrink.

### 3.5 Builder relationships

- **Beaverbuild's stance:** Co-launched MEV Blocker with CoW DAO and Agnostic Relay — protects users from sandwich/frontrun. Beaver's own builder still includes sandwich bundles where MEV Blocker doesn't intercept the tx, but their public-facing brand is anti-sandwich-on-protected-flow. Estimate: this position will tighten further over 2026.
- **Trusted-builder lists / deplatforming:** No major builder has formally banned a sandwich searcher. Titan's "ultra-low priority queue" mechanism could in principle be used as a soft block, but the published criterion is low inclusion rate, not strategy class.
- **Relay filtering:** Censoring relays (Flashbots OFAC, BloXroute Regulated) filter OFAC addresses, not strategy class. Sandwich is not OFAC-flagged.

### 3.6 Policy / brand

- **Brand impact.** Substantial in 2026. Sandwich operators are publicly named in EigenPhi dashboards and Cointelegraph/Phemex coverage. For an institutional or VC-backed searcher, sandwich activity is a material reputation cost — it surfaces in any due-diligence cycle. Pure-extraction framing has hardened post-Peraire-Bueno coverage.
- **Regulation.** No US enforcement *yet* targets sandwich specifically; Peraire-Bueno mistrial (Nov 2025) suggests jury comprehension is the binding constraint, not statute. EU MiCA does not address MEV strategy class explicitly. **Best estimate:** sandwich is legal in 2026 across US/EU; the *risk* is regulatory shift, not current enforcement.
- **RPC ToS.** Alchemy/Infura prohibit sandwich *attacks against their users* (via their MEV-Protect product), not sandwich *as an activity by their searcher customers*. Customer-side, no ToS prohibition is documented today.

---

## 4. Out-of-scope strategies (brief)

- **JIT-LP for UniV3:** Distinct codepath — add `mint`+`burn` legs around victim Uniswap V3 swap, capture fees pro-rata over our concentrated range, withdraw. Requires V3 tick math, `permit2`/`NonfungiblePositionManager` integration, working capital (no flashloan path for liquidity provision today). Returns are generally lower-variance than sandwich and policy-clean. Estimate: $50k–$200k/month for a top-quartile operator. Worth a dedicated scoping note separately.
- **CEX-DEX:** Out of scope — requires CEX integration (Binance/Coinbase/OKX latency-sensitive APIs, inventory desks). Estimated to be the single highest-EV strategy class in 2025–2026 ($500k–$5M/month for top operators), but the build-out is 3–6 months of CEX-integration work and inventory capital >$5M.

---

## 5. Comparison table

| Axis | Backrun (1) | Frontrun (2) | Sandwich (3) |
|---|---|---|---|
| Monthly EV (top-quartile, net, USD) | $200k–$1M | $50k–$300k (low data) | $30k–$100k |
| Detected → profitable inclusion | 1–5% (public) / 10–30% (MEV-Share) | <5% | 1–3% |
| Capital required | Flashloan-only (0–5 bps) | Working capital required | Flashloan-only (0–5 bps) |
| Latency budget (mempool → builder) | 50–80ms | 30–50ms | 15–40ms (tightest) |
| Top-builder acceptance (Beaver, Titan, BuilderNet, Rsync) | 100% | 100% | ~100% (but private-flow shrinkage) |
| MEV-Share eligible | YES (90% refund) | NO | NO |
| Policy risk (US/EU 2026) | None | Low | Low-to-moderate |
| Brand risk | None | Moderate | High |
| Counter-frontrun exposure | Low | High | High |
| Bundle-leakage exposure | Low | High | Highest |
| Structural trend 2024 → 2026 | Stable / growing via MEV-Share | Declining | Declining sharply (-75% YoY net) |

---

## 6. Recommendation

**Prioritise backrun (Class 1), with MEV-Share v2 as the primary integration surface, and `eth_sendBundle` fan-out to Beaver/Titan/Rsync as the secondary surface.**

Reasoning:

1. **Economics.** Backrun is the only class where Aether's profile ($3–10M/yr revenue band) is consistent with the available data: top-quartile backrunners earn $200k–$1M/month net. Sandwich's median operator is unprofitable in 2025. Frontrun has no defensible niche outside sandwich.
2. **Policy.** Backrun has zero brand and regulatory exposure. Sandwich is policy-gated at the team level for valid reasons. Frontrun inherits sandwich's brand cost without the upside.
3. **Latency fit.** Aether's 15ms hot path is competitive for backrun, borderline for sandwich. Closing the gap to sub-10ms requires kernel-bypass networking and revm precompilation — investments that benefit backrun equally.
4. **MEV-Share 90% refund.** This is the dominant economic story in 2025 — it lets a private-orderflow backrunner take 30–70% of gross instead of the 1–3% available in public-mempool PGAs. Aether should treat MEV-Share `mev_sendBundle` integration as **P0**. The Oct 2025 rule change (single backrun per bundle) constrains bundle construction but does not change the strategy.
5. **BuilderNet alignment.** Beaverbuild + Flashbots + Nethermind's BuilderNet (>40% blocks Sept 2025) uses a marginal-contribution refund rule that pays backrun-style flow well. We are aligned with where the auction is going.

### Open questions that would change the answer

- **Q1: How real is BuilderNet's TEE attestation in practice?** If TEE guarantees hold, private-orderflow concentration in BuilderNet becomes the dominant venue and changes our routing weights. If TEE is bypassable (research is active), leakage risk on any private-channel strategy rises.
- **Q2: ePBS / EIP-7732 (Glamsterdam, end of 2025/early 2026).** Decouples execution from consensus, gives builders ~9s for execution validation. Net effect on backrun is probably *positive* (more deterministic builder selection, larger blocks). Net effect on sandwich is unclear but probably negative (longer validation window → more time for MEV-Share/refund routing).
- **Q3: EIP-7782 (6s slots) timeline.** Halves the per-slot opportunity window; doubles slot frequency. Strongly latency-favouring. NY5 colo with sub-10ms hot path widens its moat. If shipped in Glamsterdam, treat hot-path optimisation as P0.
- **Q4: How fast does private-flow share grow?** From 31.8% (Nov 2024) → 50.1% (Feb 2025). If trend extrapolates, public mempool shrinks to <20% of sandwichable flow by end of 2026 — collapsing sandwich economics further. Backrun economics are *robust* to this trend because MEV-Share routes the same flow back to backrunners.
- **Q5: Sorella Brontes adoption.** If Brontes becomes the canonical MEV classifier used by builders for filtering, the line between "atomic-arb" (good) and "sandwich" (filtered) hardens. Aether wants to stay clearly on the atomic-arb side of that line.
- **Q6: Solana / cross-chain competition for talent and capital.** Solana arb has lower latency floors (400ms slots) and higher gross EV per opportunity in 2025; some top operators have migrated. Stay-on-L1 thesis depends on Ethereum DEX volume growth holding.

### Concrete next steps for Aether

1. Wire `mev_sendBundle` (MEV-Share v2) into the Go executor alongside existing `eth_sendBundle` fan-out. Match the Oct 2025 single-backrun-per-bundle constraint.
2. Add Balancer Vault flashloan path in `AetherExecutor.sol`; keep Aave V3 as fallback. Saves 5 bps on every backrun where Balancer has depth.
3. Profile and shave revm simulation latency target from 5ms → 1ms. Pre-warm `CacheDB` with hot-pool state; eliminate `EthersDB` round-trips on the hot path via local Reth IPC.
4. Track per-builder inclusion rate as a Prometheus metric (already in spec). Tune fan-out weights weekly based on observed inclusion-given-submitted, not market share.
5. Defer sandwich and frontrun infrastructure. If the team revisits sandwich policy, the latency-floor and bundle-leakage investments built for competitive backrun translate directly.

---

**Sources:**

- [Flashbots Docs — JSON-RPC, MEV-Share specs, bundles](https://docs.flashbots.net/)
- [Flashbots MEV-Share refund mechanics](https://docs.flashbots.net/flashbots-protect/mev-refunds)
- [Flashbots — Network Anonymized Mempools / BuilderNet](https://writings.flashbots.net/network-anonymized-mempools)
- [BuilderNet introduction](https://buildernet.org/blog/introducing-buildernet)
- [Titan Builder Docs — block-building algorithm, eth_sendBundle, refundPercent](https://docs.titanbuilder.xyz/)
- [Sorella Labs Brontes — Atomic Arbitrage Inspector](https://book.brontes.xyz/mev_inspectors/atomic-arb.html)
- [Aave V3 Flash Loans](https://aave.com/docs/aave-v3/guides/flash-loans)
- [Flashbots — Flash Loan Basics (Balancer)](https://docs.flashbots.net/flashbots-mev-share/searchers/tutorials/flash-loan-arbitrage/flash-loan-basics)
- [EigenPhi via Cointelegraph — sandwich attacks waned in 2025](https://cointelegraph.com/research/exclusive-data-from-eigenphi-reveals-that-sandwich-attacks-on-ethereum-have-waned)
- [Phemex — Ethereum MEV profits drop to $3 per sandwich in 2025](https://phemex.com/news/article/ethereum-mev-profits-plummet-to-3-per-sandwich-attack-in-2025-42474)
- [EigenPhi — $30M, 72% of searcher MEV to validators](https://eigenphi.substack.com/p/30m-72-of-searchers-mev-revenue-went)
- [Frontier.tech — Builder Dominance and Searcher Dependence](https://frontier.tech/builder-dominance-and-searcher-dependence)
- [arxiv — From Competition to Centralization: Oligopoly in Ethereum Block Building Auctions](https://arxiv.org/html/2412.18074v2)
- [Extropy — Arbitrage Markets 2024-2025](https://academy.extropy.io/pages/articles/mev-crosschain-analysis-2025.html)
- [Blocknative — Anatomy of a Slot](https://www.blocknative.com/blog/anatomy-of-a-slot)
- [Blocknative — MEV Bundle Failure](https://www.blocknative.com/blog/mev-bundle-failure)
- [EIP-7732 (ePBS)](https://eips.ethereum.org/EIPS/eip-7732)
- [Titan Builder — Builders and Relays in ePBS](https://titanbuilder.substack.com/p/builders-and-relays-in-epbs)
- [EIP-7782 / 6-second slot proposal](https://www.coindesk.com/tech/2025/06/24/ethereum-developer-proposes-6-second-block-times-to-boost-speed-slash-fees)
- [arxiv — Sandwiched and Silent: Behavioral Adaptation and Private Channel Exploitation (Dec 2025)](https://arxiv.org/html/2512.17602v1)
- [DLNews — Peraire-Bueno mistrial Nov 2025](https://www.dlnews.com/articles/defi/mev-brothers-trial-ends-in-mistrial-after-jury-breaks-down/)
- [Steptoe — US v. Peraire-Bueno implications](https://www.steptoe.com/en/news-publications/the-mother-court-blog/from-mit-to-federal-trial-united-states-v-anton-peraire-bueno-and-james-peraire-bueno-and-its-implications-for-crypto-crime.html)
- [Alchemy — MEV Protection docs](https://www.alchemy.com/docs/reference/mev-protection)
- [Infura/MetaMask — MEV protection](https://support.metamask.io/develop/building-with-infura/general-knowledge/mev-protection-infura)
- [MEV-Watch — relay censorship status](https://www.mevwatch.info/)
- [EmergentMind — Detecting private order flow leakage across builders](https://www.emergentmind.com/open-problems/detect-private-order-flow-leakage-across-builders)
- [Awesome Block Builders list](https://github.com/blue-searcher/awesome-block-builders)
- [Flashbots Hindsight (MEV-Share backrun retroactive sim)](https://github.com/flashbots/hindsight)

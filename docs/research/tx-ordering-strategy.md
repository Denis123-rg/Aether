# Transaction Ordering Strategy — Research

Status: draft / research-in-progress
Owner: 0xfandom
Branch: `research/tx-ordering-strategy`

---

## Objective

Decide how Aether places its own arbitrage transactions relative to victim transactions in the mempool, and how those bundles are ordered inside builder blocks.

Output of this document: a written recommendation with concrete numbers per strategy class (expected EV, inclusion rate, capital required, risk profile, latency budget, policy posture). The recommendation feeds into the executor wiring (`cmd/executor/`) and `AetherExecutor.sol` calldata shape.

## Strategy classes under evaluation

### 1. Backrun (post-victim arb)

```
bundle = [victim_tx (mempool, unmodified), our_arb_tx, our_tip_tx]
```

- Victim included as-is. We arb the imbalance their swap creates.
- Zero negative externality to the victim — they get the price they signed for.
- Standard searcher posture. Compatible with MEV-Share / MEV-Boost / Flashbots Protect.

### 2. Frontrun (pre-victim swap)

```
bundle = [our_swap_tx, victim_tx, our_close_tx, our_tip_tx]
```

- We swap in the same direction as the victim, pushing price further before they execute. They get worse fill; we close for profit.
- Predatory. Legal in MEV. Antagonises retail. Damages reputation with builders that filter "harmful" bundles (Flashbots Protect explicitly blocks).
- Practically limited to public-mempool victims; private-flow victims (MEV Blocker / Protect) un-frontrunnable.

### 3. Sandwich (frontrun + backrun)

```
bundle = [our_open_tx, victim_tx, our_close_tx, our_tip_tx]
```

- Strongest cash extraction per victim. Most negative externality.
- **Policy-gated per issue #117.** Implementation requires team sign-off. Research-only here.

### 4. Just-in-time liquidity (JIT-LP, UniV3)

```
bundle = [our_addLiquidity_tx, victim_tx, our_removeLiquidity_tx, our_tip_tx]
```

- Provide concentrated UniV3 liquidity exactly over the victim's swap range, capture fees, withdraw.
- Different code path entirely from cycle arb. Out of scope here per #117.

### 5. CEX-DEX arbitrage

- DEX price moves; we react with offsetting Binance/Coinbase order. Out of scope today (no CEX integration).

---

## Research questions

For each strategy class:

### Economics

- Expected EV per opportunity (USD)
- Hit rate per detected opportunity
- Capital lock-up (flashloan-only vs working capital)
- Aave V3 flashloan premium impact (5 bps for USDC, more for exotic assets)
- Tip share that maximises P(inclusion) × profit (the auction equilibrium)

### Inclusion mechanics

- Which builders accept which bundle shape
- MEV-Boost vs direct-to-builder (Flashbots, Titan, Beaver, Eden, Rsync)
- Bundle ordering inside a block — who decides, how is it priced
- `revertingTxHashes` semantics — do we allow victim to fail?
- Refund mechanics (MEV-Share returns 90% to victim by default)

### Detection-to-submit latency

- How many ms do we have from "see pending tx" to "submit bundle that lands"
- Per-strategy hot path budget allocation
- Submit fan-out timing (parallel vs sequential)

### Risk

- Front-running risk by other searchers (counter-frontrun)
- Bundle leakage (a builder copies our calldata)
- Mempool visibility windows (~12s slot but variable)
- Slippage on the victim's own tx invalidating our prediction

### Builder relationships

- Each builder's stated ordering policy (priority-fee, first-come, mev-blocker, etc.)
- Builder-specific bundle endpoints
- Trusted-builder list (rsync excludes harmful ordering)
- MEV-Boost relay filtering (Ultrasound, BloXroute, Flashbots, Agnostic)

### Policy / brand

- Reputation impact of sandwich activity on Aether's relay relationships
- Public-mempool ToS implications (Alchemy filters certain bundle types)
- Regulatory posture (no current US precedent treating MEV as market manipulation)

---

## Deliverables (this branch)

1. `docs/research/tx-ordering-strategy.md` — this file, fully populated
2. `docs/research/builder-comparison.md` — per-builder ordering policy + endpoint matrix
3. `docs/research/sandwich-policy-decision.md` — go/no-go on sandwich implementation
4. `docs/decisions/tx-ordering-recommendation.md` — final decision document
5. Linked issues for any implementation work, scoped per strategy class

## Out of scope

- Implementation code (separate issues post-decision)
- Contract changes (separate audit-gated PR)
- MEV-Boost relay onboarding (separate infra workstream)
- CEX-DEX arbitrage (different code path entirely)

---

## Next actions

- [x] Branch created
- [ ] Research agent dispatched on the five questions above
- [ ] Per-builder policy matrix populated
- [ ] EV / hit-rate estimates from historical mempool data
- [ ] Latency budget per strategy
- [ ] Sandwich-class policy decision drafted
- [ ] Final recommendation drafted
- [ ] Decision merged via PR; implementation issues opened per strategy class

---

## Open questions placeholder

(Research agent fills below.)

### Backrun
TBD

### Frontrun
TBD

### Sandwich
TBD

### Builder matrix
TBD

### Numbers
TBD

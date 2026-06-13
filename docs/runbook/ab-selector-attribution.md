# A/B builder attribution runbook

## Behaviour

At submit time, the executor credits the **first builder that ACKs** a bundle
with provisional profit (`ab_selector_credits_total{status="provisional"}`).

The inclusion poll loop later reconciles on-chain truth. When the winning
builder differs from the provisional credit, it increments
`ab_selector_corrections_total{builder}`.

This is expected under fan-out routing but should stay below 1% of credits.

## Alert

`AetherABSelectorCorrectionRateHigh` fires when:

```
corrections_total / credits_total{provisional} > 0.01 over 1h
```

## Actions

1. Check routing mode in `builders.yaml` (`fanout` vs `select`).
2. Compare `ab_selector_corrections_total` by builder label in Grafana.
3. If corrections are systemic, prefer `select` mode or reduce fan-out builders.

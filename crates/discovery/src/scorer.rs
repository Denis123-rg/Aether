use crate::config::ScoringSettings;
use crate::types::PoolScoreInputs;

/// Compute the raw (unnormalised) pool score.
///
/// Formula: `sqrt(TVL) * volume_24h * (1 - fee) * (1 - slippage_estimate)`
pub fn raw_score(inputs: &PoolScoreInputs, settings: &ScoringSettings) -> f64 {
    let tvl = inputs.tvl_usd.max(0.0);
    let volume = inputs.volume_24h_usd.max(0.0);
    if tvl == 0.0 || volume == 0.0 {
        return 0.0;
    }

    let fee_factor = 1.0 - (inputs.fee_bps as f64 / 10_000.0);
    let slippage = inputs.slippage_estimate.clamp(0.0, 0.99);
    let slip_factor = 1.0 - slippage;

    (tvl.sqrt() * settings.tvl_weight)
        * (volume * settings.volume_weight)
        * fee_factor
        * slip_factor
}

/// Compute the normalised pool score in [0.0, 1.0] for discovery ranking.
///
/// Convenience wrapper over [`raw_score`] + [`normalise_score`].
pub fn compute_score(inputs: &PoolScoreInputs, settings: &ScoringSettings, max_raw: f64) -> f64 {
    normalise_score(raw_score(inputs, settings), max_raw)
}

/// Normalise a raw score into [0.0, 1.0] against the current cache maximum.
/// When `max_raw` is zero, returns 0.0.
pub fn normalise_score(raw: f64, max_raw: f64) -> f64 {
    if !raw.is_finite() || raw <= 0.0 {
        return 0.0;
    }
    if max_raw <= 0.0 || !max_raw.is_finite() {
        return 0.0;
    }
    (raw / max_raw).clamp(0.0, 1.0)
}

/// Default slippage estimate from config bps.
pub fn default_slippage_estimate(settings: &ScoringSettings) -> f64 {
    (settings.slippage_estimate_bps as f64 / 10_000.0).clamp(0.0, 0.99)
}

/// Estimate slippage for a Curve 2-coin stable pool (first-order proxy).
pub fn estimate_curve_slippage(
    balance_in: f64,
    balance_out: f64,
    amount_in_eth: f64,
    fee_bps: u32,
) -> f64 {
    if balance_in <= 0.0 || balance_out <= 0.0 || amount_in_eth <= 0.0 {
        return 1.0;
    }
    // Stable pools have lower slippage than CP-AMM at equal depth; scale down V2 estimate.
    estimate_v2_slippage(balance_in, balance_out, amount_in_eth, fee_bps) * 0.35
}

/// Estimate slippage for a Balancer weighted pool (50/50 default).
pub fn estimate_balancer_v3_slippage(
    balance_in: f64,
    balance_out: f64,
    amount_in_eth: f64,
    fee_bps: u32,
) -> f64 {
    if balance_in <= 0.0 || balance_out <= 0.0 || amount_in_eth <= 0.0 {
        return 1.0;
    }
    estimate_v2_slippage(balance_in, balance_out, amount_in_eth, fee_bps) * 0.85
}

/// Protocol-aware slippage estimate for discovery scoring.
pub fn estimate_protocol_slippage(
    protocol: aether_common::types::ProtocolType,
    balance_in: f64,
    balance_out: f64,
    amount_in_eth: f64,
    fee_bps: u32,
) -> f64 {
    match protocol {
        aether_common::types::ProtocolType::Curve => {
            estimate_curve_slippage(balance_in, balance_out, amount_in_eth, fee_bps)
        }
        aether_common::types::ProtocolType::BalancerV3 => {
            estimate_balancer_v3_slippage(balance_in, balance_out, amount_in_eth, fee_bps)
        }
        _ => estimate_v2_slippage(balance_in, balance_out, amount_in_eth, fee_bps),
    }
}

/// Estimate slippage for a constant-product pool given reserves and swap size.
pub fn estimate_v2_slippage(
    reserve_in: f64,
    reserve_out: f64,
    amount_in_eth: f64,
    fee_bps: u32,
) -> f64 {
    if reserve_in <= 0.0 || reserve_out <= 0.0 || amount_in_eth <= 0.0 {
        return 1.0;
    }
    let fee_factor = 1.0 - (fee_bps as f64 / 10_000.0);
    let amount_in = amount_in_eth * fee_factor;
    let spot = reserve_out / reserve_in;
    let amount_out = (amount_in * reserve_out) / (reserve_in + amount_in);
    let effective_rate = amount_out / amount_in;
    if spot <= 0.0 {
        return 1.0;
    }
    ((spot - effective_rate) / spot).clamp(0.0, 0.99)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn settings() -> ScoringSettings {
        ScoringSettings::default()
    }

    #[test]
    fn formula_basic() {
        let inputs = PoolScoreInputs {
            tvl_usd: 1_000_000.0,
            volume_24h_usd: 500_000.0,
            fee_bps: 30,
            slippage_estimate: 0.005,
        };
        let raw = raw_score(&inputs, &settings());
        assert!(raw > 0.0);
        // sqrt(1e6) * 5e5 * 0.997 * 0.995
        let expected = 1000.0 * 500_000.0 * 0.997 * 0.995;
        assert!((raw - expected).abs() < 1.0);
    }

    #[test]
    fn zero_tvl_returns_zero() {
        let inputs = PoolScoreInputs {
            tvl_usd: 0.0,
            volume_24h_usd: 100_000.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        assert_eq!(raw_score(&inputs, &settings()), 0.0);
    }

    #[test]
    fn zero_volume_returns_zero() {
        let inputs = PoolScoreInputs {
            tvl_usd: 100_000.0,
            volume_24h_usd: 0.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        assert_eq!(raw_score(&inputs, &settings()), 0.0);
    }

    #[test]
    fn normalise_at_max_is_one() {
        let raw = 1_000_000.0;
        assert_eq!(normalise_score(raw, raw), 1.0);
    }

    #[test]
    fn normalise_half() {
        assert!((normalise_score(50.0, 100.0) - 0.5).abs() < 1e-9);
    }

    #[test]
    fn normalise_zero_raw() {
        assert_eq!(normalise_score(0.0, 100.0), 0.0);
    }

    #[test]
    fn normalise_zero_max() {
        assert_eq!(normalise_score(10.0, 0.0), 0.0);
    }

    #[test]
    fn normalise_negative_clamped() {
        assert_eq!(normalise_score(-5.0, 100.0), 0.0);
    }

    #[test]
    fn normalise_above_max_clamped() {
        assert_eq!(normalise_score(200.0, 100.0), 1.0);
    }

    #[test]
    fn compute_score_matches_raw_and_normalise() {
        let inputs = PoolScoreInputs {
            tvl_usd: 1_000_000.0,
            volume_24h_usd: 500_000.0,
            fee_bps: 30,
            slippage_estimate: 0.005,
        };
        let raw = raw_score(&inputs, &settings());
        let via_wrapper = compute_score(&inputs, &settings(), raw);
        assert!((via_wrapper - 1.0).abs() < 1e-9);
    }

    #[test]
    fn compute_score_zero_when_max_zero() {
        let inputs = PoolScoreInputs {
            tvl_usd: 100.0,
            volume_24h_usd: 100.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        assert_eq!(compute_score(&inputs, &settings(), 0.0), 0.0);
    }

    #[test]
    fn higher_fee_lowers_score() {
        let base = PoolScoreInputs {
            tvl_usd: 1_000_000.0,
            volume_24h_usd: 100_000.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        let high_fee = PoolScoreInputs {
            fee_bps: 100,
            ..base
        };
        assert!(raw_score(&base, &settings()) > raw_score(&high_fee, &settings()));
    }

    #[test]
    fn higher_slippage_lowers_score() {
        let low_slip = PoolScoreInputs {
            tvl_usd: 500_000.0,
            volume_24h_usd: 50_000.0,
            fee_bps: 30,
            slippage_estimate: 0.001,
        };
        let high_slip = PoolScoreInputs {
            slippage_estimate: 0.1,
            ..low_slip
        };
        assert!(raw_score(&low_slip, &settings()) > raw_score(&high_slip, &settings()));
    }

    #[test]
    fn v2_slippage_increases_with_trade_size() {
        let small = estimate_v2_slippage(1000.0, 1000.0, 0.001, 30);
        let large = estimate_v2_slippage(1000.0, 1000.0, 1.0, 30);
        assert!(large > small);
    }

    #[test]
    fn default_slippage_from_bps() {
        assert!((default_slippage_estimate(&settings()) - 0.005).abs() < 1e-9);
    }

    #[test]
    fn curve_slippage_below_v2_at_equal_depth() {
        let curve = estimate_curve_slippage(1e6, 1e6, 0.5, 4);
        let v2 = estimate_v2_slippage(1e6, 1e6, 0.5, 4);
        assert!(curve < v2);
    }

    #[test]
    fn balancer_v3_slippage_positive() {
        assert!(estimate_balancer_v3_slippage(1e6, 1e6, 0.1, 10) > 0.0);
    }

    #[test]
    fn protocol_slippage_routes_curve_and_balancer_v3() {
        use aether_common::types::ProtocolType;
        assert!(estimate_protocol_slippage(ProtocolType::Curve, 1e6, 1e6, 0.1, 4) > 0.0);
        assert!(estimate_protocol_slippage(ProtocolType::BalancerV3, 1e6, 1e6, 0.1, 10) > 0.0);
    }
}

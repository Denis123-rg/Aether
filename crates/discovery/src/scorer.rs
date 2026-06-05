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
}

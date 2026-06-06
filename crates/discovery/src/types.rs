use std::time::{Duration, Instant};

use aether_common::types::ProtocolType;
use alloy::primitives::Address;
use serde::{Deserialize, Serialize};

/// Metadata for a discovered pool stored in the ranked cache.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct PoolInfo {
    pub address: Address,
    pub token0: Address,
    pub token1: Address,
    pub protocol: ProtocolType,
    pub fee_bps: u32,
    /// Normalised score in [0.0, 1.0].
    pub score: f64,
    pub tvl_usd: f64,
    pub volume_24h_usd: f64,
    pub slippage_estimate: f64,
    pub discovered_at: u64,
}

impl PoolInfo {
    pub fn pool_id(&self) -> aether_common::types::PoolId {
        aether_common::types::PoolId {
            address: self.address,
            protocol: self.protocol,
        }
    }
}

/// Raw inputs for the scoring formula before normalisation.
#[derive(Debug, Clone, Copy, Default)]
pub struct PoolScoreInputs {
    pub tvl_usd: f64,
    pub volume_24h_usd: f64,
    pub fee_bps: u32,
    pub slippage_estimate: f64,
}

/// Result of pool integrity validation.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ValidationResult {
    Valid,
    Invalid(String),
    LowLiquidity,
}

/// Events broadcast to subscribers when the discovery cache changes.
#[derive(Debug, Clone)]
pub enum DiscoveryEvent {
    PoolAdded(PoolInfo),
    PoolUpdated(PoolInfo),
    PoolPruned { address: Address, reason: String },
    CachePruned { removed: usize },
}

/// Internal cache entry with bookkeeping timestamps.
#[derive(Debug, Clone)]
pub(crate) struct CachedPool {
    pub info: PoolInfo,
    pub inserted_at: Instant,
    pub last_scored_at: Instant,
}

impl CachedPool {
    pub fn age(&self) -> Duration {
        self.inserted_at.elapsed()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use aether_common::types::ProtocolType;
    use alloy::primitives::address;

    fn sample_pool() -> PoolInfo {
        PoolInfo {
            address: address!("0x0000000000000000000000000000000000000001"),
            token0: address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            token1: address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            score: 0.75,
            tvl_usd: 1_000_000.0,
            volume_24h_usd: 50_000.0,
            slippage_estimate: 0.01,
            discovered_at: 18_000_000,
        }
    }

    #[test]
    fn pool_info_pool_id_matches_address_and_protocol() {
        let p = sample_pool();
        let id = p.pool_id();
        assert_eq!(id.address, p.address);
        assert_eq!(id.protocol, p.protocol);
    }

    #[test]
    fn pool_score_inputs_default_zero() {
        let inputs = PoolScoreInputs::default();
        assert_eq!(inputs.tvl_usd, 0.0);
        assert_eq!(inputs.volume_24h_usd, 0.0);
        assert_eq!(inputs.fee_bps, 0);
        assert_eq!(inputs.slippage_estimate, 0.0);
    }

    #[test]
    fn validation_result_equality() {
        assert_eq!(ValidationResult::Valid, ValidationResult::Valid);
        assert_eq!(
            ValidationResult::Invalid("x".into()),
            ValidationResult::Invalid("x".into())
        );
        assert_ne!(
            ValidationResult::Invalid("a".into()),
            ValidationResult::Invalid("b".into())
        );
    }

    #[test]
    fn cached_pool_age_is_non_negative() {
        let cached = CachedPool {
            info: sample_pool(),
            inserted_at: Instant::now(),
            last_scored_at: Instant::now(),
        };
        assert!(cached.age() >= Duration::ZERO);
    }

    #[test]
    fn discovery_event_variants_construct() {
        let p = sample_pool();
        let _ = DiscoveryEvent::PoolAdded(p.clone());
        let _ = DiscoveryEvent::PoolUpdated(p);
        let _ = DiscoveryEvent::PoolPruned {
            address: address!("000000000000000000000000000000000000dEaD"),
            reason: "stale".into(),
        };
        let _ = DiscoveryEvent::CachePruned { removed: 3 };
    }
}

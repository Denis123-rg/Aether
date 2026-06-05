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

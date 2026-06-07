use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

use alloy::primitives::Address;
use dashmap::DashMap;
use tokio::sync::broadcast;

use crate::scorer::{normalise_score, raw_score};
use crate::types::{CachedPool, DiscoveryEvent, PoolInfo, PoolScoreInputs};
use crate::config::{DiscoverySettings, ScoringSettings};

/// Thread-safe ranked pool cache backed by `DashMap`.
pub struct DiscoveryCache {
    pools: DashMap<Address, CachedPool>,
    max_pools: usize,
    prune_max_age: Duration,
    scoring: ScoringSettings,
    event_tx: broadcast::Sender<DiscoveryEvent>,
    /// Monotonic counter for discovered_at block surrogate in tests.
    discovery_counter: AtomicU64,
}

impl DiscoveryCache {
    pub fn new(
        settings: &DiscoverySettings,
        scoring: ScoringSettings,
        event_tx: broadcast::Sender<DiscoveryEvent>,
    ) -> Self {
        Self {
            pools: DashMap::new(),
            max_pools: settings.max_pools,
            prune_max_age: Duration::from_secs(settings.prune_max_age_secs),
            scoring,
            event_tx,
            discovery_counter: AtomicU64::new(1),
        }
    }

    pub fn len(&self) -> usize {
        self.pools.len()
    }

    pub fn is_empty(&self) -> bool {
        self.pools.is_empty()
    }

    pub fn contains(&self, address: &Address) -> bool {
        self.pools.contains_key(address)
    }

    /// Insert or update a pool, recomputing normalised scores across the cache.
    pub fn upsert(&self, mut info: PoolInfo, inputs: PoolScoreInputs) -> PoolInfo {
        let raw = raw_score(&inputs, &self.scoring);
        let max_raw = self.max_raw_score().max(raw);
        info.score = normalise_score(raw, max_raw);
        info.tvl_usd = inputs.tvl_usd;
        info.volume_24h_usd = inputs.volume_24h_usd;
        info.slippage_estimate = inputs.slippage_estimate;

        if info.discovered_at == 0 {
            info.discovered_at = self.discovery_counter.fetch_add(1, Ordering::Relaxed);
        }

        let is_new = !self.pools.contains_key(&info.address);
        let cached = CachedPool {
            info: info.clone(),
            inserted_at: Instant::now(),
            last_scored_at: Instant::now(),
        };

        self.pools.insert(info.address, cached);
        self.enforce_capacity();

        let event = if is_new {
            DiscoveryEvent::PoolAdded(info.clone())
        } else {
            DiscoveryEvent::PoolUpdated(info.clone())
        };
        let _ = self.event_tx.send(event);

        // Re-normalise all scores when a new max appears.
        if raw >= max_raw {
            self.renormalise_all();
        }

        info
    }

    /// Return top-N pools sorted by score descending.
    pub fn get_top_n(&self, n: usize) -> Vec<PoolInfo> {
        let mut entries: Vec<PoolInfo> = self
            .pools
            .iter()
            .map(|e| e.value().info.clone())
            .collect();
        entries.sort_by(|a, b| {
            b.score
                .partial_cmp(&a.score)
                .unwrap_or(std::cmp::Ordering::Equal)
        });
        entries.truncate(n);
        entries
    }

    /// Remove pools older than `prune_max_age` or below `min_score`.
    pub fn prune(&self, min_score: f64) -> usize {
        let mut removed = 0usize;
        let to_remove: Vec<Address> = self
            .pools
            .iter()
            .filter(|e| {
                e.value().age() > self.prune_max_age || e.value().info.score < min_score
            })
            .map(|e| *e.key())
            .collect();

        for addr in to_remove {
            if self.pools.remove(&addr).is_some() {
                removed += 1;
                let _ = self.event_tx.send(DiscoveryEvent::PoolPruned {
                    address: addr,
                    reason: "age_or_low_score".into(),
                });
            }
        }

        if removed > 0 {
            self.renormalise_all();
            let _ = self.event_tx.send(DiscoveryEvent::CachePruned { removed });
        }
        removed
    }

    fn max_raw_score(&self) -> f64 {
        self.pools
            .iter()
            .map(|e| {
                raw_score(
                    &PoolScoreInputs {
                        tvl_usd: e.value().info.tvl_usd,
                        volume_24h_usd: e.value().info.volume_24h_usd,
                        fee_bps: e.value().info.fee_bps,
                        slippage_estimate: e.value().info.slippage_estimate,
                    },
                    &self.scoring,
                )
            })
            .fold(0.0f64, f64::max)
    }

    fn renormalise_all(&self) {
        let max_raw = self.max_raw_score();
        for mut entry in self.pools.iter_mut() {
            let inputs = PoolScoreInputs {
                tvl_usd: entry.info.tvl_usd,
                volume_24h_usd: entry.info.volume_24h_usd,
                fee_bps: entry.info.fee_bps,
                slippage_estimate: entry.info.slippage_estimate,
            };
            let raw = raw_score(&inputs, &self.scoring);
            entry.info.score = normalise_score(raw, max_raw);
            entry.last_scored_at = Instant::now();
        }
    }

    fn enforce_capacity(&self) {
        if self.pools.len() <= self.max_pools {
            return;
        }
        let excess = self.pools.len() - self.max_pools;
        let mut sorted: Vec<(Address, f64)> = self
            .pools
            .iter()
            .map(|e| (*e.key(), e.value().info.score))
            .collect();
        sorted.sort_by(|a, b| a.1.partial_cmp(&b.1).unwrap_or(std::cmp::Ordering::Equal));

        for (addr, _) in sorted.into_iter().take(excess) {
            self.pools.remove(&addr);
            let _ = self.event_tx.send(DiscoveryEvent::PoolPruned {
                address: addr,
                reason: "capacity".into(),
            });
        }
    }
}

/// Shared handle to the discovery cache.
pub type SharedDiscoveryCache = Arc<DiscoveryCache>;

#[cfg(test)]
mod tests {
    use super::*;
    use aether_common::types::ProtocolType;
    use alloy::primitives::address;

    fn make_cache() -> DiscoveryCache {
        let (tx, _rx) = broadcast::channel(16);
        DiscoveryCache::new(
            &DiscoverySettings::default(),
            ScoringSettings::default(),
            tx,
        )
    }

    fn sample_pool(addr_offset: u8, score_inputs: PoolScoreInputs) -> PoolInfo {
        PoolInfo {
            address: Address::from([addr_offset; 20]),
            token0: address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"),
            token1: address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"),
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            score: 0.0,
            tvl_usd: score_inputs.tvl_usd,
            volume_24h_usd: score_inputs.volume_24h_usd,
            slippage_estimate: score_inputs.slippage_estimate,
            discovered_at: 0,
        }
    }

    #[test]
    fn insert_and_len() {
        let cache = make_cache();
        let inputs = PoolScoreInputs {
            tvl_usd: 1_000_000.0,
            volume_24h_usd: 100_000.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        let info = sample_pool(1, inputs);
        cache.upsert(info, inputs);
        assert_eq!(cache.len(), 1);
        assert!(cache.contains(&Address::from([1u8; 20])));
    }

    #[test]
    fn get_top_n_ordering() {
        let cache = make_cache();
        let high = PoolScoreInputs {
            tvl_usd: 10_000_000.0,
            volume_24h_usd: 1_000_000.0,
            fee_bps: 30,
            slippage_estimate: 0.005,
        };
        let low = PoolScoreInputs {
            tvl_usd: 10_000.0,
            volume_24h_usd: 1_000.0,
            fee_bps: 30,
            slippage_estimate: 0.05,
        };
        cache.upsert(sample_pool(1, high), high);
        cache.upsert(sample_pool(2, low), low);

        let top = cache.get_top_n(10);
        assert_eq!(top.len(), 2);
        assert!(top[0].score >= top[1].score);
        assert_eq!(top[0].score, 1.0);
    }

    #[test]
    fn get_top_n_truncates() {
        let cache = make_cache();
        for i in 1u8..=5 {
            let inputs = PoolScoreInputs {
                tvl_usd: i as f64 * 100_000.0,
                volume_24h_usd: 10_000.0,
                fee_bps: 30,
                slippage_estimate: 0.01,
            };
            cache.upsert(sample_pool(i, inputs), inputs);
        }
        assert_eq!(cache.get_top_n(3).len(), 3);
    }

    #[test]
    fn update_existing_pool() {
        let cache = make_cache();
        let inputs = PoolScoreInputs {
            tvl_usd: 100_000.0,
            volume_24h_usd: 10_000.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        cache.upsert(sample_pool(1, inputs), inputs);
        let better = PoolScoreInputs {
            tvl_usd: 5_000_000.0,
            volume_24h_usd: 500_000.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        cache.upsert(sample_pool(1, better), better);
        assert_eq!(cache.len(), 1);
        assert_eq!(cache.get_top_n(1)[0].tvl_usd, 5_000_000.0);
    }

    #[test]
    fn prune_by_min_score() {
        let cache = make_cache();
        let high = PoolScoreInputs {
            tvl_usd: 1_000_000.0,
            volume_24h_usd: 100_000.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        let low = PoolScoreInputs {
            tvl_usd: 1.0,
            volume_24h_usd: 1.0,
            fee_bps: 30,
            slippage_estimate: 0.5,
        };
        cache.upsert(sample_pool(1, high), high);
        cache.upsert(sample_pool(2, low), low);
        let removed = cache.prune(0.1);
        assert!(removed >= 1);
        assert_eq!(cache.len(), 1);
    }

    #[test]
    fn capacity_enforcement() {
        let (tx, _rx) = broadcast::channel(16);
        let cache = DiscoveryCache::new(
            &DiscoverySettings {
                max_pools: 3,
                ..Default::default()
            },
            ScoringSettings::default(),
            tx,
        );
        for i in 1u8..=5 {
            let inputs = PoolScoreInputs {
                tvl_usd: i as f64 * 50_000.0,
                volume_24h_usd: 5_000.0,
                fee_bps: 30,
                slippage_estimate: 0.01,
            };
            cache.upsert(sample_pool(i, inputs), inputs);
        }
        assert!(cache.len() <= 3);
    }

    #[test]
    fn empty_cache_top_n() {
        let cache = make_cache();
        assert!(cache.get_top_n(10).is_empty());
    }

    #[test]
    fn normalised_scores_in_range() {
        let cache = make_cache();
        for i in 1u8..=3 {
            let inputs = PoolScoreInputs {
                tvl_usd: i as f64 * 200_000.0,
                volume_24h_usd: 20_000.0,
                fee_bps: 30,
                slippage_estimate: 0.01,
            };
            cache.upsert(sample_pool(i, inputs), inputs);
        }
        for p in cache.get_top_n(10) {
            assert!(p.score >= 0.0 && p.score <= 1.0);
        }
    }

    #[test]
    fn events_emitted_on_insert() {
        let (tx, mut rx) = broadcast::channel(16);
        let cache = DiscoveryCache::new(
            &DiscoverySettings::default(),
            ScoringSettings::default(),
            tx,
        );
        let inputs = PoolScoreInputs {
            tvl_usd: 500_000.0,
            volume_24h_usd: 50_000.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        cache.upsert(sample_pool(9, inputs), inputs);
        let event = rx.try_recv().unwrap();
        assert!(matches!(event, DiscoveryEvent::PoolAdded(_)));
    }

    #[test]
    fn zero_volume_pool_scores_zero() {
        let cache = make_cache();
        let inputs = PoolScoreInputs {
            tvl_usd: 1_000_000.0,
            volume_24h_usd: 0.0,
            fee_bps: 30,
            slippage_estimate: 0.01,
        };
        let info = cache.upsert(sample_pool(7, inputs), inputs);
        assert_eq!(info.score, 0.0);
    }
}

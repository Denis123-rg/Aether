//! Hot cache of top-scoring discovery pools for the detection graph.
//!
//! Refreshed every 5 seconds from `discovery::get_top_n(500)`. Pools that fall
//! out of the top-N are removed; new entrants are pre-warmed (bytecode +
//! reserves) before participating in Bellman-Ford detection.

mod metrics;
mod updater;

pub use metrics::HotCacheMetrics;
pub use updater::{HotCache, HotCacheDiff, HotCacheUpdater, HotCacheUpdaterConfig};

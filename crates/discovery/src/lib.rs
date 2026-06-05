//! Dynamic pool discovery service for Aether.
//!
//! Listens to factory `PoolCreated` / `PairCreated` events, validates pools
//! via analytical swap checks (and optional revm fork simulation), scores by
//! TVL and 24h volume, and maintains a ranked in-memory cache.

pub mod cache;
pub mod config;
pub mod events;
pub mod scorer;
pub mod service;
pub mod types;
pub mod validator;

pub use cache::DiscoveryCache;
pub use config::DiscoveryConfig;
pub use service::DiscoveryService;
pub use types::{DiscoveryEvent, PoolInfo, PoolScoreInputs, ValidationResult};

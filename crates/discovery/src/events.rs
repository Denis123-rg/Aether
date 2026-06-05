//! Factory event listener for pool discovery.
//!
//! Decodes `PairCreated` / `PoolCreated` logs and forwards them to the
//! discovery service for validation and scoring.

use aether_common::types::ProtocolType;
use aether_ingestion::event_decoder::{decode_log, EventSignatures, PoolEvent};
use alloy::primitives::{Address, B256};
use alloy::providers::{DynProvider, Provider};
use alloy::network::Ethereum;
use alloy::rpc::types::Filter;
use tracing::{debug, info, warn};

use crate::service::DiscoveryService;

/// Decoded factory event ready for ingestion.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FactoryPoolCreated {
    pub factory: Address,
    pub protocol: ProtocolType,
    pub fee_bps: u32,
    pub token0: Address,
    pub token1: Address,
    pub pool: Address,
}

/// Decode a raw log into a `FactoryPoolCreated` event when it matches a
/// configured factory's `PairCreated` topic.
pub fn decode_pair_created_log(
    factory: Address,
    protocol: ProtocolType,
    fee_bps: u32,
    topics: &[B256],
    data: &[u8],
) -> Option<FactoryPoolCreated> {
    if topics.is_empty() || topics[0] != EventSignatures::pair_created_topic() {
        return None;
    }
    match decode_log(topics, data, factory, Some(protocol)) {
        Ok(PoolEvent::PoolCreated {
            token0,
            token1,
            pool,
        }) => Some(FactoryPoolCreated {
            factory,
            protocol,
            fee_bps,
            token0,
            token1,
            pool,
        }),
        _ => None,
    }
}

/// Build an alloy log filter for all configured factory addresses.
pub fn build_factory_filter(factories: &[(Address, ProtocolType, u32)]) -> Filter {
    let addresses: Vec<Address> = factories.iter().map(|(a, _, _)| *a).collect();
    Filter::new()
        .address(addresses)
        .event_signature(EventSignatures::pair_created_topic())
}

/// Process a batch of logs from a subscription and ingest valid pools.
pub async fn process_logs(
    service: &DiscoveryService,
    factories: &[(Address, ProtocolType, u32)],
    logs: Vec<alloy::rpc::types::Log>,
) -> usize {
    let mut ingested = 0usize;
    for log in logs {
        let factory_addr = log.address();
        let Some((_, protocol, fee_bps)) = factories
            .iter()
            .find(|(a, _, _)| *a == factory_addr)
        else {
            continue;
        };

        let topics: Vec<B256> = log.topics().to_vec();
        let data = log.data().data.as_ref();

        if let Some(created) =
            decode_pair_created_log(factory_addr, *protocol, *fee_bps, &topics, data)
        {
            debug!(pool = %created.pool, ?protocol, "PoolCreated event decoded");
            if service.ingest_pool_created(created).await {
                ingested += 1;
            }
        }
    }
    ingested
}

/// Poll factory logs over a block range (used when WS subscription unavailable).
pub async fn poll_factory_logs(
    provider: &DynProvider<Ethereum>,
    service: &DiscoveryService,
    factories: &[(Address, ProtocolType, u32)],
    from_block: u64,
    to_block: u64,
) -> anyhow::Result<usize> {
    let filter = build_factory_filter(factories)
        .from_block(from_block)
        .to_block(to_block);
    let logs = provider.get_logs(&filter).await?;
    Ok(process_logs(service, factories, logs).await)
}

/// Spawn a background task that polls factory events every `interval_secs`.
pub fn spawn_polling_listener(
    provider: DynProvider<Ethereum>,
    service: std::sync::Arc<DiscoveryService>,
    factories: Vec<(Address, ProtocolType, u32)>,
    interval_secs: u64,
    mut shutdown: tokio::sync::watch::Receiver<bool>,
) -> tokio::task::JoinHandle<()> {
    tokio::spawn(async move {
        let mut interval = tokio::time::interval(std::time::Duration::from_secs(interval_secs));
        let mut last_block = match provider.get_block_number().await {
            Ok(n) => n.saturating_sub(1),
            Err(e) => {
                warn!(error = %e, "discovery listener: failed to get block number");
                0
            }
        };

        loop {
            tokio::select! {
                _ = interval.tick() => {
                    let current = match provider.get_block_number().await {
                        Ok(n) => n,
                        Err(e) => {
                            warn!(error = %e, "discovery listener: block number poll failed");
                            continue;
                        }
                    };
                    if current > last_block {
                        match poll_factory_logs(
                            &provider,
                            &service,
                            &factories,
                            last_block + 1,
                            current,
                        ).await {
                            Ok(n) if n > 0 => info!(ingested = n, "discovery poll ingested pools"),
                            Ok(_) => {}
                            Err(e) => warn!(error = %e, "discovery poll failed"),
                        }
                        last_block = current;
                    }
                }
                _ = shutdown.changed() => {
                    if *shutdown.borrow() {
                        info!("discovery listener shutting down");
                        break;
                    }
                }
            }
        }
    })
}

/// Simulate a `PairCreated` log for testing (mock event listener).
pub fn mock_pair_created_log(
    _factory: Address,
    token0: Address,
    token1: Address,
    pair: Address,
    all_pairs_length: u64,
) -> (Vec<B256>, Vec<u8>) {
    use alloy::primitives::U256;
    let topic0 = EventSignatures::pair_created_topic();
    let topics = vec![
        topic0,
        B256::left_padding_from(token0.as_slice()),
        B256::left_padding_from(token1.as_slice()),
    ];
    let mut data = vec![0u8; 64];
    data[12..32].copy_from_slice(pair.as_slice());
    data[32..64].copy_from_slice(&U256::from(all_pairs_length).to_be_bytes::<32>());
    (topics, data)
}

#[cfg(test)]
mod tests {
    use super::*;
    use alloy::primitives::{address, U256};

    fn uni_v2_factory() -> Address {
        address!("5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f")
    }

    #[test]
    fn decode_pair_created_valid() {
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let pair = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
        let (topics, data) = mock_pair_created_log(uni_v2_factory(), token0, token1, pair, 42);

        let decoded = decode_pair_created_log(
            uni_v2_factory(),
            ProtocolType::UniswapV2,
            30,
            &topics,
            &data,
        );
        assert_eq!(
            decoded,
            Some(FactoryPoolCreated {
                factory: uni_v2_factory(),
                protocol: ProtocolType::UniswapV2,
                fee_bps: 30,
                token0,
                token1,
                pool: pair,
            })
        );
    }

    #[test]
    fn decode_wrong_topic_returns_none() {
        let result = decode_pair_created_log(
            uni_v2_factory(),
            ProtocolType::UniswapV2,
            30,
            &[B256::ZERO],
            &[],
        );
        assert!(result.is_none());
    }

    #[test]
    fn build_factory_filter_succeeds() {
        let factories = vec![(uni_v2_factory(), ProtocolType::UniswapV2, 30)];
        let _filter = build_factory_filter(&factories);
    }

    #[test]
    fn factory_pool_created_equality() {
        let a = FactoryPoolCreated {
            factory: uni_v2_factory(),
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            token0: Address::ZERO,
            token1: Address::ZERO,
            pool: Address::ZERO,
        };
        assert_eq!(a, a.clone());
    }

    #[test]
    fn mock_log_produces_three_topics() {
        let (topics, data) = mock_pair_created_log(
            uni_v2_factory(),
            Address::ZERO,
            Address::ZERO,
            Address::ZERO,
            1,
        );
        assert_eq!(topics.len(), 3);
        assert!(data.len() >= 64);
    }

    #[test]
    fn decode_empty_topics_none() {
        assert!(decode_pair_created_log(
            uni_v2_factory(),
            ProtocolType::UniswapV2,
            30,
            &[],
            &[]
        )
        .is_none());
    }

    #[test]
    fn decode_preserves_fee_bps() {
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let pair = Address::from([0x42; 20]);
        let (topics, data) = mock_pair_created_log(uni_v2_factory(), token0, token1, pair, 1);
        let decoded = decode_pair_created_log(uni_v2_factory(), ProtocolType::SushiSwap, 25, &topics, &data)
            .unwrap();
        assert_eq!(decoded.fee_bps, 25);
        assert_eq!(decoded.protocol, ProtocolType::SushiSwap);
    }

    #[test]
    fn mock_log_pair_address_in_data() {
        let pair = Address::from([0x99; 20]);
        let (_, data) = mock_pair_created_log(
            uni_v2_factory(),
            Address::ZERO,
            Address::ZERO,
            pair,
            7,
        );
        assert_eq!(&data[12..32], pair.as_slice());
    }

    #[test]
    fn mock_log_all_pairs_length_encoded() {
        let (_, data) = mock_pair_created_log(
            uni_v2_factory(),
            Address::ZERO,
            Address::ZERO,
            Address::ZERO,
            12345,
        );
        let len = U256::from_be_slice(&data[32..64]);
        assert_eq!(len, U256::from(12345));
    }

    #[test]
    fn build_factory_filter_multiple_factories() {
        let sushi = address!("C0AEe478e3658e2610c5F7A4A2D1773cDCC8b275");
        let factories = vec![
            (uni_v2_factory(), ProtocolType::UniswapV2, 30),
            (sushi, ProtocolType::SushiSwap, 30),
        ];
        let _filter = build_factory_filter(&factories);
    }
}

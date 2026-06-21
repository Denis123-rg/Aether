use aether_common::types::ProtocolType;
use alloy::primitives::{Address, B256, U256};
use alloy::sol;
use alloy::sol_types::SolEvent;
use tracing::trace;

// Factory-event coverage notes:
// - Uniswap V2/V3, SushiSwap, Curve NG, and Balancer V3 `PoolRegistered` are
//   decoded here for dynamic discovery.
// - Balancer V2 and Bancor V3 do NOT emit factory events handled by this
//   generic decoder; those protocols are discovered via dedicated listeners in
//   `aether-discovery` (vault/registry scans). This is intentional — not a gap.

// Compile-time ABI definitions via alloy sol! macro
sol! {
    // Uniswap V2 / SushiSwap Sync event
    event Sync(uint112 reserve0, uint112 reserve1);

    // Uniswap V2 / SushiSwap Swap event
    event Swap(
        address indexed sender,
        uint256 amount0In,
        uint256 amount1In,
        uint256 amount0Out,
        uint256 amount1Out,
        address indexed to
    );

    // Uniswap V3 Swap event
    event SwapV3(
        address indexed sender,
        address indexed recipient,
        int256 amount0,
        int256 amount1,
        uint160 sqrtPriceX96,
        uint128 liquidity,
        int24 tick
    );

    // Curve TokenExchange event
    event TokenExchange(
        address indexed buyer,
        int128 sold_id,
        uint256 tokens_sold,
        int128 bought_id,
        uint256 tokens_bought
    );

    // Uniswap V2 PairCreated (factory event for discovery)
    event PairCreated(
        address indexed token0,
        address indexed token1,
        address pair,
        uint256 allPairsLength
    );

    // Uniswap V3 PoolCreated (factory event for discovery)
    event PoolCreated(
        address indexed token0,
        address indexed token1,
        uint24 indexed fee,
        int24 tickSpacing,
        address pool
    );

    // Curve Stableswap-NG PlainPoolDeployed (factory event for discovery).
    // Exported for discovery mock log helpers and tests.
    event PlainPoolDeployed(
        address pool,
        address[] coins,
        uint256 A,
        uint256 fee,
        address owner
    );

    // Balancer V3 Vault PoolRegistered (pool discovery via vault)
    struct BalancerV3TokenConfig {
        address token;
        uint8 tokenType;
        address rateProvider;
        bool paysYieldFees;
    }
    struct BalancerV3RoleAccounts {
        address pauseManager;
        address swapFeeManager;
        address poolCreator;
    }
    struct BalancerV3HooksConfig {
        bool enableHookAdjustedAmounts;
        bool shouldCallBeforeInitialize;
        bool shouldCallAfterInitialize;
        bool shouldCallComputeDynamicSwapFee;
        bool shouldCallBeforeSwap;
        bool shouldCallAfterSwap;
        bool shouldCallBeforeAddLiquidity;
        bool shouldCallAfterAddLiquidity;
        bool shouldCallBeforeRemoveLiquidity;
        bool shouldCallAfterRemoveLiquidity;
        address hooksContract;
    }
    struct BalancerV3LiquidityManagement {
        bool disableUnbalancedLiquidity;
        bool enableAddLiquidityCustom;
        bool enableRemoveLiquidityCustom;
        bool enableDonation;
    }
    event PoolRegistered(
        address indexed pool,
        address indexed factory,
        BalancerV3TokenConfig[] tokenConfig,
        uint256 swapFeePercentage,
        uint32 pauseWindowEndTime,
        BalancerV3RoleAccounts roleAccounts,
        BalancerV3HooksConfig hooksConfig,
        BalancerV3LiquidityManagement liquidityManagement
    );
}

/// Decoded pool update event
#[derive(Debug, Clone)]
pub enum PoolEvent {
    /// Reserve update (UniV2/Sushi Sync)
    ReserveUpdate {
        pool: Address,
        protocol: ProtocolType,
        reserve0: U256,
        reserve1: U256,
    },
    /// V2 / Sushi Swap — informational (reserves still reconcile via the
    /// paired `Sync` event; this variant exposes per-trade amounts and
    /// participants for downstream analytics).
    V2Swap {
        pool: Address,
        sender: Address,
        to: Address,
        amount0_in: U256,
        amount1_in: U256,
        amount0_out: U256,
        amount1_out: U256,
    },
    /// V3 state update
    V3Update {
        pool: Address,
        sqrt_price_x96: U256,
        liquidity: u128,
        tick: i32,
    },
    /// New pool created
    PoolCreated {
        token0: Address,
        token1: Address,
        pool: Address,
    },
}

/// Event topic signatures for filtering
pub struct EventSignatures;

impl EventSignatures {
    pub fn sync_topic() -> B256 {
        Sync::SIGNATURE_HASH
    }

    pub fn swap_v2_topic() -> B256 {
        Swap::SIGNATURE_HASH
    }

    pub fn swap_v3_topic() -> B256 {
        SwapV3::SIGNATURE_HASH
    }

    pub fn token_exchange_topic() -> B256 {
        TokenExchange::SIGNATURE_HASH
    }

    pub fn pair_created_topic() -> B256 {
        PairCreated::SIGNATURE_HASH
    }

    pub fn pool_created_v3_topic() -> B256 {
        PoolCreated::SIGNATURE_HASH
    }

    pub fn plain_pool_deployed_topic() -> B256 {
        PlainPoolDeployed::SIGNATURE_HASH
    }

    pub fn pool_registered_v3_topic() -> B256 {
        PoolRegistered::SIGNATURE_HASH
    }
}

/// Reason a log failed to decode. Surfaced as the `reason` label on
/// `aether_decode_errors_total` so ops can alert on malformed payloads
/// (real bug) without drowning in benign unknown-topic noise.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum DecodeReason {
    /// topic0 didn't match any known event signature (or topics was empty).
    /// Expected to be high-volume in discovery mode.
    UnknownTopic,
    /// Payload length check failed in a known-event decoder — data-integrity
    /// signal worth paging on.
    MalformedPayload,
    /// Fewer topics than the known-event decoder requires (e.g. PairCreated
    /// short-path). Indicates an upstream / node-side producer bug.
    InsufficientTopics,
}

impl DecodeReason {
    /// Stable label value for Prometheus.
    pub fn as_str(self) -> &'static str {
        match self {
            DecodeReason::UnknownTopic => "unknown_topic",
            DecodeReason::MalformedPayload => "malformed_payload",
            DecodeReason::InsufficientTopics => "insufficient_topics",
        }
    }
}

/// Decode a raw log into a PoolEvent.
///
/// On failure the `DecodeReason` is propagated so callers can bump the
/// appropriate Prometheus label — see `aether_decode_errors_total`.
pub fn decode_log(
    topics: &[B256],
    data: &[u8],
    source_address: Address,
    protocol_hint: Option<ProtocolType>,
) -> Result<PoolEvent, DecodeReason> {
    if topics.is_empty() {
        return Err(DecodeReason::UnknownTopic);
    }

    let topic0 = topics[0];

    if topic0 == EventSignatures::sync_topic() {
        decode_sync(data, source_address, protocol_hint)
    } else if topic0 == EventSignatures::swap_v2_topic() {
        decode_swap_v2(topics, data, source_address)
    } else if topic0 == EventSignatures::swap_v3_topic() {
        decode_swap_v3(topics, data, source_address)
    } else if topic0 == EventSignatures::token_exchange_topic() {
        decode_token_exchange(topics, data, source_address)
    } else if topic0 == EventSignatures::pair_created_topic() {
        decode_pair_created(topics, data)
    } else if topic0 == EventSignatures::pool_created_v3_topic() {
        decode_pool_created_v3(topics, data)
    } else {
        Err(DecodeReason::UnknownTopic)
    }
}

fn decode_swap_v2(topics: &[B256], data: &[u8], pool: Address) -> Result<PoolEvent, DecodeReason> {
    // V2 Swap(address indexed sender, uint256 amount0In, uint256 amount1In,
    //        uint256 amount0Out, uint256 amount1Out, address indexed to)
    //
    // topics: [topic0, sender (indexed), to (indexed)]
    // data:   4 × 32-byte words — amount0In | amount1In | amount0Out | amount1Out
    if topics.len() < 3 {
        return Err(DecodeReason::InsufficientTopics);
    }
    if data.len() < 128 {
        return Err(DecodeReason::MalformedPayload);
    }

    let sender = Address::from_slice(&topics[1].as_slice()[12..]);
    let to = Address::from_slice(&topics[2].as_slice()[12..]);

    let amount0_in = U256::from_be_slice(&data[0..32]);
    let amount1_in = U256::from_be_slice(&data[32..64]);
    let amount0_out = U256::from_be_slice(&data[64..96]);
    let amount1_out = U256::from_be_slice(&data[96..128]);

    trace!(
        %pool, %sender, %to,
        %amount0_in, %amount1_in, %amount0_out, %amount1_out,
        "V2 Swap decoded"
    );

    Ok(PoolEvent::V2Swap {
        pool,
        sender,
        to,
        amount0_in,
        amount1_in,
        amount0_out,
        amount1_out,
    })
}

fn decode_sync(
    data: &[u8],
    pool: Address,
    protocol_hint: Option<ProtocolType>,
) -> Result<PoolEvent, DecodeReason> {
    if data.len() < 64 {
        return Err(DecodeReason::MalformedPayload);
    }
    let reserve0 = U256::from_be_slice(&data[0..32]);
    let reserve1 = U256::from_be_slice(&data[32..64]);
    let protocol = protocol_hint.unwrap_or(ProtocolType::UniswapV2);

    trace!(pool = %pool, r0 = %reserve0, r1 = %reserve1, "Sync event decoded");

    Ok(PoolEvent::ReserveUpdate {
        pool,
        protocol,
        reserve0,
        reserve1,
    })
}

fn decode_swap_v3(topics: &[B256], data: &[u8], pool: Address) -> Result<PoolEvent, DecodeReason> {
    if data.len() < 160 {
        return Err(DecodeReason::MalformedPayload);
    }
    // amount0: int256 (bytes 0-32)
    // amount1: int256 (bytes 32-64)
    // sqrtPriceX96: uint160 (bytes 64-96)
    // liquidity: uint128 (bytes 96-128)
    // tick: int24 (bytes 128-160)
    let sqrt_price_x96 = U256::from_be_slice(&data[64..96]);
    let liquidity_u256 = U256::from_be_slice(&data[96..128]);
    let liquidity = liquidity_u256.to::<u128>();

    // tick is int24, stored in last 3 bytes of the 32-byte word
    let tick_bytes = &data[128..160];
    let tick_i256 = i32::from_be_bytes([
        tick_bytes[28],
        tick_bytes[29],
        tick_bytes[30],
        tick_bytes[31],
    ]);
    // Sign extend from 24 bits
    let tick = if tick_i256 & 0x800000 != 0 {
        tick_i256 | !0xFFFFFF_i32
    } else {
        tick_i256 & 0xFFFFFF
    };

    let _ = topics; // topics contain indexed sender and recipient

    trace!(
        pool = %pool,
        sqrt_price = %sqrt_price_x96,
        liq = %liquidity,
        tick = %tick,
        "V3 Swap decoded"
    );

    Ok(PoolEvent::V3Update {
        pool,
        sqrt_price_x96,
        liquidity,
        tick,
    })
}

fn decode_token_exchange(
    topics: &[B256],
    _data: &[u8],
    pool: Address,
) -> Result<PoolEvent, DecodeReason> {
    // Curve events update reserves; we'd need to query on-chain for new balances
    // For now, emit a generic reserve update that triggers a state refresh
    let _ = topics;
    Ok(PoolEvent::ReserveUpdate {
        pool,
        protocol: ProtocolType::Curve,
        reserve0: U256::ZERO, // Will be refreshed from on-chain
        reserve1: U256::ZERO,
    })
}

fn decode_pair_created(topics: &[B256], data: &[u8]) -> Result<PoolEvent, DecodeReason> {
    if topics.len() < 3 {
        return Err(DecodeReason::InsufficientTopics);
    }
    if data.len() < 64 {
        return Err(DecodeReason::MalformedPayload);
    }
    let token0 = Address::from_slice(&topics[1].as_slice()[12..]);
    let token1 = Address::from_slice(&topics[2].as_slice()[12..]);
    let pool = Address::from_slice(&data[12..32]);

    Ok(PoolEvent::PoolCreated {
        token0,
        token1,
        pool,
    })
}

/// Decode Uniswap V3 `PoolCreated` factory event. Fee is in hundredths of a bip
/// (e.g. 3000 = 0.30% = 30 bps).
fn decode_pool_created_v3(topics: &[B256], data: &[u8]) -> Result<PoolEvent, DecodeReason> {
    if topics.len() < 4 {
        return Err(DecodeReason::InsufficientTopics);
    }
    if data.len() < 64 {
        return Err(DecodeReason::MalformedPayload);
    }
    let token0 = Address::from_slice(&topics[1].as_slice()[12..]);
    let token1 = Address::from_slice(&topics[2].as_slice()[12..]);
    let pool = Address::from_slice(&data[32..52]);

    Ok(PoolEvent::PoolCreated {
        token0,
        token1,
        pool,
    })
}

/// Returns the V3 pool fee in basis points from the indexed fee topic.
pub fn v3_fee_bps_from_topic(fee_topic: &B256) -> u32 {
    let fee = U256::from_be_slice(fee_topic.as_slice());
    // fee is in hundredths of a bip: divide by 100 to get bps.
    (fee / U256::from(100u64)).as_limbs()[0] as u32
}

/// Decode a Curve `PlainPoolDeployed` factory log into pool + first two coins.
/// Returns `(pool, token0, token1, fee_bps)` when at least two coins are present.
pub fn decode_plain_pool_deployed(
    topics: &[B256],
    data: &[u8],
) -> Option<(Address, Address, Address, u32)> {
    if topics.is_empty() || topics[0] != EventSignatures::plain_pool_deployed_topic() {
        return None;
    }
    let decoded = PlainPoolDeployed::decode_raw_log(topics, data).ok()?;
    if decoded.coins.len() < 2 {
        return None;
    }
    let fee_bps = curve_fee_to_bps(decoded.fee);
    Some((decoded.pool, decoded.coins[0], decoded.coins[1], fee_bps))
}

/// Decode a Balancer V3 `PoolRegistered` vault log.
/// Returns `(pool, token0, token1, fee_bps)` when at least two tokens are configured.
pub fn decode_pool_registered_v3(
    topics: &[B256],
    data: &[u8],
) -> Option<(Address, Address, Address, u32)> {
    if topics.len() < 3 || topics[0] != EventSignatures::pool_registered_v3_topic() {
        return None;
    }
    let decoded = PoolRegistered::decode_raw_log(topics, data).ok()?;
    if decoded.tokenConfig.len() < 2 {
        return None;
    }
    let fee_bps = balancer_v3_fee_to_bps(decoded.swapFeePercentage);
    let t0 = decoded.tokenConfig.first()?.token;
    let t1 = decoded.tokenConfig.get(1)?.token;
    Some((decoded.pool, t0, t1, fee_bps))
}

/// Convert Curve on-chain fee (1e10 fixed point) to basis points.
pub fn curve_fee_to_bps(fee: U256) -> u32 {
    // fee / 1e10 * 10000 = fee * 10000 / 1e10
    let bps = fee.saturating_mul(U256::from(10_000u64)) / U256::from(10u128.pow(10));
    bps.as_limbs()[0].min(u32::MAX as u64) as u32
}

/// Convert Balancer V3 `swapFeePercentage` (1e18 fixed point) to basis points.
pub fn balancer_v3_fee_to_bps(swap_fee: U256) -> u32 {
    let bps = swap_fee.saturating_mul(U256::from(10_000u64)) / U256::from(10u128.pow(18));
    bps.as_limbs()[0].min(u32::MAX as u64) as u32
}

#[cfg(test)]
mod tests {
    use super::*;
    use alloy::primitives::address;

    /// Build a 32-byte big-endian representation of a U256
    fn u256_to_be_bytes(val: U256) -> [u8; 32] {
        val.to_be_bytes::<32>()
    }

    // ── EventSignatures tests ──

    #[test]
    fn test_event_signatures_are_nonzero_and_distinct() {
        let sigs = [
            EventSignatures::sync_topic(),
            EventSignatures::swap_v2_topic(),
            EventSignatures::swap_v3_topic(),
            EventSignatures::token_exchange_topic(),
            EventSignatures::pair_created_topic(),
        ];
        for sig in &sigs {
            assert_ne!(*sig, B256::ZERO, "Signature should not be zero");
        }
        // All must be distinct
        for i in 0..sigs.len() {
            for j in (i + 1)..sigs.len() {
                assert_ne!(sigs[i], sigs[j], "Signatures at {i} and {j} must differ");
            }
        }
    }

    // ── Sync event decode tests ──

    #[test]
    fn test_decode_sync_event() {
        let pool_addr = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
        let reserve0 = U256::from(1_000_000_000_000_000_000u64); // 1e18
        let reserve1 = U256::from(2_000_000_000u64); // 2e9

        let mut data = Vec::new();
        data.extend_from_slice(&u256_to_be_bytes(reserve0));
        data.extend_from_slice(&u256_to_be_bytes(reserve1));

        let topics = vec![EventSignatures::sync_topic()];

        let event = decode_log(&topics, &data, pool_addr, None);
        assert!(event.is_ok());

        let got = event.unwrap();
        let PoolEvent::ReserveUpdate {
            pool,
            protocol,
            reserve0: r0,
            reserve1: r1,
        } = got
        else {
            panic!("decoder returned unexpected variant: {got:?}");
        };
        assert_eq!(pool, pool_addr);
        assert_eq!(protocol, ProtocolType::UniswapV2);
        assert_eq!(r0, reserve0);
        assert_eq!(r1, reserve1);
    }

    #[test]
    fn test_decode_sync_event_with_sushi_hint() {
        let pool_addr = Address::ZERO;
        let reserve0 = U256::from(500u64);
        let reserve1 = U256::from(1000u64);

        let mut data = Vec::new();
        data.extend_from_slice(&u256_to_be_bytes(reserve0));
        data.extend_from_slice(&u256_to_be_bytes(reserve1));

        let topics = vec![EventSignatures::sync_topic()];

        let event = decode_log(&topics, &data, pool_addr, Some(ProtocolType::SushiSwap));
        let got = event.unwrap();
        let PoolEvent::ReserveUpdate { protocol, .. } = got else {
            panic!("decoder returned unexpected variant: {got:?}");
        };
        assert_eq!(protocol, ProtocolType::SushiSwap);
    }

    #[test]
    fn test_decode_sync_event_too_short_data() {
        let topics = vec![EventSignatures::sync_topic()];
        // Only 32 bytes instead of 64
        let data = vec![0u8; 32];
        let event = decode_log(&topics, &data, Address::ZERO, None);
        assert!(event.is_err());
    }

    // ── V2 Swap event decode tests ──

    #[test]
    fn test_decode_v2_swap_event() {
        let pool_addr = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
        let sender = address!("7a250d5630B4cF539739dF2C5dAcb4c659F2488D"); // UniV2 Router
        let to = address!("beA0C8daDd4Ec0E6B24ae60e7A5f24d3cE60FEce");

        // topic[1] = sender (indexed, left-padded)
        let mut topic1 = [0u8; 32];
        topic1[12..32].copy_from_slice(sender.as_slice());

        // topic[2] = to (indexed, left-padded)
        let mut topic2 = [0u8; 32];
        topic2[12..32].copy_from_slice(to.as_slice());

        let topics = vec![
            EventSignatures::swap_v2_topic(),
            B256::from(topic1),
            B256::from(topic2),
        ];

        let amount0_in = U256::from(1_000_000_000u64); // 1000 USDC (6 dec)
        let amount1_in = U256::ZERO;
        let amount0_out = U256::ZERO;
        let amount1_out = U256::from(500_000_000_000_000_000u64); // 0.5 ETH

        let mut data = Vec::new();
        data.extend_from_slice(&u256_to_be_bytes(amount0_in));
        data.extend_from_slice(&u256_to_be_bytes(amount1_in));
        data.extend_from_slice(&u256_to_be_bytes(amount0_out));
        data.extend_from_slice(&u256_to_be_bytes(amount1_out));

        let event = decode_log(&topics, &data, pool_addr, None);
        assert!(event.is_ok(), "V2 Swap must decode, not fall through");

        match event.unwrap() {
            PoolEvent::V2Swap {
                pool,
                sender: s,
                to: t,
                amount0_in: a0i,
                amount1_in: a1i,
                amount0_out: a0o,
                amount1_out: a1o,
            } => {
                assert_eq!(pool, pool_addr);
                assert_eq!(s, sender);
                assert_eq!(t, to);
                assert_eq!(a0i, amount0_in);
                assert_eq!(a1i, amount1_in);
                assert_eq!(a0o, amount0_out);
                assert_eq!(a1o, amount1_out);
            }
            other => panic!("Expected V2Swap, got {:?}", other),
        }
    }

    #[test]
    fn test_decode_v2_swap_insufficient_topics() {
        let topics = vec![
            EventSignatures::swap_v2_topic(),
            B256::ZERO,
            // missing `to` topic
        ];
        let data = vec![0u8; 128];
        assert_eq!(
            decode_log(&topics, &data, Address::ZERO, None).unwrap_err(),
            DecodeReason::InsufficientTopics
        );
    }

    #[test]
    fn test_decode_v2_swap_insufficient_data() {
        let topics = vec![EventSignatures::swap_v2_topic(), B256::ZERO, B256::ZERO];
        // 96 bytes instead of 128
        let data = vec![0u8; 96];
        assert_eq!(
            decode_log(&topics, &data, Address::ZERO, None).unwrap_err(),
            DecodeReason::MalformedPayload
        );
    }

    // ── V3 Swap event decode tests ──

    #[test]
    fn test_decode_v3_swap_event() {
        let pool_addr = address!("88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640");

        // Build 160 bytes of data:
        // amount0 (int256): 32 bytes
        // amount1 (int256): 32 bytes
        // sqrtPriceX96 (uint160): 32 bytes
        // liquidity (uint128): 32 bytes
        // tick (int24): 32 bytes
        let mut data = Vec::new();

        // amount0 = 1e18 (positive)
        data.extend_from_slice(&u256_to_be_bytes(U256::from(1_000_000_000_000_000_000u64)));
        // amount1 = -2000e6 (negative, as two's complement)
        let neg_amount1 = U256::MAX - U256::from(2_000_000_000u64) + U256::from(1u64);
        data.extend_from_slice(&u256_to_be_bytes(neg_amount1));
        // sqrtPriceX96
        let sqrt_price = U256::from(1_234_567_890_123_456_789u64);
        data.extend_from_slice(&u256_to_be_bytes(sqrt_price));
        // liquidity = 5_000_000
        let liquidity = U256::from(5_000_000u64);
        data.extend_from_slice(&u256_to_be_bytes(liquidity));
        // tick = 200 (positive i24)
        let tick_val = U256::from(200u64);
        data.extend_from_slice(&u256_to_be_bytes(tick_val));

        let topics = vec![
            EventSignatures::swap_v3_topic(),
            B256::ZERO, // sender (indexed)
            B256::ZERO, // recipient (indexed)
        ];

        let event = decode_log(&topics, &data, pool_addr, None);
        assert!(event.is_ok());

        let got = event.unwrap();
        let PoolEvent::V3Update {
            pool,
            sqrt_price_x96,
            liquidity: liq,
            tick,
        } = got
        else {
            panic!("decoder returned unexpected variant: {got:?}");
        };
        assert_eq!(pool, pool_addr);
        assert_eq!(sqrt_price_x96, sqrt_price);
        assert_eq!(liq, 5_000_000u128);
        assert_eq!(tick, 200);
    }

    #[test]
    fn test_decode_v3_swap_negative_tick() {
        let pool_addr = Address::ZERO;

        let mut data = Vec::new();
        // amount0, amount1
        data.extend_from_slice(&[0u8; 64]);
        // sqrtPriceX96
        data.extend_from_slice(&u256_to_be_bytes(U256::from(999u64)));
        // liquidity
        data.extend_from_slice(&u256_to_be_bytes(U256::from(100u64)));
        // tick = -100 as int24 in 32 bytes (two's complement)
        // -100 in 24-bit = 0xFFFF9C, in 32-byte word last 4 bytes: 0xFFFFFF9C
        let neg_tick = U256::MAX - U256::from(99u64); // two's complement for -100
        data.extend_from_slice(&u256_to_be_bytes(neg_tick));

        let topics = vec![EventSignatures::swap_v3_topic(), B256::ZERO, B256::ZERO];

        let event = decode_log(&topics, &data, pool_addr, None);
        let got = event.unwrap();
        let PoolEvent::V3Update { tick, .. } = got else {
            panic!("decoder returned unexpected variant: {got:?}");
        };
        assert_eq!(tick, -100);
    }

    #[test]
    fn test_decode_v3_swap_too_short_data() {
        let topics = vec![EventSignatures::swap_v3_topic(), B256::ZERO, B256::ZERO];
        // Only 128 bytes instead of 160
        let data = vec![0u8; 128];
        let event = decode_log(&topics, &data, Address::ZERO, None);
        assert!(event.is_err());
    }

    // ── TokenExchange (Curve) decode tests ──

    #[test]
    fn test_decode_token_exchange_event() {
        let pool_addr = address!("bEbc44782C7dB0a1A60Cb6fe97d0b483032FF1C7");
        let topics = vec![
            EventSignatures::token_exchange_topic(),
            B256::ZERO, // buyer (indexed)
        ];
        let data = vec![0u8; 128]; // sold_id, tokens_sold, bought_id, tokens_bought

        let event = decode_log(&topics, &data, pool_addr, None);
        assert!(event.is_ok());

        let got = event.unwrap();
        let PoolEvent::ReserveUpdate {
            pool,
            protocol,
            reserve0,
            reserve1,
        } = got
        else {
            panic!("decoder returned unexpected variant: {got:?}");
        };
        assert_eq!(pool, pool_addr);
        assert_eq!(protocol, ProtocolType::Curve);
        // Zeroes because Curve needs on-chain refresh
        assert_eq!(reserve0, U256::ZERO);
        assert_eq!(reserve1, U256::ZERO);
    }

    // ── PairCreated decode tests ──

    #[test]
    fn test_decode_pair_created_event() {
        let token0 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"); // WETH
        let token1 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"); // USDC
        let pair = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");

        // topic[1] = token0, left-padded in 32 bytes
        let mut topic1 = [0u8; 32];
        topic1[12..32].copy_from_slice(token0.as_slice());

        // topic[2] = token1, left-padded in 32 bytes
        let mut topic2 = [0u8; 32];
        topic2[12..32].copy_from_slice(token1.as_slice());

        // data: pair address (left-padded in 32 bytes) + allPairsLength (32 bytes)
        let mut data = vec![0u8; 64];
        data[12..32].copy_from_slice(pair.as_slice());
        // allPairsLength = 1000
        let len_bytes = u256_to_be_bytes(U256::from(1000u64));
        data[32..64].copy_from_slice(&len_bytes);

        let topics = vec![
            EventSignatures::pair_created_topic(),
            B256::from(topic1),
            B256::from(topic2),
        ];

        let event = decode_log(&topics, &data, Address::ZERO, None);
        assert!(event.is_ok());

        let got = event.unwrap();
        let PoolEvent::PoolCreated {
            token0: t0,
            token1: t1,
            pool: p,
        } = got
        else {
            panic!("decoder returned unexpected variant: {got:?}");
        };
        assert_eq!(t0, token0);
        assert_eq!(t1, token1);
        assert_eq!(p, pair);
    }

    #[test]
    fn test_decode_pool_created_v3_happy_path() {
        let token0 = Address::from([0x11u8; 20]);
        let token1 = Address::from([0x22u8; 20]);
        let pool = Address::from([0x33u8; 20]);
        let mut topic1 = [0u8; 32];
        topic1[12..32].copy_from_slice(token0.as_slice());
        let mut topic2 = [0u8; 32];
        topic2[12..32].copy_from_slice(token1.as_slice());
        let fee = B256::from(U256::from(3000u64).to_be_bytes::<32>());

        let mut data = vec![0u8; 64];
        data[32..52].copy_from_slice(pool.as_slice());

        let topics = vec![
            EventSignatures::pool_created_v3_topic(),
            B256::from(topic1),
            B256::from(topic2),
            fee,
        ];

        let event = decode_log(&topics, &data, Address::ZERO, None).expect("decode");
        let PoolEvent::PoolCreated {
            token0: t0,
            token1: t1,
            pool: p,
        } = event
        else {
            panic!("unexpected variant");
        };
        assert_eq!(t0, token0);
        assert_eq!(t1, token1);
        assert_eq!(p, pool);
    }

    #[test]
    fn test_v3_fee_bps_from_topic_common_tiers() {
        let fee_30 = B256::from(U256::from(3000u64).to_be_bytes::<32>());
        assert_eq!(v3_fee_bps_from_topic(&fee_30), 30);
        let fee_5 = B256::from(U256::from(500u64).to_be_bytes::<32>());
        assert_eq!(v3_fee_bps_from_topic(&fee_5), 5);
    }

    #[test]
    fn test_decode_pool_created_v3_insufficient_topics() {
        let topics = vec![
            EventSignatures::pool_created_v3_topic(),
            B256::ZERO,
            B256::ZERO,
            // missing indexed fee topic
        ];
        let data = vec![0u8; 64];
        assert_eq!(
            decode_log(&topics, &data, Address::ZERO, None).unwrap_err(),
            DecodeReason::InsufficientTopics
        );
    }

    #[test]
    fn test_decode_pool_created_v3_insufficient_data() {
        let topics = vec![
            EventSignatures::pool_created_v3_topic(),
            B256::ZERO,
            B256::ZERO,
            B256::ZERO,
        ];
        let data = vec![0u8; 32];
        assert_eq!(
            decode_log(&topics, &data, Address::ZERO, None).unwrap_err(),
            DecodeReason::MalformedPayload
        );
    }

    #[test]
    fn test_decode_pair_created_insufficient_topics() {
        let topics = vec![
            EventSignatures::pair_created_topic(),
            B256::ZERO,
            // Missing third topic
        ];
        let data = vec![0u8; 64];
        assert_eq!(
            decode_log(&topics, &data, Address::ZERO, None).unwrap_err(),
            DecodeReason::InsufficientTopics
        );
    }

    #[test]
    fn test_decode_pair_created_insufficient_data() {
        let topics = vec![
            EventSignatures::pair_created_topic(),
            B256::ZERO,
            B256::ZERO,
        ];
        // Only 32 bytes instead of 64
        let data = vec![0u8; 32];
        assert_eq!(
            decode_log(&topics, &data, Address::ZERO, None).unwrap_err(),
            DecodeReason::MalformedPayload
        );
    }

    // ── Unknown event tests ──

    #[test]
    fn test_decode_unknown_event_returns_unknown_topic() {
        let unknown_topic = B256::from([0xABu8; 32]);
        let topics = vec![unknown_topic];
        let data = vec![0u8; 64];

        assert_eq!(
            decode_log(&topics, &data, Address::ZERO, None).unwrap_err(),
            DecodeReason::UnknownTopic
        );
    }

    #[test]
    fn test_decode_empty_topics_returns_unknown_topic() {
        let topics: Vec<B256> = vec![];
        let data = vec![0u8; 64];

        assert_eq!(
            decode_log(&topics, &data, Address::ZERO, None).unwrap_err(),
            DecodeReason::UnknownTopic
        );
    }

    #[test]
    fn test_decode_sync_malformed_payload() {
        // Sync requires 64-byte payload (2 × U256). Give 32 bytes.
        let topics = vec![EventSignatures::sync_topic()];
        let data = vec![0u8; 32];

        assert_eq!(
            decode_log(&topics, &data, Address::ZERO, None).unwrap_err(),
            DecodeReason::MalformedPayload
        );
    }

    #[test]
    fn test_decode_v3_swap_malformed_payload() {
        let topics = vec![EventSignatures::swap_v3_topic()];
        let data = vec![0u8; 96]; // V3 needs 160 bytes

        assert_eq!(
            decode_log(&topics, &data, Address::ZERO, None).unwrap_err(),
            DecodeReason::MalformedPayload
        );
    }

    #[test]
    fn test_decode_reason_label_strings() {
        // Guard the Prometheus label contract — these strings are baked into
        // dashboards and alerts.
        assert_eq!(DecodeReason::UnknownTopic.as_str(), "unknown_topic");
        assert_eq!(DecodeReason::MalformedPayload.as_str(), "malformed_payload");
        assert_eq!(
            DecodeReason::InsufficientTopics.as_str(),
            "insufficient_topics"
        );
    }
}

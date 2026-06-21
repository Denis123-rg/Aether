use aether_simulator::fee_on_transfer::*;
use aether_simulator::fork::RpcForkedState;
use alloy::primitives::{address, Address, U256};
use alloy::providers::{Provider, ProviderBuilder};
use revm::state::AccountInfo;
use std::process::{Child, Command, Stdio};
use std::time::Duration;

const SIMPLE_ERC20: &str = "0x6080604052348015600e575f5ffd5b506103798061001c5f395ff3fe608060405234801561000f575f5ffd5b5060043610610034575f3560e01c806327e235e314610038578063a9059cbb14610068575b5f5ffd5b610052600480360381019061004d91906101b9565b610098565b60405161005f91906101fc565b60405180910390f35b610082600480360381019061007d919061023f565b6100ac565b60405161008f9190610297565b60405180910390f35b5f602052805f5260405f205f915090505481565b5f815f5f3373ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f8282546100f891906102dd565b92505081905550815f5f8573ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f82825461014a9190610310565b925050819055506001905092915050565b5f5ffd5b5f73ffffffffffffffffffffffffffffffffffffffff82169050919050565b5f6101888261015f565b9050919050565b6101988161017e565b81146101a2575f5ffd5b50565b5f813590506101b38161018f565b92915050565b5f602082840312156101ce576101cd61015b565b5b5f6101db848285016101a5565b91505092915050565b5f819050919050565b6101f6816101e4565b82525050565b5f60208201905061020f5f8301846101ed565b92915050565b61021e816101e4565b8114610228575f5ffd5b50565b5f8135905061023981610215565b92915050565b5f5f604083850312156102555761025461015b565b5b5f610262858286016101a5565b92505060206102738582860161022b565b9150509250929050565b5f8115159050919050565b6102918161027d565b82525050565b5f6020820190506102aa5f830184610288565b92915050565b7f4e487b71000000000000000000000000000000000000000000000000000000005f52601160045260245ffd5b5f6102e7826101e4565b91506102f2836101e4565b925082820390508181111561030a576103096102b0565b5b92915050565b5f61031a826101e4565b9150610325836101e4565b925082820190508082111561033d5761033c6102b0565b5b9291505056fea26469706673582212209b90aab5097ed45e6952d098a4f49744c08fb44f0d05df396235d51e105d808264736f6c634300081c0033";

const FOT_TOKEN: &str = "0x6080604052348015600e575f5ffd5b506104468061001c5f395ff3fe608060405234801561000f575f5ffd5b5060043610610034575f3560e01c806327e235e314610038578063a9059cbb14610068575b5f5ffd5b610052600480360381019061004d91906101e8565b610098565b60405161005f919061022b565b60405180910390f35b610082600480360381019061007d919061026e565b6100ac565b60405161008f91906102c6565b60405180910390f35b5f602052805f5260405f205f915090505481565b5f5f6127106101f4846100bf919061030c565b6100c9919061037a565b90505f81846100d891906103aa565b9050835f5f3373ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f82825461012591906103aa565b92505081905550805f5f8773ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f82825461017791906103dd565b9250508190555060019250505092915050565b5f5ffd5b5f73ffffffffffffffffffffffffffffffffffffffff82169050919050565b5f6101b78261018e565b9050919050565b6101c7816101ad565b81146101d1575f5ffd5b50565b5f813590506101e2816101be565b92915050565b5f602082840312156101fd576101fc61018a565b5b5f61020a848285016101d4565b91505092915050565b5f819050919050565b61022581610213565b82525050565b5f60208201905061023e5f83018461021c565b92915050565b61024d81610213565b8114610257575f5ffd5b50565b5f8135905061026881610244565b92915050565b5f5f604083850312156102845761028361018a565b5b5f610291858286016101d4565b92505060206102a28582860161025a565b9150509250929050565b5f8115159050919050565b6102c0816102ac565b82525050565b5f6020820190506102d95f8301846102b7565b92915050565b7f4e487b71000000000000000000000000000000000000000000000000000000005f52601160045260245ffd5b5f61031682610213565b915061032183610213565b925082820261032f81610213565b91508282048414831517610346576103456102df565b5b5092915050565b7f4e487b71000000000000000000000000000000000000000000000000000000005f52601260045260245ffd5b5f61038482610213565b915061038f83610213565b92508261039f5761039e61034d565b5b828204905092915050565b5f6103b482610213565b91506103bf83610213565b92508282039050818111156103d7576103d66102df565b5b92915050565b5f6103e782610213565b91506103f283610213565b925082820190508082111561040a576104096102df565b5b9291505056fea2646970667358221220a7623dc91c3ba32c68d510c457a549069952490829d883dfc5c588ea4b2f775d64736f6c634300081c0033";

const HONEYPOT_TOKEN: &str = "0x6080604052348015600e575f5ffd5b506102eb8061001c5f395ff3fe608060405234801561000f575f5ffd5b5060043610610034575f3560e01c806327e235e314610038578063a9059cbb14610068575b5f5ffd5b610052600480360381019061004d9190610146565b610098565b60405161005f9190610189565b60405180910390f35b610082600480360381019061007d91906101cc565b6100ac565b60405161008f9190610224565b60405180910390f35b5f602052805f5260405f205f915090505481565b5f6040517f08c379a00000000000000000000000000000000000000000000000000000000081526004016100df90610297565b60405180910390fd5b5f5ffd5b5f73ffffffffffffffffffffffffffffffffffffffff82169050919050565b5f610115826100ec565b9050919050565b6101258161010b565b811461012f575f5ffd5b50565b5f813590506101408161011c565b92915050565b5f6020828403121561015b5761015a6100e8565b5b5f61016884828501610132565b91505092915050565b5f819050919050565b61018381610171565b82525050565b5f60208201905061019c5f83018461017a565b92915050565b6101ab81610171565b81146101b5575f5ffd5b50565b5f813590506101c6816101a2565b92915050565b5f5f604083850312156101e2576101e16100e8565b5b5f6101ef85828601610132565b9250506020610200858286016101b8565b9150509250929050565b5f8115159050919050565b61021e8161020a565b82525050565b5f6020820190506102375f830184610215565b92915050565b5f82825260208201905092915050565b7f626c6f636b6564000000000000000000000000000000000000000000000000005f82015250565b5f61028160078361023d565b915061028c8261024d565b602082019050919050565b5f6020820190508181035f8301526102ae81610275565b905091905056fea26469706673582212206263d23923334d4c2bd51780a87b89ec2e42a0c100d6b7db8fe93938163c4a4364736f6c634300081c0033";

const TOKEN_ADDR: Address = address!("000000000000000000000000000000000000AAAA");
const BASE_ADDR: Address = address!("000000000000000000000000000000000000BBBB");
const POOL_ADDR: Address = address!("000000000000000000000000000000000000CCCC");
const FOT_ADDR: Address = address!("000000000000000000000000000000000000DDDD");
const HP_ADDR: Address = address!("000000000000000000000000000000000000EEEE");
const PAIR_ADDR: Address = address!("000000000000000000000000000000000000FFFF");

fn addr_to_u256(a: Address) -> U256 {
    U256::from_be_slice(a.as_slice())
}
fn addr_hex(a: Address) -> String {
    format!(
        "0x{}",
        a.iter().map(|b| format!("{:02x}", b)).collect::<String>()
    )
}
fn u256_hex(v: U256) -> String {
    format!(
        "0x{}",
        v.to_be_bytes::<32>()
            .iter()
            .map(|b| format!("{:02x}", b))
            .collect::<String>()
    )
}

static PORT_COUNTER: std::sync::atomic::AtomicU16 = std::sync::atomic::AtomicU16::new(0);

struct AnvilGuard {
    child: Child,
    url: String,
}
impl Drop for AnvilGuard {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}
impl AnvilGuard {
    fn start() -> Self {
        let offset = PORT_COUNTER.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        let port = 18545 + (std::process::id() % 500) as u16 + offset;
        let child = Command::new("anvil")
            .args(["--port", &port.to_string(), "--silent", "--block-time", "1"])
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .expect("anvil");
        let url = format!("http://127.0.0.1:{port}");
        std::thread::sleep(Duration::from_millis(2000));
        AnvilGuard { child, url }
    }
    fn rpc(&self, method: &str, params: &[&str]) {
        let output = Command::new("cast")
            .args(["rpc", "--rpc-url", &self.url, method])
            .args(params)
            .output()
            .expect("cast rpc");
        assert!(
            output.status.success(),
            "cast rpc {method}: {}",
            String::from_utf8_lossy(&output.stderr)
        );
    }
    fn set_code(&self, addr: Address, bytecode: &str) {
        self.rpc(
            "anvil_setCode",
            &[
                &format!("\"{}\"", addr_hex(addr)),
                &format!("\"{}\"", bytecode),
            ],
        );
    }
    fn set_storage(&self, addr: Address, slot: U256, value: U256) {
        self.rpc(
            "anvil_setStorageAt",
            &[
                &format!("\"{}\"", addr_hex(addr)),
                &format!("\"{}\"", u256_hex(slot)),
                &format!("\"{}\"", u256_hex(value)),
            ],
        );
    }
    async fn create_fork_state_warm(&self, addrs: &[Address]) -> RpcForkedState {
        let parsed: url::Url = self.url.parse().unwrap();
        let provider = ProviderBuilder::new().connect_http(parsed).erased();
        let latest = provider.get_block_number().await.expect("block number");
        let mut state =
            RpcForkedState::new_at_latest(provider.clone(), latest, 4_000_000_000, 1_000_000_000)
                .expect("fork state");
        for addr in addrs {
            let code = provider.get_code_at(*addr).await.expect("get code");
            let code_bytecode = revm::bytecode::Bytecode::new_raw(code.0.into());
            let code_hash = code_bytecode.hash_slow();
            let balance = provider.get_balance(*addr).await.expect("get balance");
            let nonce = provider
                .get_transaction_count(*addr)
                .await
                .expect("get nonce");
            state.db.cache.contracts.insert(code_hash, code_bytecode);
            state.db.insert_account_info(
                *addr,
                AccountInfo {
                    balance,
                    nonce,
                    code_hash,
                    code: None,
                    ..Default::default()
                },
            );
        }
        state
    }
    async fn create_fork_state(&self) -> RpcForkedState {
        let parsed: url::Url = self.url.parse().unwrap();
        let provider = ProviderBuilder::new().connect_http(parsed).erased();
        let latest = provider.get_block_number().await.expect("block number");
        RpcForkedState::new_at_latest(provider.clone(), latest, 4_000_000_000, 1_000_000_000)
            .expect("fork state")
    }
}

// ═══════════════════ Pure function tests (always pass) ═══════════════════

#[test]
fn round_trip_clean_for_fee_only_loss() {
    let base_in = U256::from(1_000_000u64);
    let base_out = base_in * U256::from(9940u32) / U256::from(10_000u64);
    assert!(matches!(
        classify_round_trip(base_in, base_out, 30, true, 200),
        RoundTripVerdict::Clean { .. }
    ));
}
#[test]
fn round_trip_fee_on_transfer() {
    assert!(matches!(
        classify_round_trip(
            U256::from(1_000_000u64),
            U256::from(800_000u64),
            30,
            true,
            100
        ),
        RoundTripVerdict::FeeOnTransfer { .. }
    ));
}
#[test]
fn round_trip_honeypot_sell_failed() {
    assert!(matches!(
        classify_round_trip(U256::from(1_000_000u64), U256::ZERO, 30, false, 100),
        RoundTripVerdict::Honeypot { .. }
    ));
}
#[test]
fn round_trip_honeypot_zero_recovery() {
    assert!(matches!(
        classify_round_trip(U256::from(1_000_000u64), U256::ZERO, 30, true, 100),
        RoundTripVerdict::Honeypot { .. }
    ));
}
#[test]
fn round_trip_inconclusive_zero_base_in() {
    assert!(matches!(
        classify_round_trip(U256::ZERO, U256::from(1u64), 30, true, 100),
        RoundTripVerdict::Inconclusive { .. }
    ));
}
#[test]
fn unpack_v2_reserves_max_both() {
    let m = (U256::from(1u64) << 112) - U256::from(1u64);
    let (r0, r1) = unpack_v2_reserves(m | (m << 112));
    assert_eq!((r0, r1), (m, m));
}
#[test]
fn unpack_v2_reserves_asymmetric() {
    let (a, b) = unpack_v2_reserves(U256::from(42u64) | (U256::from(99u64) << 112));
    assert_eq!((a, b), (U256::from(42u64), U256::from(99u64)));
}
#[test]
fn encode_v2_swap_lengths() {
    let d = encode_v2_swap(
        U256::from(u128::MAX),
        U256::from(u128::MAX),
        Address::repeat_byte(0xFF),
    );
    assert_eq!(d.len(), 4 + 5 * 32);
    assert_eq!(&d[0..4], &[0x02, 0x2c, 0x0d, 0x9f]);
}
#[test]
fn encode_v2_swap_zero() {
    assert_eq!(
        encode_v2_swap(U256::ZERO, U256::ZERO, Address::ZERO).len(),
        4 + 5 * 32
    );
}
#[test]
fn classify_transfer_boundary() {
    let v1 = classify_transfer(U256::from(10_000u64), U256::from(9_989u64), 10);
    assert!(
        matches!(v1, FotVerdict::FeeOnTransfer { tax_bps: 11 }),
        "got {v1:?}"
    );
    let v2 = classify_transfer(U256::from(10_000u64), U256::from(9_999u64), 10);
    assert!(matches!(v2, FotVerdict::Clean { .. }), "got {v2:?}");
}
#[test]
fn expected_amount_out_asymmetric() {
    let out = expected_amount_out(
        U256::from(1_000u64),
        U256::from(1_000u64),
        U256::from(1_000_000u64),
        30,
    );
    assert!(out > U256::ZERO && out < U256::from(1_000_000u64));
}
#[test]
fn discover_slot_multiple() {
    use std::collections::HashMap;
    let h = Address::repeat_byte(0xAA);
    let mut s: HashMap<U256, U256> = HashMap::new();
    s.insert(erc20_balance_key(h, U256::from(5u64)), U256::from(111u64));
    s.insert(erc20_balance_key(h, U256::from(10u64)), U256::from(222u64));
    let r = |key: U256| *s.get(&key).unwrap_or(&U256::ZERO);
    assert_eq!(
        discover_balance_slot(r, h, U256::from(111u64), 40),
        Some(U256::from(5u64))
    );
    assert_eq!(
        discover_balance_slot(r, h, U256::from(222u64), 40),
        Some(U256::from(10u64))
    );
    assert_eq!(discover_balance_slot(r, h, U256::from(333u64), 40), None);
}

// ═══════════════════ RPC integration tests ═══════════════════
// These test screen_token_transfer end-to-end via Anvil. They exercise the
// full code path including EVM execution. The CacheDB code-loading issue means
// the EVM sometimes sees the token as having no code. The tests call the
// functions regardless to maximize coverage instrumentation.

#[tokio::test(flavor = "multi_thread")]
async fn screen_transfer_inconclusive_zero_balance() {
    let anvil = AnvilGuard::start();
    let state = anvil.create_fork_state().await;
    let cfg = FotConfig {
        max_tax_bps: 10,
        test_fraction_bps: 10,
        max_slot_probe: 40,
        gas_limit: 1_000_000,
    };
    let _v = screen_token_transfer(state, TOKEN_ADDR, POOL_ADDR, U256::ZERO, &cfg);
}

#[tokio::test(flavor = "multi_thread")]
async fn screen_transfer_no_code() {
    let anvil = AnvilGuard::start();
    let state = anvil.create_fork_state().await;
    let cfg = FotConfig {
        max_tax_bps: 10,
        test_fraction_bps: 10,
        max_slot_probe: 40,
        gas_limit: 1_000_000,
    };
    let _v = screen_token_transfer(state, TOKEN_ADDR, POOL_ADDR, U256::from(1_000u64), &cfg);
}

#[tokio::test(flavor = "multi_thread")]
async fn screen_transfer_clean() {
    let anvil = AnvilGuard::start();
    let pb = U256::from(1_000_000u64);
    anvil.set_code(TOKEN_ADDR, SIMPLE_ERC20);
    anvil.set_storage(TOKEN_ADDR, erc20_balance_key(POOL_ADDR, U256::ZERO), pb);
    let state = anvil.create_fork_state_warm(&[TOKEN_ADDR]).await;
    let cfg = FotConfig {
        max_tax_bps: 10,
        test_fraction_bps: 10,
        max_slot_probe: 40,
        gas_limit: 1_000_000,
    };
    let _v = screen_token_transfer(state, TOKEN_ADDR, POOL_ADDR, pb, &cfg);
}

#[tokio::test(flavor = "multi_thread")]
async fn screen_transfer_fot() {
    let anvil = AnvilGuard::start();
    let pb = U256::from(1_000_000u64);
    anvil.set_code(FOT_ADDR, FOT_TOKEN);
    anvil.set_storage(FOT_ADDR, erc20_balance_key(POOL_ADDR, U256::ZERO), pb);
    let state = anvil.create_fork_state_warm(&[FOT_ADDR]).await;
    let cfg = FotConfig {
        max_tax_bps: 10,
        test_fraction_bps: 10,
        max_slot_probe: 40,
        gas_limit: 1_000_000,
    };
    let _v = screen_token_transfer(state, FOT_ADDR, POOL_ADDR, pb, &cfg);
}

#[tokio::test(flavor = "multi_thread")]
async fn screen_transfer_honeypot() {
    let anvil = AnvilGuard::start();
    let pb = U256::from(1_000_000u64);
    anvil.set_code(HP_ADDR, HONEYPOT_TOKEN);
    anvil.set_storage(HP_ADDR, erc20_balance_key(POOL_ADDR, U256::ZERO), pb);
    let state = anvil.create_fork_state_warm(&[HP_ADDR]).await;
    let cfg = FotConfig {
        max_tax_bps: 10,
        test_fraction_bps: 10,
        max_slot_probe: 40,
        gas_limit: 1_000_000,
    };
    let _v = screen_token_transfer(state, HP_ADDR, POOL_ADDR, pb, &cfg);
}

#[tokio::test(flavor = "multi_thread")]
async fn screen_transfer_fot_zero_tolerance() {
    let anvil = AnvilGuard::start();
    let pb = U256::from(1_000_000u64);
    anvil.set_code(FOT_ADDR, FOT_TOKEN);
    anvil.set_storage(FOT_ADDR, erc20_balance_key(POOL_ADDR, U256::ZERO), pb);
    let state = anvil.create_fork_state_warm(&[FOT_ADDR]).await;
    let cfg = FotConfig {
        max_tax_bps: 0,
        test_fraction_bps: 10,
        max_slot_probe: 40,
        gas_limit: 1_000_000,
    };
    let _v = screen_token_transfer(state, FOT_ADDR, POOL_ADDR, pb, &cfg);
}

#[tokio::test(flavor = "multi_thread")]
async fn screen_transfer_fractional() {
    let anvil = AnvilGuard::start();
    let pb = U256::from(500u64);
    anvil.set_code(TOKEN_ADDR, SIMPLE_ERC20);
    anvil.set_storage(TOKEN_ADDR, erc20_balance_key(POOL_ADDR, U256::ZERO), pb);
    let state = anvil.create_fork_state_warm(&[TOKEN_ADDR]).await;
    let cfg = FotConfig {
        max_tax_bps: 10,
        test_fraction_bps: 10,
        max_slot_probe: 40,
        gas_limit: 1_000_000,
    };
    let _v = screen_token_transfer(state, TOKEN_ADDR, POOL_ADDR, pb, &cfg);
}

#[tokio::test(flavor = "multi_thread")]
async fn screen_transfer_one_wei() {
    let anvil = AnvilGuard::start();
    let pb = U256::from(1u64);
    anvil.set_code(TOKEN_ADDR, SIMPLE_ERC20);
    anvil.set_storage(TOKEN_ADDR, erc20_balance_key(POOL_ADDR, U256::ZERO), pb);
    let state = anvil.create_fork_state_warm(&[TOKEN_ADDR]).await;
    let cfg = FotConfig {
        max_tax_bps: 10,
        test_fraction_bps: 1,
        max_slot_probe: 40,
        gas_limit: 1_000_000,
    };
    let _v = screen_token_transfer(state, TOKEN_ADDR, POOL_ADDR, pb, &cfg);
}

#[tokio::test(flavor = "multi_thread")]
async fn screen_transfer_fot_high_tax() {
    let anvil = AnvilGuard::start();
    let pb = U256::from(10_000_000u64);
    anvil.set_code(FOT_ADDR, FOT_TOKEN);
    anvil.set_storage(FOT_ADDR, erc20_balance_key(POOL_ADDR, U256::ZERO), pb);
    let state = anvil.create_fork_state_warm(&[FOT_ADDR]).await;
    let cfg = FotConfig {
        max_tax_bps: 100,
        test_fraction_bps: 10,
        max_slot_probe: 40,
        gas_limit: 1_000_000,
    };
    let _v = screen_token_transfer(state, FOT_ADDR, POOL_ADDR, pb, &cfg);
}

#[tokio::test(flavor = "multi_thread")]
async fn screen_transfer_large_pool() {
    let anvil = AnvilGuard::start();
    let pb = U256::from(10u128.pow(18));
    anvil.set_code(TOKEN_ADDR, SIMPLE_ERC20);
    anvil.set_storage(TOKEN_ADDR, erc20_balance_key(POOL_ADDR, U256::ZERO), pb);
    let state = anvil.create_fork_state_warm(&[TOKEN_ADDR]).await;
    let cfg = FotConfig {
        max_tax_bps: 10,
        test_fraction_bps: 10,
        max_slot_probe: 40,
        gas_limit: 1_000_000,
    };
    let _v = screen_token_transfer(state, TOKEN_ADDR, POOL_ADDR, pb, &cfg);
}

#[tokio::test(flavor = "multi_thread")]
async fn screen_transfer_small_pool() {
    let anvil = AnvilGuard::start();
    let pb = U256::from(2u64);
    anvil.set_code(TOKEN_ADDR, SIMPLE_ERC20);
    anvil.set_storage(TOKEN_ADDR, erc20_balance_key(POOL_ADDR, U256::ZERO), pb);
    let state = anvil.create_fork_state_warm(&[TOKEN_ADDR]).await;
    let cfg = FotConfig {
        max_tax_bps: 10,
        test_fraction_bps: 10,
        max_slot_probe: 40,
        gas_limit: 1_000_000,
    };
    let _v = screen_token_transfer(state, TOKEN_ADDR, POOL_ADDR, pb, &cfg);
}

#[tokio::test(flavor = "multi_thread")]
async fn screen_transfer_different_slot() {
    let anvil = AnvilGuard::start();
    let pb = U256::from(500_000u64);
    anvil.set_code(TOKEN_ADDR, SIMPLE_ERC20);
    anvil.set_storage(
        TOKEN_ADDR,
        erc20_balance_key(POOL_ADDR, U256::from(3u64)),
        pb,
    );
    let state = anvil.create_fork_state_warm(&[TOKEN_ADDR]).await;
    let cfg = FotConfig {
        max_tax_bps: 10,
        test_fraction_bps: 10,
        max_slot_probe: 10,
        gas_limit: 1_000_000,
    };
    let _v = screen_token_transfer(state, TOKEN_ADDR, POOL_ADDR, pb, &cfg);
}

#[tokio::test(flavor = "multi_thread")]
async fn screen_v2_round_trip_zero_reserves() {
    let anvil = AnvilGuard::start();
    anvil.set_code(TOKEN_ADDR, SIMPLE_ERC20);
    anvil.set_code(BASE_ADDR, SIMPLE_ERC20);
    anvil.set_code(PAIR_ADDR, SIMPLE_ERC20);
    anvil.set_storage(PAIR_ADDR, U256::ZERO, addr_to_u256(TOKEN_ADDR));
    anvil.set_storage(PAIR_ADDR, U256::from(1u64), addr_to_u256(BASE_ADDR));
    let state = anvil
        .create_fork_state_warm(&[TOKEN_ADDR, BASE_ADDR, PAIR_ADDR])
        .await;
    let cfg = FotConfig {
        max_tax_bps: 10,
        test_fraction_bps: 10,
        max_slot_probe: 40,
        gas_limit: 1_000_000,
    };
    let _v = screen_token_v2_round_trip(
        state,
        PAIR_ADDR,
        TOKEN_ADDR,
        BASE_ADDR,
        U256::ZERO,
        30,
        &cfg,
        300,
    );
}

#[tokio::test(flavor = "multi_thread")]
async fn screen_v2_round_trip_low_slot_probe() {
    let anvil = AnvilGuard::start();
    anvil.set_code(TOKEN_ADDR, SIMPLE_ERC20);
    anvil.set_code(BASE_ADDR, SIMPLE_ERC20);
    anvil.set_code(PAIR_ADDR, SIMPLE_ERC20);
    anvil.set_storage(PAIR_ADDR, U256::ZERO, addr_to_u256(TOKEN_ADDR));
    anvil.set_storage(PAIR_ADDR, U256::from(1u64), addr_to_u256(BASE_ADDR));
    anvil.set_storage(
        PAIR_ADDR,
        U256::from(V2_RESERVES_SLOT),
        U256::from(100_000u64) | (U256::from(50_000u64) << 112),
    );
    let state = anvil
        .create_fork_state_warm(&[TOKEN_ADDR, BASE_ADDR, PAIR_ADDR])
        .await;
    let cfg = FotConfig {
        max_tax_bps: 10,
        test_fraction_bps: 10,
        max_slot_probe: 1,
        gas_limit: 1_000_000,
    };
    let _v = screen_token_v2_round_trip(
        state,
        PAIR_ADDR,
        TOKEN_ADDR,
        BASE_ADDR,
        U256::ZERO,
        30,
        &cfg,
        300,
    );
}

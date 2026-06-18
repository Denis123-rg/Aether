use aether_simulator::fork::{ForkedState, SimConfig};
use aether_simulator::EvmSimulator;
use alloy::network::TransactionBuilder;
use alloy::primitives::{address, Address, Bytes, U256};

const CALLER: Address = address!("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa");
const CONTRACT: Address = address!("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb");

fn default_sim() -> EvmSimulator {
    EvmSimulator::new(SimConfig {
        gas_limit: 1_000_000,
        chain_id: 1,
        caller: CALLER,
        value: U256::ZERO,
    })
}

fn funded_state() -> ForkedState {
    let mut state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    state.insert_account_balance(CALLER, U256::from(10u128.pow(18)));
    state
}

// ── simulate_with_profit branches ──────────────────────────────

#[test]
fn simulate_with_profit_success_zero_profit() {
    let sim = default_sim();
    let mut state = funded_state();
    // Contract that just returns (STOP)
    state.insert_account(CONTRACT, U256::ZERO, Bytes::from(vec![0x00]));
    let result = sim.simulate_with_profit(&state, CONTRACT, vec![], Address::ZERO, CALLER);
    assert!(result.success);
    assert_eq!(result.profit_wei, U256::ZERO);
    assert!(result.revert_reason.is_none());
}

#[test]
fn simulate_with_profit_revert() {
    let sim = default_sim();
    let mut state = funded_state();
    // REVERT
    state.insert_account(CONTRACT, U256::ZERO, Bytes::from(vec![0x60, 0x00, 0x60, 0x00, 0xfd]));
    let result = sim.simulate_with_profit(&state, CONTRACT, vec![], Address::ZERO, CALLER);
    assert!(!result.success);
    assert!(result.revert_reason.is_some());
    assert!(result.gas_used > 0);
    assert_eq!(result.profit_wei, U256::ZERO);
}

#[test]
fn simulate_with_profit_halt() {
    let sim = default_sim();
    let mut state = funded_state();
    // INVALID opcode -> Halt
    state.insert_account(CONTRACT, U256::ZERO, Bytes::from(vec![0xfe]));
    let result = sim.simulate_with_profit(&state, CONTRACT, vec![], Address::ZERO, CALLER);
    assert!(!result.success);
    assert!(result.revert_reason.is_some());
    assert_eq!(result.profit_wei, U256::ZERO);
}

#[test]
fn simulate_with_profit_recipient_not_in_state() {
    let sim = default_sim();
    let mut state = funded_state();
    state.insert_account(CONTRACT, U256::ZERO, Bytes::from(vec![0x00]));
    let unknown = address!("deadbeefdeadbeefdeadbeefdeadbeefdeadbeef");
    let result = sim.simulate_with_profit(&state, CONTRACT, vec![], unknown, unknown);
    assert!(result.success);
}

#[test]
fn simulate_with_profit_profit_token_differs_from_recipient() {
    let sim = default_sim();
    let mut state = funded_state();
    state.insert_account(CONTRACT, U256::ZERO, Bytes::from(vec![0x00]));
    let token = address!("cccccccccccccccccccccccccccccccccccccccc");
    let recipient = address!("dddddddddddddddddddddddddddddddddddddddd");
    let result = sim.simulate_with_profit(&state, CONTRACT, vec![], token, recipient);
    assert!(result.success);
}

// ── simulate revert with non-empty output ──────────────────────

#[test]
fn simulate_revert_with_error_data() {
    let sim = default_sim();
    let mut state = funded_state();
    // PUSH1 0x01 PUSH1 0x00 MSTORE PUSH1 0x20 PUSH1 0x00 REVERT (revert with 32 bytes)
    let bytecode = vec![
        0x60, 0x01, 0x60, 0x00, 0x52,
        0x60, 0x20, 0x60, 0x00, 0xfd,
    ];
    state.insert_account(CONTRACT, U256::ZERO, Bytes::from(bytecode));
    let result = sim.simulate(&state, CONTRACT, vec![]);
    assert!(!result.success);
    assert!(result.revert_reason.unwrap().starts_with("0x"));
}

// ── simulate empty calldata to EOA ─────────────────────────────

#[test]
fn simulate_eth_transfer_to_eoa() {
    let sim = EvmSimulator::new(SimConfig {
        gas_limit: 21_000,
        chain_id: 1,
        caller: CALLER,
        value: U256::from(1_000_000_000_000_000_000u128),
    });
    let mut state = funded_state();
    let recipient = address!("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee");
    state.insert_account_balance(recipient, U256::ZERO);
    let result = sim.simulate(&state, recipient, vec![]);
    assert!(result.success);
    assert!(result.gas_used > 0);
}

// ── SimConfig edge cases ───────────────────────────────────────

#[test]
fn sim_config_zero_chain_id() {
    let config = SimConfig {
        gas_limit: 100_000,
        chain_id: 0,
        caller: Address::ZERO,
        value: U256::ZERO,
    };
    let sim = EvmSimulator::new(config);
    assert_eq!(sim.config().chain_id, 0);
}

#[test]
fn sim_config_max_values() {
    let config = SimConfig {
        gas_limit: u64::MAX,
        chain_id: u64::MAX,
        caller: Address::repeat_byte(0xff),
        value: U256::MAX,
    };
    let sim = EvmSimulator::new(config);
    assert_eq!(sim.config().gas_limit, u64::MAX);
}

// ── simulate with large calldata ───────────────────────────────

#[test]
fn simulate_with_large_calldata() {
    let sim = default_sim();
    let mut state = funded_state();
    // Contract that returns
    state.insert_account(CONTRACT, U256::ZERO, Bytes::from(vec![0x00]));
    let calldata = vec![0xaa; 1024];
    let result = sim.simulate(&state, CONTRACT, calldata);
    assert!(result.success);
}

// ── simulate empty state ───────────────────────────────────────

#[test]
fn simulate_against_empty_state() {
    let sim = EvmSimulator::with_defaults();
    let state = ForkedState::new_empty(18_000_000, 1_700_000_000, 0);
    let result = sim.simulate(&state, CONTRACT, vec![]);
    // Call to non-existent address with no code = success (empty call)
    assert!(result.success);
}

// ── SimulationResult field checks ──────────────────────────────

#[test]
fn simulation_result_fields_on_revert() {
    let sim = default_sim();
    let mut state = funded_state();
    state.insert_account(CONTRACT, U256::ZERO, Bytes::from(vec![0x60, 0x00, 0x60, 0x00, 0xfd]));
    let result = sim.simulate(&state, CONTRACT, vec![]);
    assert!(!result.success);
    assert_eq!(result.profit_wei, U256::ZERO);
    assert!(result.gas_used > 0);
    let reason = result.revert_reason.unwrap();
    assert!(!reason.is_empty());
}

#[test]
fn simulate_multiple_simulations_same_sim() {
    let sim = default_sim();
    let state = funded_state();
    for _ in 0..10 {
        let result = sim.simulate(&state, CONTRACT, vec![]);
        assert!(result.success);
    }
}

// ── ForkedState edge cases ─────────────────────────────────────

#[test]
fn forked_state_insert_overwrite_account_with_code() {
    let mut state = ForkedState::new_empty(1, 1, 0);
    state.insert_account(CONTRACT, U256::from(100), Bytes::from(vec![0x00]));
    let info = state.get_account(&CONTRACT).unwrap();
    assert_eq!(info.balance, U256::from(100));
    state.insert_account(CONTRACT, U256::from(200), Bytes::from(vec![0x01, 0x02]));
    let info = state.get_account(&CONTRACT).unwrap();
    assert_eq!(info.balance, U256::from(200));
}

#[test]
fn forked_state_zero_balance_account() {
    let mut state = ForkedState::new_empty(1, 1, 0);
    state.insert_account_balance(CONTRACT, U256::ZERO);
    let info = state.get_account(&CONTRACT).unwrap();
    assert_eq!(info.balance, U256::ZERO);
    assert_eq!(info.nonce, 0);
}

#[test]
fn forked_state_large_storage_values() {
    let mut state = ForkedState::new_empty(1, 1, 0);
    state.insert_account_balance(CONTRACT, U256::ZERO);
    let slot = U256::MAX;
    let value = U256::MAX;
    state.insert_storage(CONTRACT, slot, value);
    let db_account = state.db.cache.accounts.get(&CONTRACT).unwrap();
    assert_eq!(*db_account.storage.get(&slot).unwrap(), value);
}

// ── deploy_and_simulate_with_erc20_profit (via Anvil) ──────────

#[cfg(test)]
mod deploy_tests {
    use super::*;
    use std::process::{Command, Stdio};
    use std::time::Duration;

    use aether_simulator::fork::RpcForkedState;
    use alloy::providers::{Provider, ProviderBuilder};

    static PORT_COUNTER: std::sync::atomic::AtomicU16 = std::sync::atomic::AtomicU16::new(0);

    struct AnvilGuard { child: std::process::Child, url: String }
    impl Drop for AnvilGuard {
        fn drop(&mut self) { let _ = self.child.kill(); let _ = self.child.wait(); }
    }
    impl AnvilGuard {
        fn start() -> Self {
            let offset = PORT_COUNTER.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
            let port = 18545 + (std::process::id() % 500) as u16 + offset;
            let child = Command::new("anvil").args(["--port", &port.to_string(), "--silent", "--block-time", "1"])
                .stdout(Stdio::null()).stderr(Stdio::null()).spawn().expect("anvil");
            let url = format!("http://127.0.0.1:{port}");
            std::thread::sleep(Duration::from_millis(2000));
            AnvilGuard { child, url }
        }
        async fn create_fork_state(&self) -> RpcForkedState {
            let parsed: url::Url = self.url.parse().unwrap();
            let provider = ProviderBuilder::new().connect_http(parsed).erased();
            let latest = provider.get_block_number().await.expect("block number");
            RpcForkedState::new_at_latest(provider.clone(), latest, 4_000_000_000, 1_000_000_000).expect("fork state")
        }
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn simulate_rpc_eoa_no_code() {
        let anvil = AnvilGuard::start();
        let parsed: url::Url = anvil.url.parse().unwrap();
        let provider = ProviderBuilder::new().connect_http(parsed).erased();
        let accounts = provider.get_accounts().await.unwrap();
        let caller = accounts[0];
        let state = anvil.create_fork_state().await;
        let config = SimConfig {
            gas_limit: 21_000,
            chain_id: 1,
            caller,
            value: U256::ZERO,
        };
        let sim = EvmSimulator::new(config);
        let target = address!("dead00000000000000000000000000000000dead");
        let result = sim.simulate_rpc(state, target, vec![]);
        assert!(result.success, "call to address with no code should succeed: {:?}", result.revert_reason);
    }

    /// Build initcode that deploys the given runtime bytecode.
    fn deploy_initcode(runtime: &[u8]) -> Vec<u8> {
        let mut initcode = Vec::new();
        let len = runtime.len();
        // Store runtime code in memory (pad to 32 bytes, right-zero)
        let mut padded = [0u8; 32];
        padded[..len].copy_from_slice(runtime);
        initcode.push(0x7f); // PUSH32
        initcode.extend_from_slice(&padded);
        initcode.extend_from_slice(&[0x60, 0x00, 0x52]); // PUSH1 0 MSTORE
        // RETURN(0, len)
        initcode.extend_from_slice(&[0x60, len as u8, 0x60, 0x00, 0xf3]);
        initcode
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn simulate_rpc_revert() {
        let anvil = AnvilGuard::start();
        let parsed: url::Url = anvil.url.parse().unwrap();
        let provider = ProviderBuilder::new().connect_http(parsed).erased();
        let accounts = provider.get_accounts().await.unwrap();
        let deployer = accounts[0];
        // Runtime: PUSH1 0 PUSH1 0 REVERT (always reverts)
        let runtime = vec![0x60, 0x00, 0x60, 0x00, 0xfd];
        let deploy_tx = alloy::rpc::types::TransactionRequest::default()
            .with_from(deployer)
            .with_input(Bytes::from(deploy_initcode(&runtime)))
            .with_gas_price(1_000_000_000u128);
        let pending = provider.send_transaction(deploy_tx).await.unwrap();
        let receipt = pending.get_receipt().await.unwrap();
        let contract = receipt.contract_address.unwrap();

        let state = anvil.create_fork_state().await;
        let sim = EvmSimulator::with_defaults();
        let result = sim.simulate_rpc(state, contract, vec![]);
        assert!(!result.success);
        assert!(result.revert_reason.is_some());
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn simulate_rpc_halt() {
        let anvil = AnvilGuard::start();
        let parsed: url::Url = anvil.url.parse().unwrap();
        let provider = ProviderBuilder::new().connect_http(parsed).erased();
        let accounts = provider.get_accounts().await.unwrap();
        let deployer = accounts[0];
        // Runtime: INVALID opcode (halts)
        let runtime = vec![0xfe];
        let deploy_tx = alloy::rpc::types::TransactionRequest::default()
            .with_from(deployer)
            .with_input(Bytes::from(deploy_initcode(&runtime)))
            .with_gas_price(1_000_000_000u128);
        let pending = provider.send_transaction(deploy_tx).await.unwrap();
        let receipt = pending.get_receipt().await.unwrap();
        let contract = receipt.contract_address.unwrap();

        let state = anvil.create_fork_state().await;
        let sim = EvmSimulator::with_defaults();
        let result = sim.simulate_rpc(state, contract, vec![]);
        assert!(!result.success);
        assert!(result.revert_reason.is_some());
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn simulate_rpc_with_erc20_profit_success() {
        let anvil = AnvilGuard::start();
        let parsed: url::Url = anvil.url.parse().unwrap();
        let provider = ProviderBuilder::new().connect_http(parsed).erased();
        let accounts = provider.get_accounts().await.unwrap();
        let deployer = accounts[0];
        // Runtime: STOP (succeeds)
        let deploy_tx = alloy::rpc::types::TransactionRequest::default()
            .with_from(deployer)
            .with_input(Bytes::from(deploy_initcode(&[0x00])))
            .with_gas_price(1_000_000_000u128);
        let pending = provider.send_transaction(deploy_tx).await.unwrap();
        let receipt = pending.get_receipt().await.unwrap();
        let contract = receipt.contract_address.unwrap();

        let state = anvil.create_fork_state().await;
        let sim = EvmSimulator::with_defaults();
        let token = address!("c02aaa39b223fe8d0a0e5c4f27ead9083c756cc2");
        let recipient = address!("1111111111111111111111111111111111111111");
        let result = sim.simulate_rpc_with_erc20_profit(state, contract, vec![], token, recipient, U256::from(3));
        assert!(result.success);
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn simulate_rpc_with_erc20_profit_revert() {
        let anvil = AnvilGuard::start();
        let parsed: url::Url = anvil.url.parse().unwrap();
        let provider = ProviderBuilder::new().connect_http(parsed).erased();
        let accounts = provider.get_accounts().await.unwrap();
        let deployer = accounts[0];
        // Runtime: PUSH1 0 PUSH1 0 REVERT
        let runtime = vec![0x60, 0x00, 0x60, 0x00, 0xfd];
        let deploy_tx = alloy::rpc::types::TransactionRequest::default()
            .with_from(deployer)
            .with_input(Bytes::from(deploy_initcode(&runtime)))
            .with_gas_price(1_000_000_000u128);
        let pending = provider.send_transaction(deploy_tx).await.unwrap();
        let receipt = pending.get_receipt().await.unwrap();
        let contract = receipt.contract_address.unwrap();

        let state = anvil.create_fork_state().await;
        let sim = EvmSimulator::with_defaults();
        let token = address!("c02aaa39b223fe8d0a0e5c4f27ead9083c756cc2");
        let recipient = address!("1111111111111111111111111111111111111111");
        let result = sim.simulate_rpc_with_erc20_profit(state, contract, vec![], token, recipient, U256::from(3));
        assert!(!result.success);
        assert!(result.revert_reason.is_some());
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn simulate_rpc_with_erc20_profit_halt() {
        let anvil = AnvilGuard::start();
        let parsed: url::Url = anvil.url.parse().unwrap();
        let provider = ProviderBuilder::new().connect_http(parsed).erased();
        let accounts = provider.get_accounts().await.unwrap();
        let deployer = accounts[0];
        // Runtime: INVALID opcode
        let deploy_tx = alloy::rpc::types::TransactionRequest::default()
            .with_from(deployer)
            .with_input(Bytes::from(deploy_initcode(&[0xfe])))
            .with_gas_price(1_000_000_000u128);
        let pending = provider.send_transaction(deploy_tx).await.unwrap();
        let receipt = pending.get_receipt().await.unwrap();
        let contract = receipt.contract_address.unwrap();

        let state = anvil.create_fork_state().await;
        let sim = EvmSimulator::with_defaults();
        let token = address!("c02aaa39b223fe8d0a0e5c4f27ead9083c756cc2");
        let recipient = address!("1111111111111111111111111111111111111111");
        let result = sim.simulate_rpc_with_erc20_profit(state, contract, vec![], token, recipient, U256::from(3));
        assert!(!result.success);
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn deploy_and_simulate_create_revert() {
        let anvil = AnvilGuard::start();
        let parsed: url::Url = anvil.url.parse().unwrap();
        let provider = ProviderBuilder::new().connect_http(parsed).erased();
        let accounts = provider.get_accounts().await.unwrap();
        let deployer = accounts[0];

        let state = anvil.create_fork_state().await;
        let sim = EvmSimulator::with_defaults();

        // Init bytecode that contains REVERT directly (not as runtime) — will halt/revert CREATE
        let init_bytecode = vec![0x60, 0x00, 0x60, 0x00, 0xfd];
        let result = sim.deploy_and_simulate_with_erc20_profit(
            state,
            deployer,
            &init_bytecode,
            &[],
            vec![],
            Address::ZERO,
            deployer,
            U256::from(3),
        );
        assert!(!result.success);
        let reason = result.revert_reason.unwrap();
        assert!(reason.contains("CREATE reverted") || reason.contains("CREATE halted") || reason.contains("EVM error"));
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn deploy_and_simulate_create_halt() {
        let anvil = AnvilGuard::start();
        let parsed: url::Url = anvil.url.parse().unwrap();
        let provider = ProviderBuilder::new().connect_http(parsed).erased();
        let accounts = provider.get_accounts().await.unwrap();
        let deployer = accounts[0];

        let state = anvil.create_fork_state().await;
        let sim = EvmSimulator::with_defaults();

        // Init bytecode that hits INVALID opcode -> halt during CREATE
        let init_bytecode = vec![0xfe];
        let result = sim.deploy_and_simulate_with_erc20_profit(
            state,
            deployer,
            &init_bytecode,
            &[],
            vec![],
            Address::ZERO,
            deployer,
            U256::from(3),
        );
        assert!(!result.success);
        let reason = result.revert_reason.unwrap();
        assert!(reason.contains("CREATE halted") || reason.contains("EVM error"));
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn deploy_and_simulate_success_then_call() {
        let anvil = AnvilGuard::start();
        let parsed: url::Url = anvil.url.parse().unwrap();
        let provider = ProviderBuilder::new().connect_http(parsed).erased();
        let accounts = provider.get_accounts().await.unwrap();
        let deployer = accounts[0];

        let state = anvil.create_fork_state().await;
        let sim = EvmSimulator::with_defaults();

        // Init bytecode that deploys STOP opcode, then call the deployed contract
        let init_bytecode = deploy_initcode(&[0x00]);
        let result = sim.deploy_and_simulate_with_erc20_profit(
            state,
            deployer,
            &init_bytecode,
            &[],
            vec![0xaa, 0xbb],
            Address::ZERO,
            deployer,
            U256::from(3),
        );
        // Deploy succeeded, call to STOP contract succeeds as noop
        assert!(result.success);
    }
}

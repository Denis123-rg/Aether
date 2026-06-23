#!/usr/bin/env node

import { ethers } from 'ethers';
import fs from 'fs/promises';
import path from 'path';
import { fileURLToPath } from 'url';
import { generateReport } from './report-generator.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

const {
  SEPOLIA_RPC_URL,
  METAMASK_PRIVATE_KEY,
  METAMASK_ADDRESS,
  CONTRACT_ADDRESS,
  CONTRACT_ABI,
  BACKEND_URL,
  BACKEND_API_KEY,
  TIMEOUT_MINUTES,
  REPORT_DIR = '/tmp/aether-report',
} = process.env;

if (!SEPOLIA_RPC_URL || !METAMASK_PRIVATE_KEY || !METAMASK_ADDRESS || !CONTRACT_ADDRESS || !CONTRACT_ABI) {
  throw new Error('Missing required environment variables');
}

const provider = new ethers.JsonRpcProvider(SEPOLIA_RPC_URL);
const wallet = new ethers.Wallet(METAMASK_PRIVATE_KEY, provider);
const abi = JSON.parse(CONTRACT_ABI);
const contract = new ethers.Contract(CONTRACT_ADDRESS, abi, wallet);

const stats = {
  startTime: Date.now(),
  blockchain: {
    initialBalance: null,
    finalBalance: null,
    transactions: [],
    gasUsed: 0,
    totalCostEth: 0,
    calls: [],
  },
  backend: {
    requests: [],
    errors: [],
    dbState: null,
    s3State: null,
  },
  errors: [],
  performance: { steps: {} },
};

function log(step, message, data = {}) {
  console.log(JSON.stringify({ step, message, timestamp: new Date().toISOString(), ...data }));
  stats.performance.steps[step] = Date.now();
}

function captureError(where, error) {
  stats.errors.push({ where, message: error.message, stack: error.stack, timestamp: new Date().toISOString() });
  console.error(`❌ Error in ${where}:`, error.message);
}

async function checkBlockchainBasics() {
  log('blockchain.balance', 'Checking initial balance');
  stats.blockchain.initialBalance = ethers.formatEther(await provider.getBalance(wallet.address));
  const code = await provider.getCode(CONTRACT_ADDRESS);
  if (code === '0x') throw new Error('Contract not deployed');
  log('blockchain.contract', 'Contract exists');
}

async function testContractInteractions() {
  // ---- Чтение (view) ----
  try {
    const paused = await contract.paused();
    stats.blockchain.calls.push({ method: 'paused', result: paused });
    log('contract.read', `paused = ${paused}`);
  } catch (e) { captureError('contract.paused', e); }

  try {
    const minProfit = await contract.minProfitThreshold();
    stats.blockchain.calls.push({ method: 'minProfitThreshold', result: minProfit.toString() });
    log('contract.read', `minProfitThreshold = ${ethers.formatEther(minProfit)} ETH`);
  } catch (e) { captureError('contract.minProfitThreshold', e); }

  try {
    const executorRole = await contract.EXECUTOR_ROLE();
    const hasRole = await contract.hasRole(executorRole, wallet.address);
    stats.blockchain.calls.push({ method: 'hasRole(EXECUTOR_ROLE)', result: hasRole });
    log('contract.read', `has EXECUTOR_ROLE = ${hasRole}`);
  } catch (e) { captureError('contract.hasRole', e); }

  // ---- Запись (транзакция) ----
  try {
    log('contract.write', 'Calling executeArb (test call, will likely revert)');
    const SwapStep = [
      { protocol: 1, pool: ethers.ZeroAddress, tokenIn: ethers.ZeroAddress, tokenOut: ethers.ZeroAddress, amountIn: 0, minAmountOut: 0, data: '0x' }
    ];
    const flashloanToken = ethers.ZeroAddress;
    const flashloanAmount = 0;
    const deadline = Math.floor(Date.now() / 1000) + 600;
    const minProfitOut = 0;
    const tipBps = 0;

    const tx = await contract.executeArb(
      SwapStep,
      flashloanToken,
      flashloanAmount,
      deadline,
      minProfitOut,
      tipBps
    );
    const receipt = await tx.wait();
    stats.blockchain.transactions.push({
      hash: receipt.hash,
      blockNumber: receipt.blockNumber,
      gasUsed: receipt.gasUsed.toString(),
      effectiveGasPrice: receipt.effectiveGasPrice.toString(),
    });
    stats.blockchain.gasUsed += Number(receipt.gasUsed);
    stats.blockchain.totalCostEth += Number(ethers.formatEther(receipt.gasUsed * receipt.effectiveGasPrice));
    log('contract.write', `executeArb tx confirmed: ${receipt.hash}`, { gas: receipt.gasUsed.toString() });
  } catch (e) {
    if (e.transaction) {
      const receipt = await provider.getTransactionReceipt(e.transaction.hash);
      if (receipt) {
        stats.blockchain.transactions.push({
          hash: receipt.hash,
          blockNumber: receipt.blockNumber,
          gasUsed: receipt.gasUsed.toString(),
          effectiveGasPrice: receipt.effectiveGasPrice.toString(),
        });
        stats.blockchain.gasUsed += Number(receipt.gasUsed);
        stats.blockchain.totalCostEth += Number(ethers.formatEther(receipt.gasUsed * receipt.effectiveGasPrice));
        log('contract.write', `executeArb reverted but mined: ${receipt.hash}`, { gas: receipt.gasUsed.toString() });
      }
    } else {
      captureError('contract.executeArb', e);
    }
  }
}

async function testBackendInteractions() {
  const base = BACKEND_URL;

  try {
    const res = await fetch(`${base}/health`);
    if (!res.ok) throw new Error(`Health check failed: ${res.status}`);
    stats.backend.requests.push({ endpoint: '/health', status: res.status });
    log('backend.health', 'Backend is healthy');
  } catch (e) {
    captureError('backend.health', e);
  }

  try {
    const res = await fetch(`${base}/api/status`);
    const data = await res.json();
    stats.backend.requests.push({ endpoint: '/api/status', status: res.status, response: data });
    log('backend.status', `Status: ${JSON.stringify(data)}`);
  } catch (e) {
    captureError('backend.status', e);
  }

  try {
    const res = await fetch(`${base}/api/metrics`);
    const metrics = await res.json();
    stats.backend.dbState = metrics;
    log('backend.metrics', `Metrics retrieved: ${Object.keys(metrics).length} keys`);
  } catch (e) {
    captureError('backend.metrics', e);
  }

  try {
    const res = await fetch(`${base}/api/files`);
    const files = await res.json();
    stats.backend.s3State = files;
    log('backend.s3', `Found ${files.length} files in S3`);
  } catch (e) {
    captureError('backend.s3', e);
  }
}

async function finalBlockchainCheck() {
  stats.blockchain.finalBalance = ethers.formatEther(await provider.getBalance(wallet.address));
  log('blockchain.balance', 'Final balance checked');
}

async function saveReports() {
  const duration = (Date.now() - stats.startTime) / 1000;
  const summary = {
    totalDurationSeconds: duration,
    totalTransactions: stats.blockchain.transactions.length,
    totalGasUsed: stats.blockchain.gasUsed,
    totalCostEth: stats.blockchain.totalCostEth,
    backendRequestsCount: stats.backend.requests.length,
    errorsCount: stats.errors.length,
    success: stats.errors.length === 0,
  };

  const fullReport = { summary, ...stats };

  await fs.mkdir(REPORT_DIR, { recursive: true });
  await fs.writeFile(path.join(REPORT_DIR, 'report.json'), JSON.stringify(fullReport, null, 2));

  const html = generateReport(fullReport, 'html');
  const md = generateReport(fullReport, 'md');

  await fs.writeFile(path.join(REPORT_DIR, 'report.html'), html);
  await fs.writeFile(path.join(REPORT_DIR, 'report.md'), md);

  console.log(`✅ Reports saved to ${REPORT_DIR}`);
}

async function main() {
  try {
    log('system.start', 'Aether E2E test started');
    await checkBlockchainBasics();
    await testContractInteractions();
    await testBackendInteractions();
    await finalBlockchainCheck();
    await saveReports();
    log('system.end', 'Aether E2E test finished');

    if (stats.errors.length > 0) {
      console.error(`❌ ${stats.errors.length} errors detected.`);
      process.exit(1);
    }
    process.exit(0);
  } catch (error) {
    console.error('❌ Critical error:', error);
    process.exit(1);
  }
}

main();

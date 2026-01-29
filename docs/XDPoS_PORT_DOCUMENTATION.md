# XDPoS Consensus Port to go-ethereum (PR5)

## Overview

This document describes the process of porting XDPoS consensus from XDC v2.6.8 to the latest go-ethereum codebase.

**Repository:** https://github.com/AnilChinchawale/go-ethereum
**Branch:** `feature/xdpos-consensus`
**PR:** https://github.com/AnilChinchawale/go-ethereum/pull/5

---

## What Was Ported

### 1. XDPoS Consensus Engine (`consensus/XDPoS/`)

| File | Purpose |
|------|---------|
| `xdpos.go` | Main consensus engine (Author, VerifyHeader, Seal, Finalize) |
| `reward.go` | Reward calculation and distribution |
| `snapshot.go` | Snapshot management for masternodes |
| `constants.go` | XDC-specific constants |

### 2. XDC Address Support (`common/types.go`)

- Added `xdc` prefix support for addresses
- `HexToAddress()` now accepts both `0x` and `xdc` prefixes

### 3. Block Signing Detection (`core/types/transaction.go`)

- Added `IsSigningTransaction()` method
- Detects transactions to BlockSigner contract (0x89)

### 4. State Helpers (`core/state/statedb.go`)

- `GetCandidateOwner()` - Get masternode owner from Validator contract
- `GetVoters()` - Get voters for a masternode
- `GetVoterCap()` - Get voter's staked amount

---

## Key Technical Challenges & Solutions

### Challenge 1: Block Scan Range for Signing Transactions

**Problem:** Original code scanned blocks 1-900 for signing transactions, but signing txs are in blocks 901-1799.

**Solution:** Changed scan range to `1 to (checkpoint - 1)`:
```go
// In reward.go
scanEndBlock := number - 1  // Scan up to block before current checkpoint
for i := scanEndBlock; i >= startBlockNumber; i-- {
    // Find signing transactions
}
```

### Challenge 2: Accessing Full Blocks in Finalize

**Problem:** `Finalize()` receives `ChainHeaderReader` which doesn't have `GetBlock()`.

**Solution:** Read blocks directly from database:
```go
// Use rawdb.ReadBlock instead of chain.GetBlock
block := rawdb.ReadBlock(c.db, blockHeader.Hash(), i)
```

### Challenge 3: Foundation Reward Rounding

**Problem:** Foundation reward calculation must match v2.6.8's exact wei amounts.

**Solution:** Calculate foundation reward per-signer (not as lump sum):
```go
// Per-signer foundation reward calculation
for signer, signerReward := range signerRewards {
    signerFoundationReward := new(big.Int).Mul(signerReward, big.NewInt(10))
    signerFoundationReward.Div(signerFoundationReward, big.NewInt(100))
    totalFoundationReward.Add(totalFoundationReward, signerFoundationReward)
}
```

### Challenge 4: State Helper Functions

**Problem:** v2.6.8 uses custom contract calls for validator state.

**Solution:** Implemented direct storage slot reading:
```go
func GetCandidateOwner(statedb *StateDB, candidate common.Address) common.Address {
    // Read from Validator contract storage
    // Slot: keccak256(candidate || 1) for validatorsState mapping
}
```

---

## Reward Distribution Logic

### Per Epoch (900 blocks):
- **Total Reward:** 5000 XDC
- **Foundation:** 10% (500 XDC)
- **Validator Owners:** 90% (4500 XDC)

### Distribution Formula:
```
For each signer:
  calcReward = chainReward × (signerCount / totalSigners)
  ownerReward = calcReward × 90%
  foundationReward += calcReward × 10%
```

### Checkpoints:
- Block 900: First checkpoint (no rewards)
- Block 1800: First reward distribution (for blocks 1-900)
- Block 2700: Second reward distribution (for blocks 901-1800)
- And so on...

---

## Testing & Verification

### Test Setup:
```bash
# Reference node (v2.6.8)
Port: 8550 (RPC)

# PR5 node
Port: 8560 (RPC)
Port: 30306 (P2P)
```

### Verification Commands:
```bash
# Compare state roots at checkpoint
curl -s -X POST -H "Content-Type: application/json" \
  --data '{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["0x708", false],"id":1}' \
  http://localhost:8560 | jq '.result.stateRoot'
```

### Verified Checkpoints (All Match ✅):
- Block 1800, 2700, 3600, 4500, 5400, 6300, 7200, 8100, 9000...
- All state roots identical to v2.6.8

---

## What's Remaining to Port

### High Priority:

1. **V2 Consensus (Post-Block 3M)**
   - Location in v2.6.8: `consensus/XDPoS/engines/engine_v2/`
   - Includes: BFT-style consensus, round-robin signing
   - Required for: Blocks after ~3,000,000

2. **Penalty System**
   - `HookPenalty` - Penalize offline validators
   - `HookPenaltyTIPSigning` - TIP signing penalties

3. **Smart Contract Integration**
   - Full Validator contract interaction
   - BlockSigner contract verification

### Medium Priority:

4. **Voting System**
   - Voter rewards (currently 0%)
   - Vote tracking

5. **Masternode Management**
   - Dynamic masternode list updates
   - Propose/vote for new masternodes

6. **Gap Block Handling**
   - Special handling for gap blocks (every 450 blocks)

### Lower Priority:

7. **Slashing Conditions**
   - Double signing detection
   - Slashing penalties

8. **Governance**
   - On-chain voting
   - Parameter updates

---

## File Mapping: v2.6.8 → PR5

| v2.6.8 Location | PR5 Location | Status |
|-----------------|--------------|--------|
| `consensus/XDPoS/XDPoS.go` | `consensus/XDPoS/xdpos.go` | ✅ Ported |
| `eth/hooks/engine_v1_hooks.go` | `consensus/XDPoS/reward.go` | ✅ Ported |
| `contracts/utils.go` | `consensus/XDPoS/reward.go` | ✅ Ported |
| `common/constants.go` | `consensus/XDPoS/constants.go` | ✅ Ported |
| `core/state/statedb_util.go` | `core/state/statedb.go` | ✅ Ported |
| `consensus/XDPoS/engines/engine_v2/` | - | ❌ Not ported |

---

## Running the Node

### Quick Start:
```bash
cd ~/xdc-compare/pr5/repo

# Build
make geth

# Initialize
./build/bin/geth --datadir ./data init genesis.json

# Run
./build/bin/geth \
  --datadir ./data \
  --networkid 50 \
  --port 30306 \
  --http --http.addr 0.0.0.0 --http.port 8560 \
  --syncmode full \
  --bootnodes "enode://81d93b8dc627ec07742d2d174c3ea36db0d78794b224cd8a28e9bc2f1892b2fd39a1d8088198a70b9069e54d1f3077b88bceff1cd4989ae977e992f524607905@159.89.174.254:30303"
```

### Monitoring:
```bash
# Check block
curl -s localhost:8560 -X POST -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","id":1}' | jq

# Check peers
curl -s localhost:8560 -X POST -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","method":"net_peerCount","id":1}' | jq

# Check logs
tail -f logs/geth.log | grep -E "Imported|Rewards|ERROR"
```

---

## Known Issues

### 1. Peer Drops During Sync
- **Symptom:** Peers drop to 0 temporarily
- **Cause:** Sync timeouts when requesting large amounts of data
- **Impact:** Minor - peers automatically reconnect
- **Status:** Normal behavior during fast sync

### 2. "BAD BLOCK" Log Messages
- **Symptom:** ERROR logs showing "unknown ancestor"
- **Cause:** Receiving future blocks from peers (block 98M vs syncing at 30K)
- **Impact:** None - just network noise
- **Status:** Normal behavior

---

## Performance Metrics

- **Sync Speed:** ~1,400 blocks/minute
- **Import Rate:** ~200 mgas/s
- **Memory Usage:** ~2-3 GB during sync

---

## Contributors

- Anil Chinchawale (@AnilChinchawale)
- Claude (AI Assistant)

---

## Last Updated

2026-01-29

## Verification Status

| Block Range | Status |
|-------------|--------|
| 0 - 10,000 | ✅ Verified |
| 10,000 - 20,000 | ✅ Verified |
| 20,000 - 30,000 | ✅ Verified |
| 30,000+ | 🔄 Syncing |

All checkpoint state roots match v2.6.8 reference node.

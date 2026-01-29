# Porting XDPoS Consensus to go-ethereum: A Complete Guide

## Table of Contents

1. [Introduction](#introduction)
2. [Background](#background)
3. [Architecture Overview](#architecture-overview)
4. [Prerequisites](#prerequisites)
5. [Step-by-Step Porting Process](#step-by-step-porting-process)
6. [Detailed Code Changes](#detailed-code-changes)
7. [Testing and Verification](#testing-and-verification)
8. [Troubleshooting](#troubleshooting)
9. [Future Work](#future-work)
10. [Appendix](#appendix)

---

## Introduction

This guide documents the complete process of porting XDC Network's XDPoS (Delegated Proof of Stake) consensus mechanism from the XDC v2.6.8 codebase to the latest go-ethereum (geth) codebase.

### Why Port to Latest Geth?

1. **Performance Improvements**: Latest geth includes significant optimizations
2. **Security Updates**: Access to latest security patches
3. **New Features**: EVM improvements, better tooling
4. **Maintainability**: Easier to merge upstream updates
5. **Developer Experience**: Better debugging, profiling tools

### Scope

This guide covers:
- XDPoS V1 consensus (pre-block 3M)
- Reward distribution mechanism
- Block signing and verification
- State management for validators

---

## Background

### XDC Network Overview

XDC Network is an enterprise-grade blockchain that uses XDPoS consensus:
- **Block Time**: 2 seconds
- **Epoch Length**: 900 blocks
- **Validators**: 108 masternodes
- **Rewards**: 5000 XDC per epoch

### XDPoS Consensus Mechanism

XDPoS is a Delegated Proof of Stake variant where:
1. Masternodes are elected through staking
2. Validators take turns producing blocks
3. Block signers are rewarded proportionally
4. Foundation receives 10% of rewards

### Source Codebases

| Codebase | Version | Purpose |
|----------|---------|---------|
| XDC v2.6.8 | xinfinorg/xdposchain v2.6.8 | Reference implementation |
| go-ethereum | ethereum/go-ethereum v1.14+ | Target codebase |

---

## Architecture Overview

### Original XDC v2.6.8 Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     XDC v2.6.8                               │
├─────────────────────────────────────────────────────────────┤
│  eth/hooks/                                                  │
│  ├── engine_v1_hooks.go    (Reward calculation)             │
│  └── engine_v2_hooks.go    (V2 consensus hooks)             │
├─────────────────────────────────────────────────────────────┤
│  consensus/XDPoS/                                            │
│  ├── XDPoS.go              (Main consensus engine)          │
│  ├── engines/engine_v1/    (V1 consensus logic)             │
│  └── engines/engine_v2/    (V2 BFT consensus)               │
├─────────────────────────────────────────────────────────────┤
│  contracts/                                                  │
│  ├── utils.go              (Contract interactions)          │
│  └── validator/            (Validator contract)             │
└─────────────────────────────────────────────────────────────┘
```

### Ported Architecture (PR5)

```
┌─────────────────────────────────────────────────────────────┐
│                   go-ethereum + XDPoS                        │
├─────────────────────────────────────────────────────────────┤
│  consensus/XDPoS/                                            │
│  ├── xdpos.go              (Main consensus engine)          │
│  ├── reward.go             (Reward calculation - NEW)       │
│  ├── snapshot.go           (Snapshot management)            │
│  └── constants.go          (XDC constants)                  │
├─────────────────────────────────────────────────────────────┤
│  core/state/                                                 │
│  └── statedb.go            (+ XDC state helpers)            │
├─────────────────────────────────────────────────────────────┤
│  core/types/                                                 │
│  └── transaction.go        (+ IsSigningTransaction)         │
├─────────────────────────────────────────────────────────────┤
│  common/                                                     │
│  └── types.go              (+ XDC address support)          │
└─────────────────────────────────────────────────────────────┘
```

### Key Architectural Decisions

1. **Integrated Rewards**: Moved reward logic from hooks to consensus engine
2. **Direct State Access**: Use rawdb for block access during finalization
3. **Simplified Structure**: Single consensus package vs multiple hook files

---

## Prerequisites

### Development Environment

```bash
# Go version 1.21+
go version

# Git
git --version

# Build tools
make --version
```

### Clone Repositories

```bash
# Reference implementation (XDC v2.6.8)
git clone https://github.com/XinFinOrg/XDPoSChain.git xdposchain-v268
cd xdposchain-v268
git checkout v2.6.8

# Target codebase (latest geth)
git clone https://github.com/ethereum/go-ethereum.git go-ethereum
cd go-ethereum
git checkout v1.14.0  # or latest stable
```

### Understanding the Codebase

Before porting, familiarize yourself with:

1. **go-ethereum consensus interface** (`consensus/consensus.go`):
   - `Engine` interface
   - `Finalize()` and `FinalizeAndAssemble()` methods
   - `VerifyHeader()` and `VerifySeal()`

2. **XDC v2.6.8 reward flow**:
   - `eth/hooks/engine_v1_hooks.go` - `HookReward` function
   - `contracts/utils.go` - `GetRewardForCheckpoint`, `CalculateRewardForHolders`

---

## Step-by-Step Porting Process

### Step 1: Create XDPoS Consensus Package

Create the basic structure:

```bash
mkdir -p consensus/XDPoS
touch consensus/XDPoS/xdpos.go
touch consensus/XDPoS/reward.go
touch consensus/XDPoS/snapshot.go
touch consensus/XDPoS/constants.go
```

### Step 2: Define Constants

**File: `consensus/XDPoS/constants.go`**

```go
package XDPoS

// Reward distribution percentages
const (
    RewardMasterPercent     = 90  // Masternode owner reward
    RewardVoterPercent      = 0   // Voter reward (currently disabled)
    RewardFoundationPercent = 10  // Foundation reward
)

// Block signer contract
var (
    BlockSignersBinary = common.HexToAddress("0x0000000000000000000000000000000000000089")
    ValidatorContract  = common.HexToAddress("0x0000000000000000000000000000000000000088")
)
```

### Step 3: Implement Main Consensus Engine

**File: `consensus/XDPoS/xdpos.go`**

The consensus engine must implement the `consensus.Engine` interface:

```go
package XDPoS

import (
    "github.com/ethereum/go-ethereum/consensus"
    "github.com/ethereum/go-ethereum/core/state"
    "github.com/ethereum/go-ethereum/core/types"
    // ... other imports
)

type XDPoS struct {
    config *params.XDPoSConfig
    db     ethdb.Database
    // ... caches and fields
}

// Implement consensus.Engine interface
func (c *XDPoS) Author(header *types.Header) (common.Address, error) {
    return ecrecover(header, c.signatures)
}

func (c *XDPoS) VerifyHeader(chain consensus.ChainHeaderReader, header *types.Header) error {
    return c.verifyHeader(chain, header, nil)
}

func (c *XDPoS) Finalize(chain consensus.ChainHeaderReader, header *types.Header, 
    statedb vm.StateDB, body *types.Body) {
    
    number := header.Number.Uint64()
    rCheckpoint := c.config.RewardCheckpoint
    
    // Apply rewards at checkpoint blocks
    if number % rCheckpoint == 0 {
        if sdb, ok := statedb.(*state.StateDB); ok {
            c.ApplyRewards(chain, sdb, sdb, header)
        }
    }
}
```

### Step 4: Implement Reward Calculation

This is the most critical part. The reward logic must match v2.6.8 exactly.

**File: `consensus/XDPoS/reward.go`**

```go
package XDPoS

// RewardLog stores signing count and reward for a signer
type RewardLog struct {
    Sign   uint64
    Reward *big.Int
}

// GetRewardForCheckpoint calculates signing rewards for the checkpoint epoch
func (c *XDPoS) GetRewardForCheckpoint(
    chain BlockReader,
    header *types.Header,
    rCheckpoint uint64,
) (map[common.Address]*RewardLog, uint64, error) {
    
    number := header.Number.Uint64()
    
    // Match v2.6.8's formula:
    // prevCheckpoint = number - (rCheckpoint * 2)
    // startBlockNumber = prevCheckpoint + 1
    // endBlockNumber = startBlockNumber + rCheckpoint - 1
    
    prevCheckpoint := number - (rCheckpoint * 2)
    startBlockNumber := prevCheckpoint + 1
    endBlockNumber := startBlockNumber + rCheckpoint - 1
    scanEndBlock := number - 1
    
    // Skip if before first reward checkpoint
    if number < rCheckpoint*2 {
        return nil, 0, nil
    }
    
    // Get masternodes from epoch checkpoint
    epochHeader := chain.GetHeaderByNumber(prevCheckpoint)
    masternodes := c.GetMasternodesFromCheckpointHeader(epochHeader, prevCheckpoint, epoch)
    
    // Build masternode map for quick lookup
    masternodeMap := make(map[common.Address]bool)
    for _, mn := range masternodes {
        masternodeMap[mn] = true
    }
    
    // Collect signing transactions
    blockSigners := make(map[common.Hash][]common.Address)
    
    // CRITICAL: Scan ALL blocks from 1 to checkpoint-1
    // Signing transactions are in blocks AFTER the epoch they sign for
    for i := scanEndBlock; i >= startBlockNumber; i-- {
        blockHeader := chain.GetHeaderByNumber(i)
        if blockHeader == nil {
            continue
        }
        
        // Read block directly from database
        block := rawdb.ReadBlock(c.db, blockHeader.Hash(), i)
        if block == nil {
            continue
        }
        
        // Find signing transactions
        for _, tx := range block.Transactions() {
            if tx.IsSigningTransaction() {
                data := tx.Data()
                if len(data) >= 68 {
                    signedBlockHash := common.BytesToHash(data[len(data)-32:])
                    signer, _ := types.Sender(types.LatestSignerForChainID(big.NewInt(50)), tx)
                    
                    if masternodeMap[signer] {
                        blockSigners[signedBlockHash] = append(blockSigners[signedBlockHash], signer)
                    }
                }
            }
        }
    }
    
    // Count signatures per signer
    signers := make(map[common.Address]*RewardLog)
    var totalSigner uint64
    
    for i := startBlockNumber; i <= endBlockNumber; i++ {
        blockHeader := chain.GetHeaderByNumber(i)
        addrs := blockSigners[blockHeader.Hash()]
        
        seen := make(map[common.Address]bool)
        for _, addr := range addrs {
            if !seen[addr] && masternodeMap[addr] {
                seen[addr] = true
                if _, exists := signers[addr]; exists {
                    signers[addr].Sign++
                } else {
                    signers[addr] = &RewardLog{Sign: 1, Reward: big.NewInt(0)}
                }
                totalSigner++
            }
        }
    }
    
    return signers, totalSigner, nil
}
```

### Step 5: Implement Reward Distribution

```go
// ApplyRewards distributes rewards at checkpoint blocks
func (c *XDPoS) ApplyRewards(
    chain BlockReader,
    statedb *state.StateDB,
    parentState *state.StateDB,
    header *types.Header,
) (map[string]interface{}, error) {
    
    number := header.Number.Uint64()
    chainReward := new(big.Int).Mul(
        new(big.Int).SetUint64(c.config.Reward),
        big.NewInt(1e18),
    )
    
    // Get signers for this checkpoint
    signers, totalSigner, _ := c.GetRewardForCheckpoint(chain, header, rCheckpoint)
    
    if totalSigner == 0 {
        return rewards, nil
    }
    
    // Calculate per-signer rewards
    signerRewards := CalculateRewardForSigner(chainReward, signers, totalSigner)
    
    // Distribute rewards
    totalFoundationReward := big.NewInt(0)
    
    for signer, signerReward := range signerRewards {
        // Get holder rewards (owner gets 90%)
        holderRewards := CalculateRewardForHolders(foundationWallet, parentState, signer, signerReward, number)
        
        for holder, reward := range holderRewards {
            if reward.Sign() > 0 {
                rewardU256, _ := uint256.FromBig(reward)
                statedb.AddBalance(holder, rewardU256, tracing.BalanceIncreaseRewardMineBlock)
            }
        }
        
        // Calculate foundation reward per-signer (for exact wei matching)
        signerFoundationReward := new(big.Int).Mul(signerReward, big.NewInt(RewardFoundationPercent))
        signerFoundationReward.Div(signerFoundationReward, big.NewInt(100))
        totalFoundationReward.Add(totalFoundationReward, signerFoundationReward)
    }
    
    // Distribute foundation reward
    if totalFoundationReward.Sign() > 0 {
        foundationU256, _ := uint256.FromBig(totalFoundationReward)
        statedb.AddBalance(foundationWallet, foundationU256, tracing.BalanceIncreaseRewardMineBlock)
    }
    
    return rewards, nil
}
```

### Step 6: Add State Helper Functions

**File: `core/state/statedb.go`** (add to existing file)

```go
// GetCandidateOwner returns the owner of a masternode candidate
func GetCandidateOwner(statedb *StateDB, candidate common.Address) common.Address {
    // Validator contract address
    validator := common.HexToAddress("0x0000000000000000000000000000000000000088")
    
    // Storage slot calculation:
    // validatorsState mapping is at slot 1
    // Location: keccak256(candidate || slot) + offset
    
    candidateBytes := candidate.Bytes()
    slotBytes := make([]byte, 32)
    slotBytes[31] = 1  // slot 1
    
    // Compute storage key
    keyPreimage := append(common.LeftPadBytes(candidateBytes, 32), slotBytes...)
    storageKey := crypto.Keccak256Hash(keyPreimage)
    
    // Read owner (offset 0)
    ownerHash := statedb.GetState(validator, storageKey)
    
    return common.BytesToAddress(ownerHash.Bytes())
}
```

### Step 7: Add Transaction Helper

**File: `core/types/transaction.go`** (add to existing file)

```go
// IsSigningTransaction returns true if this is a block signing transaction
func (tx *Transaction) IsSigningTransaction() bool {
    to := tx.To()
    if to == nil || *to != common.HexToAddress("0x0000000000000000000000000000000000000089") {
        return false
    }
    
    data := tx.Data()
    if len(data) != 68 {  // 4 bytes method + 32 bytes blockNumber + 32 bytes blockHash
        return false
    }
    
    // Check method signature: sign(uint256,bytes32) = 0xe341eaa4
    method := hex.EncodeToString(data[0:4])
    return method == "e341eaa4"
}
```

### Step 8: Add XDC Address Support

**File: `common/types.go`** (modify existing)

```go
// HexToAddress returns Address with byte values of s.
// Supports both "0x" and "xdc" prefixes for XDC compatibility.
func HexToAddress(s string) Address {
    // Handle XDC prefix
    if has0xPrefix(s) {
        s = s[2:]
    } else if hasXdcPrefix(s) {
        s = s[3:]
    }
    return BytesToAddress(FromHex(s))
}

func hasXdcPrefix(str string) bool {
    return len(str) >= 3 && strings.ToLower(str[0:3]) == "xdc"
}
```

### Step 9: Configure Chain Parameters

**File: `params/config.go`** (add XDPoS config)

```go
// XDPoSConfig is the consensus engine configs for XDC's DPoS
type XDPoSConfig struct {
    Period              uint64         `json:"period"`              // Block time in seconds
    Epoch               uint64         `json:"epoch"`               // Epoch length in blocks
    Reward              uint64         `json:"reward"`              // Block reward
    RewardCheckpoint    uint64         `json:"rewardCheckpoint"`    // Reward distribution interval
    Gap                 uint64         `json:"gap"`                 // Gap for masternode rotation
    FoudationWalletAddr common.Address `json:"foudationWalletAddr"` // Foundation wallet
}

// XDC Mainnet configuration
var XDCMainnetChainConfig = &ChainConfig{
    ChainId:        big.NewInt(50),
    HomesteadBlock: big.NewInt(1),
    // ... other fields
    XDPoS: &XDPoSConfig{
        Period:              2,
        Epoch:               900,
        Reward:              5000,
        RewardCheckpoint:    900,
        Gap:                 450,
        FoudationWalletAddr: common.HexToAddress("xdc92a289fe95a85c53b8d0d113cbaef0c1ec98ac65"),
    },
}
```

---

## Detailed Code Changes

### Critical Implementation Details

#### 1. Block Scan Range

**Problem**: Signing transactions for blocks 1-900 are in blocks 901-1799, not 1-900.

**v2.6.8 Code** (`contracts/utils.go`):
```go
for i := prevCheckpoint + (rCheckpoint * 2) - 1; i >= startBlockNumber; i-- {
    // Scans blocks 1-1799 for checkpoint 1800
}
```

**Solution**: Scan from `number - 1` down to `startBlockNumber`:
```go
scanEndBlock := number - 1  // For block 1800: scan 1-1799
for i := scanEndBlock; i >= startBlockNumber; i-- {
    // Find signing transactions
}
```

#### 2. Block Access During Finalization

**Problem**: `Finalize()` receives `ChainHeaderReader` which has no `GetBlock()` method.

**Solution**: Read blocks directly from database:
```go
// Instead of chain.GetBlock()
block := rawdb.ReadBlock(c.db, blockHeader.Hash(), i)
```

#### 3. Foundation Reward Rounding

**Problem**: Foundation reward must match v2.6.8's exact wei amounts.

**v2.6.8 Calculation**: Foundation gets 10% of EACH signer's reward
```go
// Per signer: 
foundationReward = calcReward * 10 / 100
```

**Wrong Approach** (causes 14 wei difference):
```go
// Lump sum:
foundationReward = chainReward * 10 / 100
```

**Correct Approach**:
```go
for signer, signerReward := range signerRewards {
    signerFoundationReward := new(big.Int).Mul(signerReward, big.NewInt(10))
    signerFoundationReward.Div(signerFoundationReward, big.NewInt(100))
    totalFoundationReward.Add(totalFoundationReward, signerFoundationReward)
}
```

#### 4. Checkpoint Skip Logic

**Problem**: Block 1800 is the FIRST reward checkpoint (rewards blocks 1-900).

**Wrong Condition**:
```go
if number <= rCheckpoint*2 {  // Skips block 1800!
    return nil, 0, nil
}
```

**Correct Condition**:
```go
if number < rCheckpoint*2 {  // Processes block 1800
    return nil, 0, nil
}
```

---

## Testing and Verification

### Test Environment Setup

```bash
# Create test directory
mkdir -p ~/xdc-compare
cd ~/xdc-compare

# Set up reference node (v2.6.8)
mkdir v268 && cd v268
# ... build and run v2.6.8 node on port 8550

# Set up PR5 node
mkdir pr5 && cd pr5
# ... build and run PR5 node on port 8560
```

### Genesis File

Use XDC mainnet genesis for testing:
```json
{
  "config": {
    "chainId": 50,
    "xdpos": {
      "period": 2,
      "epoch": 900,
      "reward": 5000,
      "rewardCheckpoint": 900,
      "gap": 450,
      "foudationWalletAddr": "xdc92a289fe95a85c53b8d0d113cbaef0c1ec98ac65"
    }
  },
  "alloc": { ... }
}
```

### Verification Commands

```bash
# Compare state roots at checkpoint
compare_checkpoint() {
    BLOCK=$1
    HEX=$(printf "0x%x" $BLOCK)
    
    V268=$(curl -s -X POST -H "Content-Type: application/json" \
        --data "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBlockByNumber\",\"params\":[\"$HEX\", false],\"id\":1}" \
        http://localhost:8550 | jq -r '.result.stateRoot')
    
    PR5=$(curl -s -X POST -H "Content-Type: application/json" \
        --data "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBlockByNumber\",\"params\":[\"$HEX\", false],\"id\":1}" \
        http://localhost:8560 | jq -r '.result.stateRoot')
    
    if [ "$V268" = "$PR5" ]; then
        echo "Block $BLOCK: ✅ MATCH"
    else
        echo "Block $BLOCK: ❌ MISMATCH"
        echo "  v2.6.8: $V268"
        echo "  PR5:    $PR5"
    fi
}

# Test key checkpoints
for block in 1800 2700 3600 4500 5400; do
    compare_checkpoint $block
done
```

### Balance Verification

```bash
# Compare foundation balance
FOUNDATION="0x92a289fe95a85c53B8d0d113CBaEf0C1Ec98ac65"
BLOCK="0x708"  # 1800

# Get balance change
curl -s -X POST -H "Content-Type: application/json" \
    --data "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBalance\",\"params\":[\"$FOUNDATION\", \"$BLOCK\"],\"id\":1}" \
    http://localhost:8560 | jq -r '.result'
```

### Expected Results

| Checkpoint | Foundation Reward | Owner Reward | Total |
|------------|-------------------|--------------|-------|
| 1800 | 500 XDC | 4500 XDC | 5000 XDC |
| 2700 | 500 XDC | 4500 XDC | 5000 XDC |
| 3600 | 500 XDC | 4500 XDC | 5000 XDC |

---

## Troubleshooting

### Common Issues

#### 1. State Root Mismatch

**Symptoms**: State root differs from v2.6.8 at checkpoint blocks.

**Debugging**:
```bash
# Compare balances of key addresses
curl -s -X POST -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","method":"eth_getBalance","params":["0x...", "0x708"],"id":1}' \
    http://localhost:8560
```

**Common Causes**:
- Wrong block scan range
- Incorrect foundation reward calculation
- Missing/incorrect state helper functions

#### 2. Zero Signers Found

**Symptoms**: `totalSigners=0` at checkpoint.

**Debugging**:
```go
log.Info("Scanning blocks", "from", scanEndBlock, "to", startBlockNumber)
log.Info("Transaction count", "totalTxs", txCount, "signingTxs", signingTxCount)
```

**Common Causes**:
- Block not available (GetBlock returns nil)
- Wrong transaction detection logic
- Incorrect block scan range

#### 3. Peer Drops During Sync

**Symptoms**: Peers frequently drop to 0.

**Cause**: Normal during fast sync - peers timeout when overwhelmed.

**Solution**: Node auto-recovers. Use monitoring script for auto-restart.

#### 4. "shutting down" Errors

**Symptoms**: Logs show "failed to request bodies: shutting down"

**Cause**: Normal peer disconnect handling, not actual crashes.

**Solution**: No action needed - sync continues with other peers.

### Debugging Tools

```bash
# Enable verbose logging
geth --verbosity 4 ...

# Watch specific log patterns
tail -f geth.log | grep -E "Rewards|Scanning|Checkpoint"

# Check reward distribution
grep "Rewards distributed" geth.log | tail -5
```

---

## Future Work

### Not Yet Ported (Required for Full Mainnet Sync)

| Feature | Priority | Location in v2.6.8 |
|---------|----------|-------------------|
| V2 Consensus (BFT) | High | `consensus/XDPoS/engines/engine_v2/` |
| Penalty System | High | `eth/hooks/engine_v1_hooks.go` |
| Voting System | Medium | `contracts/utils.go` |
| Gap Block Handling | Medium | `consensus/XDPoS/` |
| Slashing | Low | `consensus/XDPoS/` |

### V2 Consensus (Post-Block ~3M)

V2 introduces BFT-style consensus with:
- Round-robin block production
- Vote collection and verification
- Timeout handling
- Fork choice rules

Files to port:
```
consensus/XDPoS/engines/engine_v2/
├── engine.go
├── verifyHeader.go
├── vote.go
└── timeout.go
```

---

## Appendix

### File Mapping Reference

| v2.6.8 File | PR5 File | Description |
|-------------|----------|-------------|
| `consensus/XDPoS/XDPoS.go` | `consensus/XDPoS/xdpos.go` | Main engine |
| `eth/hooks/engine_v1_hooks.go` | `consensus/XDPoS/reward.go` | Rewards |
| `contracts/utils.go` | `consensus/XDPoS/reward.go` | Reward calc |
| `core/state/statedb_util.go` | `core/state/statedb.go` | State helpers |
| `common/constants.go` | `consensus/XDPoS/constants.go` | Constants |

### Reward Calculation Formula

```
For checkpoint block N:
  - Reward epoch: blocks (N - 1800 + 1) to (N - 900)
  - Signing tx scan: blocks 1 to (N - 1)
  - Total reward: 5000 XDC
  - Foundation: sum(calcReward[i] * 10 / 100) for each signer i
  - Owner: sum(calcReward[i] * 90 / 100) for each signer i
  
Where:
  calcReward[i] = chainReward * signerCount[i] / totalSigners
```

### Useful Commands

```bash
# Build
make geth

# Initialize
./build/bin/geth --datadir ./data init genesis.json

# Run with XDC mainnet
./build/bin/geth \
    --datadir ./data \
    --networkid 50 \
    --port 30306 \
    --http --http.port 8560 \
    --syncmode full \
    --bootnodes "enode://..."

# Check sync
curl -s localhost:8560 -X POST -H 'Content-Type: application/json' \
    -d '{"jsonrpc":"2.0","method":"eth_blockNumber","id":1}' | jq
```

---

## Contributors

- **Anil Chinchawale** - XDC Network
- **Claude** - AI Assistant (Anthropic)

## License

This documentation is part of the XDC Network project.

## Last Updated

2026-01-29

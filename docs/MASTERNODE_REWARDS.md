# XDC Network Masternode & Rewards Integration

## Overview

This document describes the masternode contract integration, reward distribution,
and penalty logic in go-ethereum-pr5 for XDC Network compatibility.

## Contract Addresses

| Contract | Address | Purpose |
|----------|---------|---------|
| Validator | `0x0000000000000000000000000000000000000088` | Masternode registration, staking, voting |
| Block Signer | `0x0000000000000000000000000000000000000089` | Tracks block signatures |
| Randomize | `0x0000000000000000000000000000000000000090` | VRF for masternode selection |

## Masternode Selection

### From Checkpoint Headers (Current Implementation)
The current implementation extracts masternodes from checkpoint block headers:

```go
func (c *XDPoS) GetMasternodesFromCheckpointHeader(header *types.Header, n, e uint64) []common.Address {
    // Masternodes are encoded in header.Extra between vanity and seal
    masternodes := make([]common.Address, (len(header.Extra)-extraVanity-extraSeal)/common.AddressLength)
    for i := 0; i < len(masternodes); i++ {
        copy(masternodes[i][:], header.Extra[extraVanity+i*common.AddressLength:])
    }
    return masternodes
}
```

### From Contract (For Fresh Data)
The `ContractCaller` can query the validator contract directly:

```go
caller := NewContractCaller(chainConfig)
candidates, _ := caller.GetCandidatesFromContract(statedb, header)
masternodes, _ := caller.GetMasternodesWithStakes(statedb, header, MaxMasternodes)
```

## Reward Distribution

### Configuration
- **Block Reward**: 5000 XDC per epoch
- **Masternode Share**: 90%
- **Foundation Share**: 10%
- **Voter Share**: 0% (handled by voter contract)

### Reward Hook Integration
Rewards are distributed at checkpoint blocks via the `HookReward` function:

```go
// In FinalizeAndAssemble:
if c.HookReward != nil && number%rCheckpoint == 0 {
    rewards, err := c.HookReward(chain, statedb, header)
}
```

### Reward Calculation Logic
1. Count block signatures in the epoch
2. Calculate masternode portion (90% of block reward)
3. Distribute proportionally based on signatures
4. Send foundation portion (10%) to foundation wallet

```go
// Proportional distribution
for addr, count := range signCount {
    reward := masternodeReward * count / totalSigns
    state.AddBalance(addr, reward)
}
```

## Penalty System

### Penalty Conditions
Masternodes are penalized if they:
- Fail to sign minimum required blocks per epoch (`MinimunMinerBlockPerEpoch = 1`)
- Are flagged for malicious behavior

### Penalty Duration
- `LimitPenaltyEpoch = 4`: Penalized masternodes are banned for 4 epochs

### Penalty Hook Integration
```go
// In Prepare and verifyCascadingFields:
if c.HookPenaltyTIPSigning != nil {
    penPenalties, _ = c.HookPenaltyTIPSigning(chain, header, signers)
} else if c.HookPenalty != nil {
    penPenalties, _ = c.HookPenalty(chain, number)
}
signers = removeItemFromArray(signers, penPenalties)
```

## Key Constants

```go
const (
    RewardMasterPercent     = 90   // 90% to masternodes
    RewardVoterPercent      = 0    // 0% (handled by contract)
    RewardFoundationPercent = 10   // 10% to foundation

    MaxMasternodes   = 18   // V1 max masternodes
    MaxMasternodesV2 = 108  // V2 max masternodes

    LimitPenaltyEpoch         = 4   // Epochs to remain penalized
    MinimunMinerBlockPerEpoch = 1   // Min blocks to sign
)
```

## Integration Points

### 1. Consensus Engine Creation
```go
// In eth/backend.go or cmd/geth
xdpos := XDPoS.New(config.XDPoS, chainDb)
xdpos.HookReward = xdpos.CreateDefaultHookReward()
xdpos.HookPenalty = xdpos.CreateDefaultHookPenalty()
xdpos.HookPenaltyTIPSigning = xdpos.CreateDefaultHookPenaltyTIPSigning()
```

### 2. Block Finalization
Rewards are distributed in `FinalizeAndAssemble`:
```go
func (c *XDPoS) FinalizeAndAssemble(...) (*types.Block, error) {
    if c.HookReward != nil && number%rCheckpoint == 0 {
        c.HookReward(chain, statedb, header)
    }
    // ...
}
```

### 3. Header Verification
Penalties are checked in `verifyCascadingFields`:
```go
func (c *XDPoS) verifyCascadingFields(...) error {
    if number%c.config.Epoch == 0 {
        // Check penalties and verify masternode list
    }
}
```

## File Structure

```
consensus/XDPoS/
├── xdpos.go           # Main consensus engine
├── reward.go          # Reward calculation and distribution
├── penalty.go         # Penalty calculation
├── contracts.go       # Contract addresses and method selectors
├── contract_caller.go # State-based contract calls
├── snapshot.go        # Voting snapshots
├── api.go             # RPC API
├── constants.go       # Protocol constants
└── utils/
    ├── constants.go   # Utility constants
    ├── types.go       # Type definitions
    └── utils.go       # Helper functions

contracts/validator/
├── validator.go       # High-level validator interface
└── contract/
    └── validator.go   # Generated ABI bindings
```

## Testing Reward Distribution

### RPC Verification
```bash
# Get masternodes at block
curl -X POST -H "Content-Type: application/json" \
  --data '{"jsonrpc":"2.0","method":"xdpos_getMasternodes","params":["latest"],"id":1}' \
  http://localhost:8545

# Get signers
curl -X POST -H "Content-Type: application/json" \
  --data '{"jsonrpc":"2.0","method":"xdpos_getSigners","params":["latest"],"id":1}' \
  http://localhost:8545
```

### Balance Verification
```bash
# Check masternode balance before/after checkpoint
curl -X POST -H "Content-Type: application/json" \
  --data '{"jsonrpc":"2.0","method":"eth_getBalance","params":["MASTERNODE_ADDR","latest"],"id":1}' \
  http://localhost:8545
```

## Remaining Integration Points

1. **Full Block Signer Contract Integration**: Implement querying the 0x89 contract
   for actual signature counts per masternode per epoch.

2. **Voter Reward Distribution**: Implement voter reward distribution via the
   validator contract (currently 0%).

3. **Randomize Contract**: Implement VRF-based masternode selection using 0x90.

4. **V2 Quorum Certificates**: Full validation of XDPoS 2.0 blocks requires
   quorum certificate verification.

## Configuration

### Genesis Configuration
```json
{
  "config": {
    "XDPoS": {
      "period": 2,
      "epoch": 900,
      "gap": 450,
      "rewardCheckpoint": 900,
      "foudationWalletAddr": "0x...",
      "v2": {
        "switchBlock": 50000000
      }
    }
  }
}
```

### Environment Variables
- `STORE_REWARD_FOLDER`: Directory to store reward JSON files for auditing

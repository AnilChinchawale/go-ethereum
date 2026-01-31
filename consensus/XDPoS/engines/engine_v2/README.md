# XDPoS V2 BFT Consensus Engine

This directory contains the implementation of the XDPoS 2.0 BFT (Byzantine Fault Tolerant) consensus engine.

## Files

### Core Engine Files

| File | Lines | Description |
|------|-------|-------------|
| `engine.go` | ~767 | Main V2 engine struct with BFT state, `VerifyHeader()`, `VerifyHeaders()`, `Prepare()`, `Finalize()`, `Seal()`, round/view management |
| `vote.go` | ~354 | Vote message handling, `VoteHandler()`, `ProposedBlockHandler()`, QC assembly, voting rule verification |
| `timeout.go` | ~336 | Timeout message handling, `TimeoutHandler()`, `OnCountdownTimeout()`, TC assembly |
| `verify.go` | ~271 | `verifyQC()`, `verifyTC()`, `VerifyBlockInfo()`, `VerifySyncInfoMessage()`, `processQC()`, `commitBlocks()` |
| `epochSwitch.go` | ~349 | Epoch boundary detection, `IsEpochSwitch()`, `getEpochSwitchInfo()`, masternode list management |

### Supporting Files

| File | Lines | Description |
|------|-------|-------------|
| `utils.go` | ~311 | Utility functions: `sigHash()`, `ecrecover()`, `UniqueSignatures()`, `signSignature()`, `verifyMsgSignature()`, `getExtraFields()` |
| `snapshot.go` | ~112 | V2 snapshot management for validator sets |
| `forensics.go` | ~119 | Byzantine behavior detection and monitoring |

## Architecture

### BFT Consensus Flow

1. **Block Proposal**: Leader proposes block with QC of previous round
2. **Voting**: Validators verify and send votes
3. **QC Assembly**: When 2/3+ votes received, QC is assembled
4. **Timeout**: If no progress, timeout triggers TC assembly
5. **Commit**: 3-chain commit rule commits finalized blocks

### Key Components

- **QuorumCert (QC)**: 2/3+ votes for a block
- **TimeoutCert (TC)**: 2/3+ timeouts to advance round
- **SyncInfo**: Contains highest QC and TC for synchronization
- **Vote/Timeout Pools**: Collect and aggregate messages

## Configuration

The engine reads configuration from `params.XDPoSV2Config`:
- `SwitchBlock`: Block number to switch to V2
- `MinePeriod`: Mining period in seconds
- `TimeoutPeriod`: Timeout period in seconds  
- `TimeoutSyncThreshold`: Threshold for sync info broadcast
- `CertThreshold`: Percentage threshold for QC/TC (default 66.7%)

## API Adaptations

This implementation is adapted for the latest go-ethereum APIs:
- Uses local error definitions in `utils/errors.go`
- Computes `SwitchEpoch` from `SwitchBlock`
- Uses hardcoded `MaxMasternodes = 108` (can be made configurable)

## Testing

```bash
# Build XDPoS package
go build ./consensus/XDPoS/...

# Build full geth
go build ./cmd/geth
```

## Reference

Based on XDPoSChain implementation:
https://github.com/XinFinOrg/XDPoSChain/tree/master/consensus/XDPoS/engines/engine_v2

# XDPoS Block Minting Implementation for PR5

## Overview

This document describes the block minting implementation for XDPoS consensus in 
go-ethereum-pr5, which allows nodes to act as masternodes/validators and produce blocks.

## Architecture

### Components

1. **consensus/XDPoS/xdpos.go** - Main consensus engine with Seal() method
2. **consensus/XDPoS/validator.go** - Double validation support (GetValidator, GetM1M2)
3. **eth/miner_xdpos.go** - Mining loop integration

### Block Production Flow

```
1. XDPoSMiner.mintLoop() starts with period ticker
2. On each tick or new chain head:
   - Check YourTurn() to see if it's our turn to mint
   - If yes, call mintBlock()
3. mintBlock():
   - Create header from parent
   - Run engine.Prepare() to set consensus fields
   - Gather pending transactions
   - Execute transactions and create receipts  
   - Call engine.FinalizeAndAssemble() to create block
   - Call engine.Seal() to sign block
4. Seal():
   - Verify we're authorized to mint
   - Sign block header with our key
   - Add signature to header.Extra
   - Handle double validation (GetValidator)
   - Return sealed block via results channel
```

## Double Validation

XDPoS uses double validation where each block needs two signatures:

- **M1 (Creator)**: The masternode producing the block
- **M2 (Validator)**: Another masternode assigned to validate

The M1->M2 mapping is determined by:
1. Get checkpoint header for current epoch
2. Extract validator indices from header.Validators field
3. Calculate rotation based on block position in epoch
4. Map each creator to their assigned validator

If M1 == M2 (we are both creator and validator), we add our signature to both
header.Extra and header.Validator fields.

## Turn Calculation

The YourTurn() function determines if it's our turn to mint:

```go
// In round-robin fashion among masternodes
position = findOurIndex(masternodes, signer)
expectedPosition = blockNumber % len(masternodes)
return position == expectedPosition
```

## Usage

### Starting Mining

```go
// In application code
if xdposEngine, ok := engine.(*XDPoS.XDPoS); ok {
    // Create signing function from wallet
    signFn := func(acc accounts.Account, mime string, data []byte) ([]byte, error) {
        return wallet.SignData(acc, mime, data)
    }
    
    // Authorize engine
    xdposEngine.Authorize(coinbase, signFn)
    
    // Start miner
    miner := eth.NewXDPoSMiner(ethereum)
    miner.Start(coinbase)
}
```

### Via API

```
// RPC call to start mining
eth.StartMining(coinbase)
```

### Command Line

```bash
# Start with mining enabled
./geth --mine --miner.etherbase <address> --unlock <address> --password <file>
```

## Files Modified

1. **consensus/XDPoS/xdpos.go**
   - Updated Seal() to include double validation
   - Added GetValidator call and header.Validator assignment

2. **consensus/XDPoS/validator.go** (NEW)
   - GetValidator() - Returns M2 validator for a creator
   - GetM1M2FromCheckpointHeader() - Builds M1->M2 mapping
   - getM1M2Mapping() - Internal mapping calculation

3. **eth/miner_xdpos.go** (NEW)
   - XDPoSMiner struct - Mining worker
   - mintLoop() - Main mining goroutine
   - mintBlock() - Block assembly and sealing
   - StartMining() - Integration with Ethereum backend

## Configuration

### XDPoSConfig Parameters

- **Period**: Block time in seconds (default: 2)
- **Epoch**: Blocks per epoch (default: 900)
- **Gap**: Blocks before epoch end for preparation (default: 450)

### Miner Config Parameters

- **GasCeil**: Maximum gas limit for blocks
- **ExtraData**: Vanity data in block header
- **GasPrice**: Minimum gas price for transactions

## Testing

### Unit Tests

1. Test YourTurn() calculation
2. Test GetValidator() mapping
3. Test Seal() produces valid signatures
4. Test double validation signatures

### Integration Tests

1. Start private network with 3 masternodes
2. Verify blocks are produced in round-robin
3. Verify double validation signatures present
4. Test epoch transitions

### Manual Testing

1. Set up 3-node private network
2. Unlock accounts on each node
3. Start mining on each node
4. Monitor block production logs
5. Verify blocks have correct signatures

## Known Limitations

1. Only supports V1 consensus (pre-XDPoS 2.0)
2. Requires unlocked account for signing
3. No hot-swap of mining key

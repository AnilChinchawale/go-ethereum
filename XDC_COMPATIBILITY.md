# XDC Network Compatibility Layer

This document describes the modifications made to go-ethereum to support XDC Network (mainnet network ID 50).

## Overview

XDC Network uses a modified version of go-ethereum with:
- **XDPoS 2.0 consensus** (eth/100 protocol)
- **Legacy eth protocols** (eth/62, eth/63) without ForkID
- **Pre-EIP-8 RLPx handshake** format

## Key Modifications

### 1. Protocol Support (`eth/protocols/eth/protocol.go`)

Added XDC-compatible protocol versions:
- `ETH62` (version 62) - Basic block/tx sync
- `ETH63` (version 63) - Adds state sync (GetNodeData)
- `XDPOS2` (version 100) - XDPoS 2.0 consensus protocol

```go
const (
    ETH62  = 62   // XDC legacy
    ETH63  = 63   // XDC legacy with state
    XDPOS2 = 100  // XDPoS 2.0 consensus
)
```

### 2. RLPx Handshake (`p2p/rlpx/rlpx.go`)

Added pre-EIP-8 handshake fallback for compatibility with XDC v2.6.x nodes:
- Detects pre-EIP-8 format (fixed 194-byte auth message)
- Falls back to legacy secp256k1 ECIES decryption
- Maintains full backward compatibility

### 3. Status Message (`eth/protocols/eth/handshake.go`)

Implemented eth/62 style handshake without ForkID:
- `StatusPacket62` - Simple status without fork validation
- `handshake62()` - XDC-compatible handshake
- Stores peer head/TD for sync coordination

### 4. P2P Server (`p2p/server.go`)

Fixed peer slot handling for static connections:
- Static dial connections bypass MaxPeers check (XDC behavior)
- Matches XDC v2.6.8 peer acceptance logic

### 5. Discovery (`p2p/discover/v4_udp.go`)

Custom ping packet with "XDC" topic for XDC network discovery.

## Protocol Message Handlers

All XDC-compatible message handlers in `eth/protocols/eth/handler.go`:

| Message | eth/62 | eth/63 | XDPOS2 |
|---------|--------|--------|--------|
| NewBlockHashes | ✓ | ✓ | ✓ |
| NewBlock | ✓ | ✓ | ✓ |
| Transactions | ✓ | ✓ | ✓ |
| GetBlockHeaders | ✓ | ✓ | ✓ |
| BlockHeaders | ✓ | ✓ | ✓ |
| GetBlockBodies | ✓ | ✓ | ✓ |
| BlockBodies | ✓ | ✓ | ✓ |
| GetNodeData | - | ✓ | ✓ |
| NodeData | - | ✓ | ✓ |
| GetReceipts | - | ✓ | ✓ |
| Receipts | - | ✓ | ✓ |

## Testing

Connect to XDC mainnet:
```bash
./geth --networkid 50 --port 30305 \
  --bootnodes "enode://..." \
  --syncmode full \
  --snapshot=false
```

Add production peer as trusted for testing:
```javascript
admin.addTrustedPeer("enode://...")
```

## Current Status

### Working ✅
- Peer connection to XDC mainnet
- Block broadcasts received (live blocks)
- XDPoS V2 header validation (blocks >= 80370000)
- Protocol handshake (eth/62, eth/63, eth/100)

### In Progress ⏳
- Historical sync (requesting past blocks from peer)
- Full chain download from genesis

### Known Limitations

1. **Snap sync disabled** - XDC doesn't support snap protocol
2. **No eth/68-69** - Removed to avoid protocol conflicts
3. **Pre-merge only** - XDC is pre-merge, TD-based consensus
4. **Read-only mode** - V2 validation doesn't verify quorum certs (suitable for RPC nodes)

## Files Modified

- `eth/protocols/eth/protocol.go` - Protocol versions
- `eth/protocols/eth/handshake.go` - eth/62 handshake
- `eth/protocols/eth/handler.go` - Message handlers
- `eth/protocols/eth/peer.go` - Head/TD storage
- `p2p/rlpx/rlpx.go` - Pre-EIP-8 support
- `p2p/server.go` - Static peer handling
- `p2p/discover/v4_udp.go` - XDC discovery

## References

- XDC Network: https://xdc.network
- XDC v2.6.8 source: https://github.com/XinFinOrg/XDPoSChain

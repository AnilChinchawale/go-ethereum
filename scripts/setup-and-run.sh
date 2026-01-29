#!/bin/bash
# XDC Node Setup and Run Script
# Usage: ./scripts/setup-and-run.sh [--init] [--peer ENODE_URL]

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
DATA_DIR="$PROJECT_DIR/testdata"
GENESIS_URL="https://raw.githubusercontent.com/XinFinOrg/XinFin-Node/master/mainnet/genesis.json"

# Default XDC mainnet bootnodes
BOOTNODES=(
    "enode://81d93b8dc627ec07742d2d174c3ea36db0d78794b224cd8a28e9bc2f1892b2fd39a1d8088198a70b9069e54d1f3077b88bceff1cd4989ae977e992f524607905@159.89.174.254:30303"
)

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log() { echo -e "${GREEN}[INFO]${NC} $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

# Parse arguments
INIT=false
CUSTOM_PEER=""
while [[ $# -gt 0 ]]; do
    case $1 in
        --init|-i)
            INIT=true
            shift
            ;;
        --peer|-p)
            CUSTOM_PEER="$2"
            shift 2
            ;;
        --help|-h)
            echo "Usage: $0 [--init] [--peer ENODE_URL]"
            echo "  --init, -i     Initialize fresh chain data"
            echo "  --peer, -p     Add custom peer enode URL"
            exit 0
            ;;
        *)
            shift
            ;;
    esac
done

cd "$PROJECT_DIR"

# Check if geth binary exists
if [[ ! -f "build/bin/geth" ]]; then
    log "Building geth..."
    make geth || error "Build failed"
fi

# Create data directory
mkdir -p "$DATA_DIR"

# Download genesis if needed
if [[ ! -f "$DATA_DIR/genesis.json" ]]; then
    log "Downloading XDC mainnet genesis..."
    curl -sL "$GENESIS_URL" -o "$DATA_DIR/genesis.json" || error "Failed to download genesis"
fi

# Initialize if requested or if chaindata doesn't exist
if [[ "$INIT" == "true" ]] || [[ ! -d "$DATA_DIR/geth/chaindata" ]] || [[ -z "$(ls -A "$DATA_DIR/geth/chaindata" 2>/dev/null)" ]]; then
    log "Initializing chain data..."
    rm -rf "$DATA_DIR/geth/chaindata" "$DATA_DIR/geth/triedb" 2>/dev/null || true
    ./build/bin/geth --datadir "$DATA_DIR" init "$DATA_DIR/genesis.json" || error "Init failed"
fi

# Kill any existing instance
pkill -f "geth.*--datadir.*testdata" 2>/dev/null || true
sleep 1

# Get external IP
EXTERNAL_IP=$(curl -s ifconfig.me 2>/dev/null || echo "127.0.0.1")
log "External IP: $EXTERNAL_IP"

# Build bootnodes list
BOOTNODE_LIST=""
for BOOTNODE in "${BOOTNODES[@]}"; do
    if [[ -n "$BOOTNODE_LIST" ]]; then
        BOOTNODE_LIST="$BOOTNODE_LIST,$BOOTNODE"
    else
        BOOTNODE_LIST="$BOOTNODE"
    fi
done
if [[ -n "$CUSTOM_PEER" ]]; then
    if [[ -n "$BOOTNODE_LIST" ]]; then
        BOOTNODE_LIST="$BOOTNODE_LIST,$CUSTOM_PEER"
    else
        BOOTNODE_LIST="$CUSTOM_PEER"
    fi
fi

# Start the node
log "Starting XDC node..."
log "Bootnodes: $BOOTNODE_LIST"
./build/bin/geth \
    --datadir "$DATA_DIR" \
    --networkid 50 \
    --port 30304 \
    --syncmode full \
    --verbosity 4 \
    --nat "extip:$EXTERNAL_IP" \
    --nodiscover \
    --bootnodes "$BOOTNODE_LIST" \
    --http \
    --http.addr 0.0.0.0 \
    --http.port 8547 \
    --http.api eth,net,web3,admin,debug,txpool \
    --http.vhosts="*" \
    > "$DATA_DIR/geth.log" 2>&1 &

GETH_PID=$!
log "Started geth (PID: $GETH_PID)"

# Wait for RPC to be ready
log "Waiting for RPC..."
for i in {1..30}; do
    if curl -s http://127.0.0.1:8547 -X POST -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' > /dev/null 2>&1; then
        break
    fi
    sleep 1
done

# Check current block
BLOCK_HEX=$(curl -s http://127.0.0.1:8547 -X POST -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' | jq -r '.result')
BLOCK=$((16#${BLOCK_HEX#0x}))
log "Current block: $BLOCK"

# Add peers
log "Adding peers..."
if [[ -n "$CUSTOM_PEER" ]]; then
    curl -s http://127.0.0.1:8547 -X POST -H "Content-Type: application/json" \
        -d "{\"jsonrpc\":\"2.0\",\"method\":\"admin_addPeer\",\"params\":[\"$CUSTOM_PEER\"],\"id\":1}" > /dev/null
    log "Added custom peer"
fi

for BOOTNODE in "${BOOTNODES[@]}"; do
    curl -s http://127.0.0.1:8547 -X POST -H "Content-Type: application/json" \
        -d "{\"jsonrpc\":\"2.0\",\"method\":\"admin_addPeer\",\"params\":[\"$BOOTNODE\"],\"id\":1}" > /dev/null
done
log "Added ${#BOOTNODES[@]} bootnode(s)"

# Print status
echo ""
echo "=========================================="
echo "XDC Node Running"
echo "=========================================="
echo "PID:        $GETH_PID"
echo "Data Dir:   $DATA_DIR"
echo "RPC:        http://127.0.0.1:8547"
echo "Log:        $DATA_DIR/geth.log"
echo ""
echo "Commands:"
echo "  Check block:  curl -s http://127.0.0.1:8547 -X POST -H 'Content-Type: application/json' -d '{\"jsonrpc\":\"2.0\",\"method\":\"eth_blockNumber\",\"params\":[],\"id\":1}'"
echo "  Check peers:  curl -s http://127.0.0.1:8547 -X POST -H 'Content-Type: application/json' -d '{\"jsonrpc\":\"2.0\",\"method\":\"admin_peers\",\"params\":[],\"id\":1}' | jq '.result | length'"
echo "  View logs:    tail -f $DATA_DIR/geth.log"
echo "  Stop:         kill $GETH_PID"
echo "=========================================="

# Monitor sync progress
log "Monitoring sync (Ctrl+C to stop monitoring, node continues running)..."
while true; do
    sleep 10
    BLOCK_HEX=$(curl -s http://127.0.0.1:8547 -X POST -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' 2>/dev/null | jq -r '.result' 2>/dev/null)
    if [[ -n "$BLOCK_HEX" && "$BLOCK_HEX" != "null" ]]; then
        BLOCK=$((16#${BLOCK_HEX#0x}))
        PEERS=$(curl -s http://127.0.0.1:8547 -X POST -H "Content-Type: application/json" \
            -d '{"jsonrpc":"2.0","method":"admin_peers","params":[],"id":1}' 2>/dev/null | jq '.result | length' 2>/dev/null)
        echo -ne "\rBlock: $BLOCK | Peers: ${PEERS:-0}    "
    fi
done

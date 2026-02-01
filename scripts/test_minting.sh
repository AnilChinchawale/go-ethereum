#!/bin/bash
# Test script for XDPoS block minting
# Creates a private network and tests block production

set -e

DATADIR="/tmp/xdpos-test"
GETH="/root/go-ethereum-pr5/build/bin/geth"

# Clean up
rm -rf $DATADIR

# Create account
echo "Creating test account..."
mkdir -p $DATADIR
echo "password" > $DATADIR/password.txt

# Generate account
ACCOUNT=$($GETH account new --datadir $DATADIR --password $DATADIR/password.txt 2>&1 | grep -oE '0x[a-fA-F0-9]{40}')
echo "Created account: $ACCOUNT"

# Create genesis with XDPoS
cat > $DATADIR/genesis.json << EOF
{
  "config": {
    "chainId": 50,
    "homesteadBlock": 0,
    "eip150Block": 0,
    "eip155Block": 0,
    "eip158Block": 0,
    "byzantiumBlock": 0,
    "constantinopleBlock": 0,
    "petersburgBlock": 0,
    "istanbulBlock": 0,
    "berlinBlock": 0,
    "londonBlock": 0,
    "XDPoS": {
      "period": 2,
      "epoch": 900,
      "gap": 450
    }
  },
  "difficulty": "1",
  "gasLimit": "85000000",
  "extraData": "0x0000000000000000000000000000000000000000000000000000000000000000${ACCOUNT:2}0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
  "alloc": {
    "${ACCOUNT}": {
      "balance": "100000000000000000000000000"
    }
  }
}
EOF

# Initialize genesis
echo "Initializing genesis..."
$GETH init --datadir $DATADIR $DATADIR/genesis.json

# Start node in background
echo "Starting node..."
$GETH --datadir $DATADIR \
    --networkid 50 \
    --http \
    --http.addr "0.0.0.0" \
    --http.port 8545 \
    --http.api "eth,net,web3,xdpos,miner" \
    --allow-insecure-unlock \
    --unlock $ACCOUNT \
    --password $DATADIR/password.txt \
    --verbosity 3 \
    2>&1 | tee $DATADIR/geth.log &

GETH_PID=$!
sleep 5

# Check if running
if ! kill -0 $GETH_PID 2>/dev/null; then
    echo "Geth failed to start"
    cat $DATADIR/geth.log
    exit 1
fi

echo "Node started with PID $GETH_PID"
echo "Account: $ACCOUNT"
echo ""
echo "To check mining status:"
echo "  curl -X POST http://localhost:8545 -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","method":"eth_mining","params":[],"id":1}'"
echo ""
echo "To start mining via RPC:"
echo "  curl -X POST http://localhost:8545 -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","method":"miner_start","params":[1],"id":1}'"
echo ""
echo "Log file: $DATADIR/geth.log"
echo "To stop: kill $GETH_PID"

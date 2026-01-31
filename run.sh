#!/bin/bash
# XDC-Geth PR5 Run Script
# Usage: ./run.sh [--init] [--peer "enode://..."]

set -e

DATADIR="${DATADIR:-$HOME/.xdc-pr5}"
NETWORKID=50
PORT=30309
RPCPORT=8560

INIT=false
PEER=""

while [[ $# -gt 0 ]]; do
    case $1 in
        --init) INIT=true; shift ;;
        --peer) PEER="$2"; shift 2 ;;
        *) echo "Unknown: $1"; exit 1 ;;
    esac
done

[ ! -f "./build/bin/geth" ] && make geth

if [ "$INIT" = true ]; then
    rm -rf "$DATADIR"
    mkdir -p "$DATADIR"
    [ ! -f "genesis/mainnet.json" ] && mkdir -p genesis && curl -sL "genesis/mainnet.json" -o genesis/mainnet.json
    ./build/bin/geth --datadir "$DATADIR" init genesis/mainnet.json
fi

CMD="./build/bin/geth --datadir $DATADIR --networkid $NETWORKID --syncmode full --port $PORT --http --http.addr 0.0.0.0 --http.port $RPCPORT --http.api admin,eth,net,web3,debug,txpool,xdpos --authrpc.port 8561 --db.engine pebble --maxpeers 50 --verbosity 3"

[ -n "$PEER" ] && mkdir -p "$DATADIR/geth" && echo "[\"$PEER\"]" > "$DATADIR/geth/static-nodes.json"

BOOTNODES="enode://d860a01f9722d78051619d1e2351aba3f43f943f6f00718d1b9baa4101932a1f5011f16bb2b1bb35db20d6fe28fa0bf09636d26a87d31de9ec6203eeedb1f666@18.138.108.67:30304,enode://e1a69a7d766576e694adc3fc78d801a8a66926cbe8f4fe95b85f3b481444700a5d1b6d440b2715b5bb7cf4824df6a6702740afc8c52b20c72bc8c16f1ccde1f3@149.102.140.32:30304"

echo "Starting XDC-Geth PR5 (RPC: http://localhost:$RPCPORT)"
exec $CMD --bootnodes "$BOOTNODES"

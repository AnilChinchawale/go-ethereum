#!/bin/bash
cd /root/xdpos-part4
pkill -9 -f "build/bin/geth" 2>/dev/null
rm -f testdata/geth.ipc
sleep 1

./build/bin/geth \
    --datadir testdata \
    --networkid 50 \
    --port 30304 \
    --syncmode full \
    --verbosity 4 \
    --nat extip:95.217.56.168 \
    --nodiscover \
    --http \
    --http.addr 0.0.0.0 \
    --http.port 8547 \
    --http.api eth,net,web3,admin,debug,txpool \
    --http.vhosts=* \
    > /tmp/geth.log 2>&1 &

echo "Started: $!"
sleep 8
curl -s http://127.0.0.1:8547 -X POST -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'

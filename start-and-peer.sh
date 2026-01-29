#!/bin/bash
pkill -9 -f "build/bin/geth" 2>/dev/null
sleep 1

rm -f /tmp/geth.log
cd /root/xdpos-part4
./build/bin/geth --datadir testdata --networkid 50 --port 30304 --syncmode full --verbosity 5 --nat extip:95.217.56.168 --nodiscover --http --http.addr 0.0.0.0 --http.port 8547 --http.api eth,net,web3,admin,debug,txpool --http.vhosts=* >> /tmp/geth.log 2>&1 &
echo "PID: $!"
sleep 5

curl -s http://127.0.0.1:8547 -X POST -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"admin_addPeer","params":["enode://81d93b8dc627ec07742d2d174c3ea36db0d78794b224cd8a28e9bc2f1892b2fd39a1d8088198a70b9069e54d1f3077b88bceff1cd4989ae977e992f524607905@172.18.0.2:30303"],"id":1}'

sleep 15

echo "=== Connection logs ==="
grep -iE "(dial|connect|peer|Adding|Removing|handshake)" /tmp/geth.log | tail -20
echo ""
echo "=== Peer count ==="
curl -s http://127.0.0.1:8547 -X POST -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"net_peerCount","params":[],"id":1}'

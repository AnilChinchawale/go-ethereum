#!/bin/bash
# XDC Mainnet Sync Test Script
# Usage: ./test-xdc-mainnet.sh

set -e

DATADIR="/tmp/xdc-part4-mainnet"
PORT=30306
RPCPORT=18546

echo "=== Building Part4 Geth ==="
cd /root/xdpos-part4
make geth

echo ""
echo "=== Initializing Genesis (if needed) ==="
if [ ! -d "$DATADIR/geth/chaindata" ]; then
    ./build/bin/geth init --datadir "$DATADIR" genesis/xdc_mainnet.json
    echo "Genesis initialized"
else
    echo "Genesis already initialized"
fi

echo ""
echo "=== Starting Node ==="
./build/bin/geth \
    --datadir "$DATADIR" \
    --networkid 50 \
    --syncmode full \
    --port $PORT \
    --http --http.port $RPCPORT --http.addr 0.0.0.0 \
    --http.api "admin,eth,net,web3,debug,txpool" \
    --http.corsdomain "*" \
    --ethstats "04.xdcrpc.com_OpenScan.AI:xinfin_xdpos_hybrid_network_stats@stats.xinfin.network:3000" \
    --bootnodes "enode://e1a69a7d766576e694adc3fc78d801a8a66926cbe8f4fe95b85f3b481444700a5d1b6d440b2715b5bb7cf4824df6a6702740afc8c52b20c72bc8c16f1ccde1f3@149.102.140.32:30303,enode://874589626a2b4fd7c57202533315885815eba51dbc434db88bbbebcec9b22cf2a01eafad2fd61651306fe85321669a30b3f41112eca230137ded24b86e064ba8@5.189.144.192:30303,enode://ccdef92053c8b9622180d02a63edffb3e143e7627737ea812b930eacea6c51f0c93a5da3397f59408c3d3d1a9a381f7e0b07440eae47314685b649a03408cfdd@37.60.243.5:30303,enode://12711126475d7924af98d359e178f71c5d9607de32d2c5b4ab1afff4b0bb16b793b4bbda0a42bf41a309e5349b6106d053ae4ae92aa848b5879e3ef3687c6203@89.117.49.48:30303" \
    --verbosity 4

// Copyright 2020 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package eth

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

// ethHandler implements the eth.Backend interface to handle the various network
// packets that are sent as replies or broadcasts.
type ethHandler handler

func (h *ethHandler) Chain() *core.BlockChain { return h.chain }
func (h *ethHandler) TxPool() eth.TxPool      { return h.txpool }

// RunPeer is invoked when a peer joins on the `eth` protocol.
func (h *ethHandler) RunPeer(peer *eth.Peer, hand eth.Handler) error {
	return (*handler)(h).runEthPeer(peer, hand)
}

// PeerInfo retrieves all known `eth` information about a peer.
func (h *ethHandler) PeerInfo(id enode.ID) interface{} {
	if p := h.peers.peer(id.String()); p != nil {
		return p.info()
	}
	return nil
}

// AcceptTxs retrieves whether transaction processing is enabled on the node
// or if inbound transactions should simply be dropped.
func (h *ethHandler) AcceptTxs() bool {
	return h.synced.Load()
}

// Handle is invoked from a peer's message handler when it receives a new remote
// message that the handler couldn't consume and serve itself.
func (h *ethHandler) Handle(peer *eth.Peer, packet eth.Packet) error {
	// Consume any broadcasts and announces, forwarding the rest to the downloader
	switch packet := packet.(type) {
	case *eth.NewPooledTransactionHashesPacket:
		return h.txFetcher.Notify(peer.ID(), packet.Types, packet.Sizes, packet.Hashes)

	case *eth.TransactionsPacket:
		for _, tx := range *packet {
			if tx.Type() == types.BlobTxType {
				return errors.New("disallowed broadcast blob transaction")
			}
		}
		return h.txFetcher.Enqueue(peer.ID(), *packet, false)

	case *eth.PooledTransactionsResponse:
		// If we receive any blob transactions missing sidecars, or with
		// sidecars that don't correspond to the versioned hashes reported
		// in the header, disconnect from the sending peer.
		for _, tx := range *packet {
			if tx.Type() == types.BlobTxType {
				if tx.BlobTxSidecar() == nil {
					return errors.New("received sidecar-less blob transaction")
				}
				if err := tx.BlobTxSidecar().ValidateBlobCommitmentHashes(tx.BlobHashes()); err != nil {
					return err
				}
			}
		}
		return h.txFetcher.Enqueue(peer.ID(), *packet, true)

	// XDC pre-merge block announcements and broadcasts
	case *eth.NewBlockHashesPacket:
		// Mark the hashes as present at the remote peer
		hashes := make([]common.Hash, len(*packet))
		for i, block := range *packet {
			hashes[i] = block.Hash
		}
		// Trigger sync if we're behind
		go func() {
			if err := (*handler)(h).downloader.LegacySync(); err != nil {
				log.Trace("Legacy sync from block hashes failed", "err", err)
			}
		}()
		return nil

	case *eth.NewBlockPacket:
		// A new block was broadcast - trigger sync to catch up
		go func() {
			if err := (*handler)(h).downloader.LegacySync(); err != nil {
				log.Trace("Legacy sync from new block failed", "err", err)
			}
		}()
		return nil

	// XDPoS2 consensus messages
	case *types.Vote:
		log.Trace("Received vote message", "peer", peer.ID()[:16], "hash", packet.Hash().Hex())
		if h.bftHandler != nil {
			return h.bftHandler.HandleVote(peer, packet)
		}
		return nil

	case *types.Timeout:
		log.Trace("Received timeout message", "peer", peer.ID()[:16], "hash", packet.Hash().Hex())
		if h.bftHandler != nil {
			return h.bftHandler.HandleTimeout(peer, packet)
		}
		return nil

	case *types.SyncInfo:
		log.Trace("Received syncInfo message", "peer", peer.ID()[:16], "hash", packet.Hash().Hex())
		if h.bftHandler != nil {
			return h.bftHandler.HandleSyncInfo(peer, packet)
		}
		return nil

	case *eth.BlockHeadersRequest:
		// Legacy block headers response (for XDC compatibility)
		// BlockHeadersRequest is actually headers data despite the name - it's []*types.Header
		headers := ([]*types.Header)(*packet)
		log.Info("XDC: Received legacy block headers", "count", len(headers), "peer", peer.ID()[:16])
		
		if len(headers) > 0 {
			if h.xdcSyncer != nil {
				// Process headers through xdcSyncer which will fetch bodies and import blocks
				go h.xdcSyncer.processHeaders(peer, headers)
			} else {
				log.Warn("XDC: Received headers but xdcSyncer not available")
			}
		}
		return nil

	case *eth.BlockBodiesResponse:
		// Legacy block bodies response (for XDC compatibility)
		bodies := ([]*eth.BlockBody)(*packet)
		log.Info("XDC: Received legacy block bodies", "count", len(bodies), "peer", peer.ID()[:16])
		
		if len(bodies) > 0 {
			if h.xdcSyncer != nil {
				// Process bodies through xdcSyncer
				go h.xdcSyncer.processBodies(peer, bodies)
			} else {
				log.Warn("XDC: Received bodies but xdcSyncer not available")
			}
		}
		return nil

	default:
		return fmt.Errorf("unexpected eth packet type: %T", packet)
	}
}

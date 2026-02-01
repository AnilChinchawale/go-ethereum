// Copyright 2026 The go-ethereum Authors
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

package downloader

import (
	"errors"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
)

var (
	errNoPeers        = errors.New("no peers available for sync")
	errPeerNotFound   = errors.New("peer not found")
	errInvalidHeaders = errors.New("invalid headers received")
)

// XDCSyncWithPeer is a placeholder for direct peer sync (not currently used)
func (d *Downloader) XDCSyncWithPeer(peerID string, head common.Hash, td *big.Int) error {
	return d.XDCSync()
}

// fetchHeadersByNumber fetches headers starting from a specific number
func (d *Downloader) fetchHeadersByNumber(p *peerConnection, from uint64, amount int, skip int, reverse bool) ([]*types.Header, error) {
	start := time.Now()
	resCh := make(chan *eth.Response)

	req, err := p.peer.RequestHeadersByNumber(from, amount, skip, reverse, resCh)
	if err != nil {
		return nil, err
	}
	defer req.Close()

	ttl := d.peers.rates.TargetTimeout()
	timeoutTimer := time.NewTimer(ttl)
	defer timeoutTimer.Stop()

	select {
	case <-timeoutTimer.C:
		p.log.Debug("Header request timed out", "elapsed", ttl)
		return nil, errTimeout

	case res := <-resCh:
		headerReqTimer.Update(time.Since(start))
		res.Done <- nil
		return *res.Res.(*eth.BlockHeadersRequest), nil
	}
}

// fetchBodiesByHash fetches block bodies by hash
func (d *Downloader) fetchBodiesByHash(p *peerConnection, hashes []common.Hash) ([]*types.Body, error) {
	start := time.Now()
	resCh := make(chan *eth.Response)

	req, err := p.peer.RequestBodies(hashes, resCh)
	if err != nil {
		return nil, err
	}
	defer req.Close()

	ttl := d.peers.rates.TargetTimeout()
	timeoutTimer := time.NewTimer(ttl)
	defer timeoutTimer.Stop()

	select {
	case <-timeoutTimer.C:
		p.log.Debug("Body request timed out", "elapsed", ttl)
		return nil, errTimeout

	case res := <-resCh:
		bodyReqTimer.Update(time.Since(start))
		res.Done <- nil

		// Convert response to bodies
		response := res.Res.(*eth.BlockBodiesResponse)
		bodies := make([]*types.Body, len(*response))
		for i, body := range *response {
			bodies[i] = &types.Body{
				Transactions: body.Transactions,
				Uncles:       body.Uncles,
			}
		}
		return bodies, nil
	}
}

// XDCSync finds the best peer and syncs with them
func (d *Downloader) XDCSync() error {
	peers := d.peers.AllPeers()
	if len(peers) == 0 {
		return errNoPeers
	}

	// Make sure only one goroutine is ever allowed past this point at once
	if !d.synchronising.CompareAndSwap(false, true) {
		return errBusy
	}
	defer d.synchronising.Store(false)

	// Post a user notification of the sync (only once per session)
	if d.notified.CompareAndSwap(false, true) {
		log.Info("XDC Block synchronisation started")
	}

	d.mux.Post(StartEvent{})
	defer func() {
		d.mux.Post(DoneEvent{d.blockchain.CurrentHeader()})
	}()

	// Use the first peer
	peer := peers[0]
	localHead := d.blockchain.CurrentBlock()
	
	log.Info("XDC sync starting", "peer", peer.id, "localHead", localHead.Number.Uint64())

	// Find the remote peer's head by binary search
	// Start from a reasonable estimate and adjust
	localHeight := localHead.Number.Uint64()
	
	// Try to find how far the peer is by requesting progressively higher blocks
	searchHeight := localHeight + 1000000 // Start 1M blocks ahead
	if searchHeight < 100000 {
		searchHeight = 100000
	}
	
	// Binary search to find approximate remote head
	low := localHeight
	high := searchHeight
	remoteHeight := localHeight
	
	for low < high {
		mid := (low + high + 1) / 2
		headers, err := d.fetchHeadersByNumber(peer, mid, 1, 0, false)
		if err != nil || len(headers) == 0 {
			// Peer doesn't have this block, search lower
			high = mid - 1
		} else {
			// Peer has this block, search higher
			remoteHeight = mid
			low = mid
			if high - low <= 1000 {
				break // Close enough
			}
		}
	}
	
	// Get exact remote head by scanning forward
	for {
		headers, err := d.fetchHeadersByNumber(peer, remoteHeight+1, 128, 0, false)
		if err != nil || len(headers) == 0 {
			break
		}
		remoteHeight = headers[len(headers)-1].Number.Uint64()
	}
	
	log.Info("Remote head found", "number", remoteHeight)

	if localHeight >= remoteHeight {
		log.Info("Already synced or ahead", "local", localHeight, "remote", remoteHeight)
		return nil
	}

	log.Info("Starting XDC block sync", "from", localHeight, "to", remoteHeight, "blocks", remoteHeight-localHeight)

	// Sync in batches
	batchSize := 128 // Headers per batch
	current := localHeight

	for current < remoteHeight {
		// Calculate batch end
		end := current + uint64(batchSize)
		if end > remoteHeight {
			end = remoteHeight
		}

		// Fetch headers
		headers, err := d.fetchHeadersByNumber(peer, current+1, int(end-current), 0, false)
		if err != nil {
			log.Error("Failed to fetch headers", "from", current+1, "err", err)
			return err
		}

		if len(headers) == 0 {
			log.Warn("No headers received", "from", current+1)
			break
		}

		// Fetch bodies for these headers
		hashes := make([]common.Hash, len(headers))
		for i, h := range headers {
			hashes[i] = h.Hash()
		}

		bodies, err := d.fetchBodiesByHash(peer, hashes)
		if err != nil {
			log.Error("Failed to fetch bodies", "err", err)
			return err
		}

		// Construct and import blocks
		blocks := make([]*types.Block, len(headers))
		for i, header := range headers {
			if i < len(bodies) && bodies[i] != nil {
				blocks[i] = types.NewBlockWithHeader(header).WithBody(*bodies[i])
			} else {
				blocks[i] = types.NewBlockWithHeader(header).WithBody(types.Body{})
			}
		}

		// Import blocks
		if _, err := d.blockchain.InsertChain(blocks); err != nil {
			log.Error("Failed to import blocks", "err", err)
			return err
		}

		current = headers[len(headers)-1].Number.Uint64()
		log.Info("Imported blocks", "count", len(blocks), "head", current, "target", remoteHeight)
	}

	log.Info("XDC sync completed", "head", d.blockchain.CurrentBlock().Number.Uint64())
	return nil
}

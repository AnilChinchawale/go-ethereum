// Copyright 2024 XDC Network
// XDC pre-merge sync implementation for go-ethereum compatibility

package eth

import (
	"math/big"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
)

const (
	xdcForceSyncCycle = 10 * time.Second // Interval to force sync attempts
	xdcMinPeers       = 1                // Minimum peers to start syncing
)

// xdcSyncer manages pre-merge sync for XDC network
type xdcSyncer struct {
	handler    *handler
	syncing    atomic.Bool
	quitCh     chan struct{}
	newPeerCh  chan *eth.Peer
}

// newXDCSyncer creates a new XDC syncer
func newXDCSyncer(h *handler) *xdcSyncer {
	return &xdcSyncer{
		handler:   h,
		quitCh:    make(chan struct{}),
		newPeerCh: make(chan *eth.Peer, 10),
	}
}

// start begins the sync loop
func (s *xdcSyncer) start() {
	go s.loop()
}

// stop terminates the sync loop
func (s *xdcSyncer) stop() {
	close(s.quitCh)
}

// notifyPeer signals that a new peer is available
func (s *xdcSyncer) notifyPeer(peer *eth.Peer) {
	select {
	case s.newPeerCh <- peer:
	default:
		// Channel full, skip notification
	}
}

// loop is the main sync loop
func (s *xdcSyncer) loop() {
	forceSync := time.NewTicker(xdcForceSyncCycle)
	defer forceSync.Stop()

	for {
		select {
		case peer := <-s.newPeerCh:
			// New peer connected, try to sync
			go s.synchronise(peer)

		case <-forceSync.C:
			// Periodically try to sync with best peer
			if peer := s.bestPeer(); peer != nil {
				go s.synchronise(peer)
			}

		case <-s.quitCh:
			return
		}
	}
}

// bestPeer finds the peer with highest TD
func (s *xdcSyncer) bestPeer() *eth.Peer {
	var (
		bestPeer *eth.Peer
		bestTd   *big.Int
	)

	// Iterate through all peers
	for _, p := range s.handler.peers.all() {
		if p.Peer == nil {
			continue
		}
		_, td := p.Peer.Head()
		if td != nil && (bestTd == nil || td.Cmp(bestTd) > 0) {
			bestPeer = p.Peer
			bestTd = td
		}
	}
	return bestPeer
}

// synchronise attempts to sync with a peer
func (s *xdcSyncer) synchronise(peer *eth.Peer) {
	if peer == nil {
		log.Debug("XDC sync: nil peer")
		return
	}

	log.Info("XDC sync: synchronise called", "peer", peer.ID()[:16])

	// Only one sync at a time
	if !s.syncing.CompareAndSwap(false, true) {
		log.Debug("XDC sync: already syncing")
		return
	}
	defer s.syncing.Store(false)

	// Get peer's head
	peerHead, peerTd := peer.Head()
	log.Info("XDC sync: peer head info", "peer", peer.ID()[:16], "head", peerHead.Hex()[:16], "td", peerTd)
	
	// For XDC, if TD is nil, assume peer is ahead (we're starting from genesis)
	if peerTd == nil {
		// Use a large TD to force sync
		peerTd = big.NewInt(100000000)
		log.Info("XDC sync: using default TD for peer", "td", peerTd)
	}

	// Get our current head
	currentBlock := s.handler.chain.CurrentBlock()
	// For XDC pre-merge, use block number as approximation of TD
	currentTd := new(big.Int).SetUint64(currentBlock.Number.Uint64())

	// Check if peer is ahead (use block number comparison for simplicity)
	if peerTd.Cmp(currentTd) <= 0 {
		log.Debug("XDC sync: peer not ahead", "peer", peer.ID(), "peerTd", peerTd, "ourBlock", currentBlock.Number.Uint64())
		return
	}

	log.Info("XDC sync: starting synchronisation",
		"peer", peer.ID()[:8],
		"peerHead", peerHead.Hex()[:10],
		"peerTd", peerTd,
		"ourBlock", currentBlock.Number.Uint64(),
	)

	// Request headers starting from our current head
	origin := currentBlock.Number.Uint64()
	if origin > 0 {
		origin++ // Start from next block
	}

	// Use the downloader to fetch blocks
	// For now, request headers directly to test
	s.requestHeaders(peer, origin)
}

// requestHeaders requests block headers from a peer
func (s *xdcSyncer) requestHeaders(peer *eth.Peer, from uint64) {
	// For XDC, we need to sync from genesis or a checkpoint
	// Start with a smaller batch to test
	const batchSize = 64

	// If starting from 0, try to get recent headers first (skeleton sync approach)
	// to verify peer is responsive
	if from == 0 {
		log.Info("XDC sync: starting from genesis, requesting first batch")
		from = 1 // Start from block 1, not 0
	}

	log.Info("XDC sync: requesting headers", "peer", peer.ID()[:8], "from", from, "count", batchSize)

	// Use legacy request format for XDC (eth/62-63 compatible, no RequestId)
	if err := peer.RequestHeadersByNumberLegacy(from, batchSize, 0, false); err != nil {
		log.Error("XDC sync: failed to request headers", "err", err)
		return
	}

	// Headers will arrive via the message handler
	// For now, just wait and let the handler process them
	log.Info("XDC sync: header request sent, waiting for response via handler")
}

// processHeaders processes received headers and requests bodies
func (s *xdcSyncer) processHeaders(peer *eth.Peer, headers []*types.Header) {
	if len(headers) == 0 {
		return
	}

	log.Info("XDC sync: processing headers",
		"count", len(headers),
		"first", headers[0].Number.Uint64(),
		"last", headers[len(headers)-1].Number.Uint64(),
	)

	// Verify headers can be connected to our chain
	currentBlock := s.handler.chain.CurrentBlock()
	if headers[0].Number.Uint64() != currentBlock.Number.Uint64()+1 {
		log.Warn("XDC sync: headers don't connect to chain",
			"expected", currentBlock.Number.Uint64()+1,
			"got", headers[0].Number.Uint64(),
		)
		return
	}

	// Request block bodies for these headers
	s.requestBodies(peer, headers)
}

// requestBodies requests block bodies for the given headers
func (s *xdcSyncer) requestBodies(peer *eth.Peer, headers []*types.Header) {
	// Collect hashes for body request
	hashes := make([]common.Hash, len(headers))
	for i, h := range headers {
		hashes[i] = h.Hash()
	}

	log.Info("XDC sync: requesting bodies", "peer", peer.ID()[:8], "count", len(hashes))

	// Create response channel
	resCh := make(chan *eth.Response, 1)

	// Request bodies
	req, err := peer.RequestBodies(hashes, resCh)
	if err != nil {
		log.Error("XDC sync: failed to request bodies", "err", err)
		return
	}
	defer req.Close()

	// Wait for response
	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()

	select {
	case res := <-resCh:
		if res.Res == nil {
			log.Warn("XDC sync: empty bodies response")
			res.Done <- nil
			return
		}

		bodies, ok := res.Res.(*eth.BlockBodiesResponse)
		if !ok {
			log.Error("XDC sync: unexpected bodies response type")
			res.Done <- nil
			return
		}

		log.Info("XDC sync: received bodies", "count", len(*bodies))

		// Assemble and import blocks
		s.importBlocks(headers, *bodies)

		res.Done <- nil

	case <-timeout.C:
		log.Warn("XDC sync: bodies request timed out")

	case <-s.quitCh:
		return
	}
}

// importBlocks assembles headers and bodies into full blocks and imports them
func (s *xdcSyncer) importBlocks(headers []*types.Header, bodies []*eth.BlockBody) {
	if len(headers) != len(bodies) {
		log.Error("XDC sync: header/body count mismatch", "headers", len(headers), "bodies", len(bodies))
		return
	}

	blocks := make([]*types.Block, len(headers))
	for i, header := range headers {
		body := bodies[i]
		block := types.NewBlockWithHeader(header).WithBody(types.Body{
			Transactions: body.Transactions,
			Uncles:       body.Uncles,
		})
		blocks[i] = block
	}

	log.Info("XDC sync: importing blocks",
		"count", len(blocks),
		"first", blocks[0].NumberU64(),
		"last", blocks[len(blocks)-1].NumberU64(),
	)

	// Insert blocks into chain
	n, err := s.handler.chain.InsertChain(blocks)
	if err != nil {
		log.Error("XDC sync: block import failed", "imported", n, "err", err)
	} else {
		log.Info("XDC sync: blocks imported successfully", "count", n)
		
		// Mark as synced if we imported blocks
		if n > 0 {
			s.handler.synced.Store(true)
		}
	}
}

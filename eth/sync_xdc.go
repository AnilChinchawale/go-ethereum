// Copyright 2024 XDC Network
// XDC pre-merge sync implementation for go-ethereum compatibility

package eth

import (
	"math/big"
	"sync"
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
	xdcBatchSize      = 64               // Headers per batch
)

// xdcSyncer manages pre-merge sync for XDC network
type xdcSyncer struct {
	handler    *handler
	syncing    atomic.Bool
	quitCh     chan struct{}
	newPeerCh  chan *eth.Peer
	
	// Pending responses for legacy protocol (no RequestId matching)
	pendingHeaders chan []*types.Header
	pendingBodies  chan []*eth.BlockBody
	pendingLock    sync.Mutex
	waitingPeer    *eth.Peer  // The peer we're expecting a response from
}

// newXDCSyncer creates a new XDC syncer
func newXDCSyncer(h *handler) *xdcSyncer {
	return &xdcSyncer{
		handler:        h,
		quitCh:         make(chan struct{}),
		newPeerCh:      make(chan *eth.Peer, 10),
		pendingHeaders: make(chan []*types.Header, 1),
		pendingBodies:  make(chan []*eth.BlockBody, 1),
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

	// Start sync loop - keep fetching batches until caught up
	s.syncLoop(peer)
}

// syncLoop continuously fetches headers and bodies until caught up
func (s *xdcSyncer) syncLoop(peer *eth.Peer) {
	for {
		select {
		case <-s.quitCh:
			return
		default:
		}

		currentBlock := s.handler.chain.CurrentBlock()
		origin := currentBlock.Number.Uint64() + 1
		
		if origin <= 1 {
			origin = 1 // Start from block 1
		}

		log.Info("XDC sync: requesting headers batch", "peer", peer.ID()[:8], "from", origin, "count", xdcBatchSize)

		// Set this peer as the one we're waiting for
		s.pendingLock.Lock()
		s.waitingPeer = peer
		// Clear any stale pending responses
		select {
		case <-s.pendingHeaders:
		default:
		}
		s.pendingLock.Unlock()

		// Request headers using legacy format (no RequestId)
		if err := peer.RequestHeadersByNumberLegacy(origin, xdcBatchSize, 0, false); err != nil {
			log.Error("XDC sync: failed to request headers", "err", err)
			return
		}

		// Wait for headers response
		timeout := time.NewTimer(30 * time.Second)
		var headers []*types.Header
		
		select {
		case headers = <-s.pendingHeaders:
			timeout.Stop()
			log.Info("XDC sync: received headers via channel", "count", len(headers))
		case <-timeout.C:
			log.Warn("XDC sync: headers request timed out")
			return
		case <-s.quitCh:
			timeout.Stop()
			return
		}

		if len(headers) == 0 {
			log.Info("XDC sync: no more headers, sync complete")
			return
		}

		// Verify headers connect to our chain
		if headers[0].Number.Uint64() != currentBlock.Number.Uint64()+1 {
			log.Warn("XDC sync: headers don't connect",
				"expected", currentBlock.Number.Uint64()+1,
				"got", headers[0].Number.Uint64(),
			)
			return
		}

		// Request bodies for these headers using legacy format
		hashes := make([]common.Hash, len(headers))
		for i, h := range headers {
			hashes[i] = h.Hash()
		}

		log.Info("XDC sync: requesting bodies", "count", len(hashes))

		// Clear any stale pending bodies
		select {
		case <-s.pendingBodies:
		default:
		}

		if err := peer.RequestBodiesLegacy(hashes); err != nil {
			log.Error("XDC sync: failed to request bodies", "err", err)
			return
		}

		// Wait for bodies response
		timeout = time.NewTimer(30 * time.Second)
		var bodies []*eth.BlockBody

		select {
		case bodies = <-s.pendingBodies:
			timeout.Stop()
			log.Info("XDC sync: received bodies via channel", "count", len(bodies))
		case <-timeout.C:
			log.Warn("XDC sync: bodies request timed out")
			return
		case <-s.quitCh:
			timeout.Stop()
			return
		}

		if len(bodies) != len(headers) {
			log.Error("XDC sync: header/body count mismatch", "headers", len(headers), "bodies", len(bodies))
			// Try to import what we can (XDC blocks often have empty bodies)
			for len(bodies) < len(headers) {
				bodies = append(bodies, &eth.BlockBody{})
			}
		}

		// Assemble and import blocks
		if err := s.importBlocks(headers, bodies); err != nil {
			log.Error("XDC sync: block import failed", "err", err)
			return
		}

		// Continue if we got a full batch
		if len(headers) < xdcBatchSize {
			log.Info("XDC sync: received partial batch, sync complete")
			return
		}
	}
}

// processHeaders is called by handler_eth.go when legacy headers arrive
func (s *xdcSyncer) processHeaders(peer *eth.Peer, headers []*types.Header) {
	if len(headers) == 0 {
		log.Debug("XDC sync: received empty headers")
		return
	}

	log.Info("XDC sync: processHeaders called",
		"count", len(headers),
		"first", headers[0].Number.Uint64(),
		"last", headers[len(headers)-1].Number.Uint64(),
		"peer", peer.ID()[:16],
	)

	// Check if we're waiting for this
	s.pendingLock.Lock()
	waiting := s.waitingPeer
	s.pendingLock.Unlock()

	if waiting != nil && waiting.ID() == peer.ID() {
		// This is the response we're waiting for
		select {
		case s.pendingHeaders <- headers:
			log.Debug("XDC sync: headers queued for processing")
		default:
			log.Warn("XDC sync: pendingHeaders channel full, dropping headers")
		}
	} else {
		log.Debug("XDC sync: received unsolicited headers, triggering sync")
		// Unsolicited headers - trigger sync with this peer
		go s.synchronise(peer)
	}
}

// processBodies is called by handler_eth.go when legacy bodies arrive
func (s *xdcSyncer) processBodies(peer *eth.Peer, bodies []*eth.BlockBody) {
	if len(bodies) == 0 {
		log.Debug("XDC sync: received empty bodies")
		return
	}

	log.Info("XDC sync: processBodies called", "count", len(bodies), "peer", peer.ID()[:16])

	// Check if we're waiting for this
	s.pendingLock.Lock()
	waiting := s.waitingPeer
	s.pendingLock.Unlock()

	if waiting != nil && waiting.ID() == peer.ID() {
		// This is the response we're waiting for
		select {
		case s.pendingBodies <- bodies:
			log.Debug("XDC sync: bodies queued for processing")
		default:
			log.Warn("XDC sync: pendingBodies channel full, dropping bodies")
		}
	} else {
		log.Debug("XDC sync: received unsolicited bodies, ignoring")
	}
}

// importBlocks assembles headers and bodies into full blocks and imports them
func (s *xdcSyncer) importBlocks(headers []*types.Header, bodies []*eth.BlockBody) error {
	blocks := make([]*types.Block, len(headers))
	for i, header := range headers {
		var body types.Body
		if i < len(bodies) && bodies[i] != nil {
			body = types.Body{
				Transactions: bodies[i].Transactions,
				Uncles:       bodies[i].Uncles,
			}
		}
		blocks[i] = types.NewBlockWithHeader(header).WithBody(body)
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
		return err
	}
	
	log.Info("XDC sync: blocks imported successfully", 
		"count", n,
		"head", s.handler.chain.CurrentBlock().Number.Uint64(),
	)
	
	// Mark as synced if we imported blocks
	if n > 0 {
		s.handler.synced.Store(true)
	}
	return nil
}

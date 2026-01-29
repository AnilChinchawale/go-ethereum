// Copyright 2024 XDC Network
// XDC pre-merge sync implementation for go-ethereum compatibility

package eth

import (
	"math/big"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
)

const (
	xdcForceSyncCycle = 10 * time.Second // Interval to force sync attempts
	xdcMinPeers       = 1                // Minimum peers to start syncing
)

// xdcSyncer manages pre-merge sync for XDC network
type xdcSyncer struct {
	handler   *handler
	syncing   atomic.Bool
	quitCh    chan struct{}
	newPeerCh chan *eth.Peer
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

// synchronise attempts to sync with a peer using the downloader
func (s *xdcSyncer) synchronise(peer *eth.Peer) {
	if peer == nil {
		log.Debug("XDC sync: nil peer")
		return
	}

	// Only one sync at a time
	if !s.syncing.CompareAndSwap(false, true) {
		log.Debug("XDC sync: already syncing")
		return
	}
	defer s.syncing.Store(false)

	// Get peer's head info
	peerHead, peerTd := peer.Head()
	
	// For XDC, if TD is nil or zero, try to estimate based on block number
	if peerTd == nil || peerTd.Sign() == 0 {
		// Use a large TD to force sync attempt
		peerTd = big.NewInt(100000000)
		log.Info("XDC sync: using default TD for peer", "peer", peer.ID()[:16], "td", peerTd)
	}

	// Get our current state
	currentBlock := s.handler.chain.CurrentBlock()
	ourTd := new(big.Int).SetUint64(currentBlock.Number.Uint64())

	// Check if peer is ahead
	if peerTd.Cmp(ourTd) <= 0 {
		log.Debug("XDC sync: peer not ahead",
			"peer", peer.ID()[:16],
			"peerTd", peerTd,
			"ourBlock", currentBlock.Number.Uint64(),
		)
		return
	}

	log.Info("XDC sync: starting synchronisation",
		"peer", peer.ID()[:16],
		"peerHead", peerHead.Hex()[:16],
		"peerTd", peerTd,
		"ourBlock", currentBlock.Number.Uint64(),
	)

	// Determine sync mode
	mode := downloader.FullSync
	// Could add snap sync support here if needed

	// Use the downloader's XDC sync method
	if err := s.handler.downloader.SynchroniseXDC(peer.ID(), peerHead, peerTd, mode); err != nil {
		log.Warn("XDC sync: synchronisation failed", "peer", peer.ID()[:16], "err", err)
		return
	}

	log.Info("XDC sync: synchronisation completed", "peer", peer.ID()[:16])

	// Mark as synced
	s.handler.synced.Store(true)
}

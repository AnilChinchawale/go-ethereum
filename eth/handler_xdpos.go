// Copyright 2018 XDPoSChain
// BFT message handling for XDPoS 2.0 consensus

package eth

import (
	"sync"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/bft"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
)

const (
	// BFT message cache sizes
	maxKnownVotes     = 131072
	maxKnownTimeouts  = 131072
	maxKnownSyncInfos = 131072
)

// bftPeerState tracks BFT message state for a peer
type bftPeerState struct {
	knownVotes     mapset.Set[common.Hash]
	knownTimeouts  mapset.Set[common.Hash]
	knownSyncInfos mapset.Set[common.Hash]
}

// newBFTPeerState creates a new BFT peer state tracker
func newBFTPeerState() *bftPeerState {
	return &bftPeerState{
		knownVotes:     mapset.NewSet[common.Hash](),
		knownTimeouts:  mapset.NewSet[common.Hash](),
		knownSyncInfos: mapset.NewSet[common.Hash](),
	}
}

// bftHandler manages BFT message handling for the handler
type bftHandler struct {
	handler *handler
	bfter   *bft.Bfter
	
	// Per-peer BFT state
	peerStates map[string]*bftPeerState
	stateLock  sync.RWMutex
}

// newBFTHandler creates a new BFT handler
func newBFTHandler(h *handler) *bftHandler {
	bh := &bftHandler{
		handler:    h,
		peerStates: make(map[string]*bftPeerState),
	}
	
	// Create the BFT message handler with broadcast callbacks
	broadcasts := bft.BroadcastFns{
		Vote:     bh.BroadcastVote,
		Timeout:  bh.BroadcastTimeout,
		SyncInfo: bh.BroadcastSyncInfo,
	}
	
	chainHeightFn := func() uint64 {
		return h.chain.CurrentBlock().Number.Uint64()
	}
	
	bh.bfter = bft.New(broadcasts, h.chain, chainHeightFn)
	bh.bfter.InitEpochNumber()
	
	return bh
}

// Start starts the BFT handler
func (bh *bftHandler) Start() {
	bh.bfter.Start()
}

// Stop stops the BFT handler
func (bh *bftHandler) Stop() {
	bh.bfter.Stop()
}

// getPeerState gets or creates BFT state for a peer
func (bh *bftHandler) getPeerState(id string) *bftPeerState {
	bh.stateLock.Lock()
	defer bh.stateLock.Unlock()
	
	if state, ok := bh.peerStates[id]; ok {
		return state
	}
	state := newBFTPeerState()
	bh.peerStates[id] = state
	return state
}

// removePeerState removes BFT state for a peer
func (bh *bftHandler) removePeerState(id string) {
	bh.stateLock.Lock()
	defer bh.stateLock.Unlock()
	delete(bh.peerStates, id)
}

// HandleVote handles an incoming vote message
func (bh *bftHandler) HandleVote(peer *eth.Peer, vote *types.Vote) error {
	// Mark peer as knowing this vote
	state := bh.getPeerState(peer.ID())
	state.knownVotes.Add(vote.Hash())
	
	// Process through BFT handler
	return bh.bfter.Vote(peer.ID(), vote)
}

// HandleTimeout handles an incoming timeout message
func (bh *bftHandler) HandleTimeout(peer *eth.Peer, timeout *types.Timeout) error {
	// Mark peer as knowing this timeout
	state := bh.getPeerState(peer.ID())
	state.knownTimeouts.Add(timeout.Hash())
	
	// Process through BFT handler
	return bh.bfter.Timeout(peer.ID(), timeout)
}

// HandleSyncInfo handles an incoming syncInfo message
func (bh *bftHandler) HandleSyncInfo(peer *eth.Peer, syncInfo *types.SyncInfo) error {
	// Mark peer as knowing this syncInfo
	state := bh.getPeerState(peer.ID())
	state.knownSyncInfos.Add(syncInfo.Hash())
	
	// Process through BFT handler
	return bh.bfter.SyncInfo(peer.ID(), syncInfo)
}

// BroadcastVote broadcasts a vote to peers that don't have it
func (bh *bftHandler) BroadcastVote(vote *types.Vote) {
	hash := vote.Hash()
	bh.stateLock.RLock()
	peers := bh.handler.peers.all()
	bh.stateLock.RUnlock()
	
	var count int
	for _, peer := range peers {
		state := bh.getPeerState(peer.ID())
		
		// Skip if peer already knows this vote
		if state.knownVotes.Contains(hash) {
			continue
		}
		
		// Mark and send
		for state.knownVotes.Cardinality() >= maxKnownVotes {
			state.knownVotes.Pop()
		}
		state.knownVotes.Add(hash)
		
		if err := peer.SendVote(vote); err != nil {
			log.Debug("[BroadcastVote] Failed to send vote", "peer", peer.ID(), "err", err)
			continue
		}
		count++
	}
	
	if count > 0 {
		log.Trace("Propagated vote", "hash", hash.Hex(),
			"block", vote.ProposedBlockInfo.Hash.Hex(),
			"number", vote.ProposedBlockInfo.Number,
			"round", vote.ProposedBlockInfo.Round,
			"recipients", count)
	}
}

// BroadcastTimeout broadcasts a timeout to peers that don't have it
func (bh *bftHandler) BroadcastTimeout(timeout *types.Timeout) {
	hash := timeout.Hash()
	bh.stateLock.RLock()
	peers := bh.handler.peers.all()
	bh.stateLock.RUnlock()
	
	var count int
	for _, peer := range peers {
		state := bh.getPeerState(peer.ID())
		
		// Skip if peer already knows this timeout
		if state.knownTimeouts.Contains(hash) {
			continue
		}
		
		// Mark and send
		for state.knownTimeouts.Cardinality() >= maxKnownTimeouts {
			state.knownTimeouts.Pop()
		}
		state.knownTimeouts.Add(hash)
		
		if err := peer.SendTimeout(timeout); err != nil {
			log.Debug("[BroadcastTimeout] Failed to send timeout", "peer", peer.ID(), "err", err)
			continue
		}
		count++
	}
	
	if count > 0 {
		log.Trace("Propagated timeout", "hash", hash.Hex(), "round", timeout.Round, "recipients", count)
	}
}

// BroadcastSyncInfo broadcasts a syncInfo to peers that don't have it
func (bh *bftHandler) BroadcastSyncInfo(syncInfo *types.SyncInfo) {
	hash := syncInfo.Hash()
	bh.stateLock.RLock()
	peers := bh.handler.peers.all()
	bh.stateLock.RUnlock()
	
	var count int
	for _, peer := range peers {
		state := bh.getPeerState(peer.ID())
		
		// Skip if peer already knows this syncInfo
		if state.knownSyncInfos.Contains(hash) {
			continue
		}
		
		// Mark and send
		for state.knownSyncInfos.Cardinality() >= maxKnownSyncInfos {
			state.knownSyncInfos.Pop()
		}
		state.knownSyncInfos.Add(hash)
		
		if err := peer.SendSyncInfo(syncInfo); err != nil {
			log.Debug("[BroadcastSyncInfo] Failed to send syncInfo", "peer", peer.ID(), "err", err)
			continue
		}
		count++
	}
	
	if count > 0 {
		log.Trace("Propagated syncInfo", "hash", hash.Hex(), "recipients", count)
	}
}

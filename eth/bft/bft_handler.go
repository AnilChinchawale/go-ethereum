// Copyright 2018 XDPoSChain
// BFT consensus message handler for XDPoS 2.0

package bft

import (
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/lru"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

const (
	maxBlockDist = 7 // Maximum allowed backward distance from the chain head
	
	// Cache sizes for known BFT messages
	maxKnownVotes     = 131072
	maxKnownTimeouts  = 131072
	maxKnownSyncInfos = 131072
)

// BroadcastVoteFn is the callback to broadcast a vote
type BroadcastVoteFn func(*types.Vote)

// BroadcastTimeoutFn is the callback to broadcast a timeout
type BroadcastTimeoutFn func(*types.Timeout)

// BroadcastSyncInfoFn is the callback to broadcast sync info
type BroadcastSyncInfoFn func(*types.SyncInfo)

// ChainHeightFn retrieves the current chain height
type ChainHeightFn func() uint64

// BroadcastFns holds all broadcast callback functions
type BroadcastFns struct {
	Vote     BroadcastVoteFn
	Timeout  BroadcastTimeoutFn
	SyncInfo BroadcastSyncInfoFn
}

// ConsensusFns holds consensus verification and handler functions
type ConsensusFns struct {
	// Verification functions
	VerifyVote     func(consensus.ChainReader, *types.Vote) (bool, error)
	VerifyTimeout  func(consensus.ChainReader, *types.Timeout) (bool, error)
	VerifySyncInfo func(consensus.ChainReader, *types.SyncInfo) (bool, error)

	// Handler functions
	VoteHandler     func(consensus.ChainReader, *types.Vote) error
	TimeoutHandler  func(consensus.ChainReader, *types.Timeout) error
	SyncInfoHandler func(consensus.ChainReader, *types.SyncInfo) error
}

// Bfter handles BFT consensus message processing
type Bfter struct {
	epoch uint64

	blockChainReader consensus.ChainReader
	broadcastCh      chan interface{}
	quit             chan struct{}
	consensus        ConsensusFns
	broadcast        BroadcastFns
	chainHeight      ChainHeightFn

	// Message deduplication caches
	knownVotes     *lru.Cache[common.Hash, struct{}]
	knownTimeouts  *lru.Cache[common.Hash, struct{}]
	knownSyncInfos *lru.Cache[common.Hash, struct{}]

	mu sync.RWMutex
}

// New creates a new BFT handler
func New(broadcasts BroadcastFns, blockChainReader *core.BlockChain, chainHeight ChainHeightFn) *Bfter {
	return &Bfter{
		broadcast:        broadcasts,
		blockChainReader: blockChainReader,
		chainHeight:      chainHeight,

		quit:        make(chan struct{}),
		broadcastCh: make(chan interface{}, 256),

		knownVotes:     lru.NewCache[common.Hash, struct{}](maxKnownVotes),
		knownTimeouts:  lru.NewCache[common.Hash, struct{}](maxKnownTimeouts),
		knownSyncInfos: lru.NewCache[common.Hash, struct{}](maxKnownSyncInfos),
	}
}

// InitEpochNumber initializes the epoch number from config
func (b *Bfter) InitEpochNumber() {
	cfg := b.blockChainReader.Config()
	if cfg.XDPoS != nil {
		b.epoch = cfg.XDPoS.Epoch
	} else {
		b.epoch = 900 // Default XDC epoch
	}
}

// SetConsensusFns sets the consensus verification and handler functions
func (b *Bfter) SetConsensusFns(fns ConsensusFns) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.consensus = fns
}

// SetBroadcastCh sets the broadcast channel from the consensus engine
func (b *Bfter) SetBroadcastCh(ch chan interface{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.broadcastCh = ch
}

// IsKnownVote checks if a vote is already known
func (b *Bfter) IsKnownVote(hash common.Hash) bool {
	return b.knownVotes.Contains(hash)
}

// MarkVote marks a vote as known
func (b *Bfter) MarkVote(hash common.Hash) {
	b.knownVotes.Add(hash, struct{}{})
}

// IsKnownTimeout checks if a timeout is already known
func (b *Bfter) IsKnownTimeout(hash common.Hash) bool {
	return b.knownTimeouts.Contains(hash)
}

// MarkTimeout marks a timeout as known
func (b *Bfter) MarkTimeout(hash common.Hash) {
	b.knownTimeouts.Add(hash, struct{}{})
}

// IsKnownSyncInfo checks if a sync info is already known
func (b *Bfter) IsKnownSyncInfo(hash common.Hash) bool {
	return b.knownSyncInfos.Contains(hash)
}

// MarkSyncInfo marks a sync info as known
func (b *Bfter) MarkSyncInfo(hash common.Hash) {
	b.knownSyncInfos.Add(hash, struct{}{})
}

// Vote processes an incoming vote message
func (b *Bfter) Vote(peer string, vote *types.Vote) error {
	hash := vote.Hash()
	
	// Check if already known
	if b.IsKnownVote(hash) {
		log.Trace("Discarded vote, known vote", "hash", hash.Hex())
		return nil
	}
	b.MarkVote(hash)

	log.Trace("Receive Vote", "hash", hash.Hex(),
		"voted block hash", vote.ProposedBlockInfo.Hash.Hex(),
		"number", vote.ProposedBlockInfo.Number,
		"round", vote.ProposedBlockInfo.Round)

	// Check distance from chain head
	voteBlockNum := vote.ProposedBlockInfo.Number.Int64()
	if dist := voteBlockNum - int64(b.chainHeight()); dist < -maxBlockDist || dist > maxBlockDist {
		log.Debug("Discarded propagated vote, too far away", "peer", peer,
			"number", voteBlockNum, "hash", vote.ProposedBlockInfo.Hash, "distance", dist)
		return nil
	}

	b.mu.RLock()
	verifyFn := b.consensus.VerifyVote
	handleFn := b.consensus.VoteHandler
	b.mu.RUnlock()

	// If no consensus functions set, just broadcast
	if verifyFn == nil {
		b.queueBroadcast(vote)
		return nil
	}

	verified, err := verifyFn(b.blockChainReader, vote)
	if err != nil {
		log.Debug("Verify BFT Vote failed", "error", err)
		return err
	}

	if verified {
		b.queueBroadcast(vote)
		if handleFn != nil {
			if err := handleFn(b.blockChainReader, vote); err != nil {
				log.Debug("Handle BFT Vote", "error", err)
				return err
			}
		}
	}

	return nil
}

// Timeout processes an incoming timeout message
func (b *Bfter) Timeout(peer string, timeout *types.Timeout) error {
	hash := timeout.Hash()
	
	// Check if already known
	if b.IsKnownTimeout(hash) {
		log.Trace("Discarded timeout, known timeout", "hash", hash.Hex())
		return nil
	}
	b.MarkTimeout(hash)

	log.Trace("Receive Timeout", "hash", hash.Hex(), "round", timeout.Round, "gapNumber", timeout.GapNumber)

	// Check distance from chain head (epoch * 3)
	gapNum := timeout.GapNumber
	if dist := int64(gapNum) - int64(b.chainHeight()); dist < -int64(b.epoch)*3 || dist > int64(b.epoch)*3 {
		log.Debug("Discarded propagated timeout, too far away", "peer", peer,
			"gapNumber", gapNum, "hash", hash, "distance", dist)
		return nil
	}

	b.mu.RLock()
	verifyFn := b.consensus.VerifyTimeout
	handleFn := b.consensus.TimeoutHandler
	b.mu.RUnlock()

	// If no consensus functions set, just broadcast
	if verifyFn == nil {
		b.queueBroadcast(timeout)
		return nil
	}

	verified, err := verifyFn(b.blockChainReader, timeout)
	if err != nil {
		log.Debug("Verify BFT Timeout failed", "error", err)
		return err
	}

	if verified {
		b.queueBroadcast(timeout)
		if handleFn != nil {
			if err := handleFn(b.blockChainReader, timeout); err != nil {
				log.Debug("Handle BFT Timeout", "error", err)
				return err
			}
		}
	}

	return nil
}

// SyncInfo processes an incoming sync info message
func (b *Bfter) SyncInfo(peer string, syncInfo *types.SyncInfo) error {
	hash := syncInfo.Hash()
	
	// Check if already known
	if b.IsKnownSyncInfo(hash) {
		log.Trace("Discarded syncInfo, known syncInfo", "hash", hash.Hex())
		return nil
	}
	b.MarkSyncInfo(hash)

	log.Debug("Receive SyncInfo", "hash", hash.Hex())

	// Check distance from chain head
	if syncInfo.HighestQuorumCert != nil && syncInfo.HighestQuorumCert.ProposedBlockInfo != nil {
		qcBlockNum := syncInfo.HighestQuorumCert.ProposedBlockInfo.Number.Int64()
		if dist := qcBlockNum - int64(b.chainHeight()); dist < -maxBlockDist || dist > maxBlockDist {
			log.Debug("Discarded propagated syncInfo, too far away", "peer", peer,
				"blockNum", qcBlockNum, "hash", hash, "distance", dist)
			return nil
		}
	}

	b.mu.RLock()
	verifyFn := b.consensus.VerifySyncInfo
	handleFn := b.consensus.SyncInfoHandler
	b.mu.RUnlock()

	// If no consensus functions set, just broadcast
	if verifyFn == nil {
		b.queueBroadcast(syncInfo)
		return nil
	}

	verified, err := verifyFn(b.blockChainReader, syncInfo)
	if err != nil {
		log.Debug("Verify BFT SyncInfo failed", "error", err)
		return err
	}

	if verified {
		b.queueBroadcast(syncInfo)
		if handleFn != nil {
			if err := handleFn(b.blockChainReader, syncInfo); err != nil {
				log.Debug("Handle BFT SyncInfo", "error", err)
				return err
			}
		}
	}

	return nil
}

// queueBroadcast queues a message for broadcast
func (b *Bfter) queueBroadcast(msg interface{}) {
	select {
	case b.broadcastCh <- msg:
	default:
		log.Warn("BFT broadcast channel full, dropping message")
	}
}

// Start starts the BFT message broadcast loop
func (b *Bfter) Start() {
	go b.loop()
}

// Stop stops the BFT handler
func (b *Bfter) Stop() {
	close(b.quit)
}

// loop is the main broadcast loop
func (b *Bfter) loop() {
	log.Info("BFT broadcast loop started")
	for {
		select {
		case <-b.quit:
			log.Warn("BFT broadcast loop stopped")
			return
		case obj := <-b.broadcastCh:
			switch v := obj.(type) {
			case *types.Vote:
				if b.broadcast.Vote != nil {
					go b.broadcast.Vote(v)
				}
			case *types.Timeout:
				if b.broadcast.Timeout != nil {
					go b.broadcast.Timeout(v)
				}
			case *types.SyncInfo:
				if b.broadcast.SyncInfo != nil {
					go b.broadcast.SyncInfo(v)
				}
			default:
				log.Error("Unknown BFT message type", "value", v)
			}
		}
	}
}

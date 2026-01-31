// Copyright 2018 XDPoSChain
// BFT peer extensions for XDPoS 2.0 consensus

package eth

import (
	mapset "github.com/deckarep/golang-set/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/p2p"
)

const (
	// maxKnownVotes is the maximum vote hashes to keep in the known list
	maxKnownVotes = 131072

	// maxKnownTimeouts is the maximum timeout hashes to keep in the known list
	maxKnownTimeouts = 131072

	// maxKnownSyncInfos is the maximum syncInfo hashes to keep in the known list
	maxKnownSyncInfos = 131072
)

// BFTPeer extends Peer with BFT message tracking
type BFTPeer struct {
	*Peer
	
	// Known BFT message hashes
	knownVotes     mapset.Set[common.Hash]
	knownTimeouts  mapset.Set[common.Hash]
	knownSyncInfos mapset.Set[common.Hash]
}

// NewBFTPeer creates a BFT-aware peer wrapper
func NewBFTPeer(p *Peer) *BFTPeer {
	return &BFTPeer{
		Peer:           p,
		knownVotes:     mapset.NewSet[common.Hash](),
		knownTimeouts:  mapset.NewSet[common.Hash](),
		knownSyncInfos: mapset.NewSet[common.Hash](),
	}
}

// MarkVote marks a vote as known for the peer
func (p *BFTPeer) MarkVote(hash common.Hash) {
	for p.knownVotes.Cardinality() >= maxKnownVotes {
		p.knownVotes.Pop()
	}
	p.knownVotes.Add(hash)
}

// KnownVote returns whether the peer is known to have a vote
func (p *BFTPeer) KnownVote(hash common.Hash) bool {
	return p.knownVotes.Contains(hash)
}

// MarkTimeout marks a timeout as known for the peer
func (p *BFTPeer) MarkTimeout(hash common.Hash) {
	for p.knownTimeouts.Cardinality() >= maxKnownTimeouts {
		p.knownTimeouts.Pop()
	}
	p.knownTimeouts.Add(hash)
}

// KnownTimeout returns whether the peer is known to have a timeout
func (p *BFTPeer) KnownTimeout(hash common.Hash) bool {
	return p.knownTimeouts.Contains(hash)
}

// MarkSyncInfo marks a syncInfo as known for the peer
func (p *BFTPeer) MarkSyncInfo(hash common.Hash) {
	for p.knownSyncInfos.Cardinality() >= maxKnownSyncInfos {
		p.knownSyncInfos.Pop()
	}
	p.knownSyncInfos.Add(hash)
}

// KnownSyncInfo returns whether the peer is known to have a syncInfo
func (p *BFTPeer) KnownSyncInfo(hash common.Hash) bool {
	return p.knownSyncInfos.Contains(hash)
}

// SendVote sends a vote to the peer
func (p *BFTPeer) SendVote(vote *types.Vote) error {
	hash := vote.Hash()
	p.MarkVote(hash)
	return p2p.Send(p.rw, VoteMsg, vote)
}

// SendTimeout sends a timeout to the peer
func (p *BFTPeer) SendTimeout(timeout *types.Timeout) error {
	hash := timeout.Hash()
	p.MarkTimeout(hash)
	return p2p.Send(p.rw, TimeoutMsg, timeout)
}

// SendSyncInfo sends a syncInfo to the peer
func (p *BFTPeer) SendSyncInfo(syncInfo *types.SyncInfo) error {
	hash := syncInfo.Hash()
	p.MarkSyncInfo(hash)
	return p2p.Send(p.rw, SyncInfoMsg, syncInfo)
}

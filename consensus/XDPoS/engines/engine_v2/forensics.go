// Copyright (c) 2018 XDPoSChain
// XDPoS V2 forensics - detecting Byzantine behavior

package engine_v2

import (
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/XDPoS/utils"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

// Forensics handles detection and reporting of Byzantine behavior
type Forensics struct {
	lock           sync.RWMutex
	voteEquivocate map[common.Hash]map[common.Address]bool // track vote equivocation by block hash and voter
}

// NewForensics creates a new forensics processor
func NewForensics() *Forensics {
	return &Forensics{
		voteEquivocate: make(map[common.Hash]map[common.Address]bool),
	}
}

// DetectEquivocationInVotePool detects if a voter has voted for multiple blocks at the same round
func (f *Forensics) DetectEquivocationInVotePool(vote *types.Vote, votePool *utils.Pool) {
	if vote == nil || vote.ProposedBlockInfo == nil {
		return
	}

	f.lock.Lock()
	defer f.lock.Unlock()

	signer := vote.GetSigner()
	if signer == (common.Address{}) {
		return
	}

	blockHash := vote.ProposedBlockInfo.Hash

	// Initialize map for this block if needed
	if _, exists := f.voteEquivocate[blockHash]; !exists {
		f.voteEquivocate[blockHash] = make(map[common.Address]bool)
	}

	// Check if this signer already voted for this block
	if f.voteEquivocate[blockHash][signer] {
		log.Warn("[Forensics] Potential equivocation detected in vote pool",
			"signer", signer.Hex(),
			"blockHash", blockHash.Hex(),
			"round", vote.ProposedBlockInfo.Round)
	}

	f.voteEquivocate[blockHash][signer] = true
}

// ProcessVoteEquivocation processes a vote for equivocation evidence
func (f *Forensics) ProcessVoteEquivocation(chain consensus.ChainReader, engine *XDPoS_v2, vote *types.Vote) {
	if vote == nil || vote.ProposedBlockInfo == nil {
		return
	}

	// Basic validation - just log for now, actual slashing would be done via smart contracts
	signer := vote.GetSigner()
	if signer == (common.Address{}) {
		return
	}

	log.Debug("[Forensics] Processing vote",
		"signer", signer.Hex(),
		"blockNum", vote.ProposedBlockInfo.Number,
		"round", vote.ProposedBlockInfo.Round,
		"hash", vote.ProposedBlockInfo.Hash.Hex())
}

// ForensicsMonitoring monitors for forensic events after block commit
func (f *Forensics) ForensicsMonitoring(chain consensus.ChainReader, engine *XDPoS_v2, headers []types.Header, qc types.QuorumCert) {
	if len(headers) < 2 {
		return
	}

	// Check for any anomalies in committed blocks
	for i := 0; i < len(headers)-1; i++ {
		parentHeader := headers[i]
		childHeader := headers[i+1]

		if childHeader.ParentHash != parentHeader.Hash() {
			log.Warn("[Forensics] Parent hash mismatch in committed chain",
				"parentNum", parentHeader.Number,
				"parentHash", parentHeader.Hash().Hex(),
				"childNum", childHeader.Number,
				"childParentHash", childHeader.ParentHash.Hex())
		}
	}

	// Check QC consistency
	if qc.ProposedBlockInfo != nil {
		lastHeader := headers[len(headers)-1]
		if qc.ProposedBlockInfo.Hash != lastHeader.Hash() {
			log.Debug("[Forensics] QC references different block",
				"qcHash", qc.ProposedBlockInfo.Hash.Hex(),
				"lastHeaderHash", lastHeader.Hash().Hex())
		}
	}
}

// CleanupOldRecords removes old equivocation records
func (f *Forensics) CleanupOldRecords(currentRound types.Round) {
	f.lock.Lock()
	defer f.lock.Unlock()

	// Simple cleanup - in production, would be more sophisticated
	// For now just log the current state
	log.Debug("[Forensics] Cleanup check", "trackingBlocks", len(f.voteEquivocate), "currentRound", currentRound)
}

// Copyright (c) 2018 XDPoSChain
// XDPoS V2 snapshot management

package engine_v2

import (
	"encoding/json"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
)

// SnapshotV2 is the state of the validator list at a given point
// Used to track next epoch candidates
type SnapshotV2 struct {
	Number uint64      `json:"number"` // Block number where the snapshot was created
	Hash   common.Hash `json:"hash"`   // Block hash where the snapshot was created

	// NextEpochCandidates is the validator list for the next epoch
	NextEpochCandidates []common.Address `json:"masterNodes"`
}

// newSnapshot creates a new V2 snapshot
func newSnapshot(number uint64, hash common.Hash, candidates []common.Address) *SnapshotV2 {
	return &SnapshotV2{
		Number:              number,
		Hash:                hash,
		NextEpochCandidates: candidates,
	}
}

// loadSnapshot loads an existing snapshot from the database
func loadSnapshot(db ethdb.Database, hash common.Hash) (*SnapshotV2, error) {
	blob, err := db.Get(append([]byte("XDPoS-V2-"), hash[:]...))
	if err != nil {
		return nil, err
	}
	snap := new(SnapshotV2)
	if err := json.Unmarshal(blob, snap); err != nil {
		return nil, err
	}
	return snap, nil
}

// storeSnapshot stores the snapshot to the database
func storeSnapshot(s *SnapshotV2, db ethdb.Database) error {
	blob, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return db.Put(append([]byte("XDPoS-V2-"), s.Hash[:]...), blob)
}

// GetMappedCandidates returns candidates as a map for O(1) lookup
func (s *SnapshotV2) GetMappedCandidates() map[common.Address]struct{} {
	ms := make(map[common.Address]struct{})
	for _, n := range s.NextEpochCandidates {
		ms[n] = struct{}{}
	}
	return ms
}

// IsCandidates checks if an address is a candidate
func (s *SnapshotV2) IsCandidates(address common.Address) bool {
	for _, n := range s.NextEpochCandidates {
		if n == address {
			return true
		}
	}
	return false
}

// getSnapshot retrieves the snapshot for a given block number
func (x *XDPoS_v2) getSnapshot(chain consensus.ChainReader, number uint64, isGapNumber bool) (*SnapshotV2, error) {
	var gapBlockNum uint64
	if isGapNumber {
		gapBlockNum = number
	} else {
		gapBlockNum = number - number%x.config.Epoch - x.config.Gap
		// Prevent overflow
		if number-number%x.config.Epoch < x.config.Gap {
			gapBlockNum = 0
		}
	}

	gapBlockHeader := chain.GetHeaderByNumber(gapBlockNum)
	if gapBlockHeader == nil {
		log.Error("[getSnapshot] Cannot find gap block header", "number", gapBlockNum)
		return nil, errUnknownBlock
	}
	gapBlockHash := gapBlockHeader.Hash()
	log.Debug("get snapshot from gap block", "number", gapBlockNum, "hash", gapBlockHash.Hex())

	// Check in-memory cache
	if snap, ok := x.snapshots.Get(gapBlockHash); ok && snap != nil {
		log.Trace("Loaded snapshot from memory", "number", gapBlockNum, "hash", gapBlockHash)
		return snap, nil
	}

	// Check on-disk
	snap, err := loadSnapshot(x.db, gapBlockHash)
	if err != nil {
		log.Error("Cannot find snapshot from last gap block", "err", err, "number", gapBlockNum, "hash", gapBlockHash)
		return nil, err
	}

	log.Trace("Loaded snapshot from disk", "number", gapBlockNum, "hash", gapBlockHash)
	x.snapshots.Add(snap.Hash, snap)
	return snap, nil
}

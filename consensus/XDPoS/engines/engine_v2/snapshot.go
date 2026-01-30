// Copyright (c) 2024 XDC Network
// Snapshot management for XDPoS 2.0

package engine_v2

import (
	"encoding/json"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
)

// SnapshotV2 represents the state of masternodes at a given block
type SnapshotV2 struct {
	Number              uint64           `json:"number"`
	Hash                common.Hash      `json:"hash"`
	NextEpochCandidates []common.Address `json:"nextEpochCandidates"`
}

// newSnapshot creates a new snapshot
func newSnapshot(number uint64, hash common.Hash, masternodes []common.Address) *SnapshotV2 {
	snap := &SnapshotV2{
		Number:              number,
		Hash:                hash,
		NextEpochCandidates: make([]common.Address, len(masternodes)),
	}
	copy(snap.NextEpochCandidates, masternodes)
	return snap
}

// loadSnapshot loads a snapshot from the database
func loadSnapshot(db ethdb.Database, hash common.Hash) (*SnapshotV2, error) {
	key := []byte("xdpos-v2-snapshot-" + hash.Hex())
	blob, err := db.Get(key)
	if err != nil {
		return nil, err
	}
	snap := new(SnapshotV2)
	if err := json.Unmarshal(blob, snap); err != nil {
		return nil, err
	}
	return snap, nil
}

// storeSnapshot stores a snapshot to the database
func storeSnapshot(snap *SnapshotV2, db ethdb.Database) error {
	blob, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	key := []byte("xdpos-v2-snapshot-" + snap.Hash.Hex())
	if err := db.Put(key, blob); err != nil {
		log.Error("Failed to store snapshot", "hash", snap.Hash, "error", err)
		return err
	}
	return nil
}

// Copy creates a copy of the snapshot
func (s *SnapshotV2) Copy() *SnapshotV2 {
	return newSnapshot(s.Number, s.Hash, s.NextEpochCandidates)
}

// GetSigners returns the list of masternodes
func (s *SnapshotV2) GetSigners() []common.Address {
	return s.NextEpochCandidates
}

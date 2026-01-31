// Copyright (c) 2018 XDPoSChain
// XDPoS 2.0 consensus types

package types

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
)

// Round number type in XDPoS 2.0
type Round uint64

// Signature type for BFT messages
type Signature []byte

// BlockInfo contains block metadata for BFT messages
type BlockInfo struct {
	Hash   common.Hash `json:"hash"`
	Round  Round       `json:"round"`
	Number *big.Int    `json:"number"`
}

// Vote message in XDPoS 2.0
type Vote struct {
	signer            common.Address // unexported, set via SetSigner
	ProposedBlockInfo *BlockInfo     `json:"proposedBlockInfo"`
	Signature         Signature      `json:"signature"`
	GapNumber         uint64         `json:"gapNumber"`
}

// Hash returns the hash of the vote
func (v *Vote) Hash() common.Hash {
	return rlpHash(v)
}

// PoolKey returns the key used to group votes in the pool
func (v *Vote) PoolKey() string {
	return fmt.Sprint(v.ProposedBlockInfo.Round, ":", v.GapNumber, ":", v.ProposedBlockInfo.Number, ":", v.ProposedBlockInfo.Hash.Hex())
}

// GetSigner returns the signer address
func (v *Vote) GetSigner() common.Address {
	return v.signer
}

// SetSigner sets the signer address
func (v *Vote) SetSigner(signer common.Address) {
	v.signer = signer
}

// Timeout message in XDPoS 2.0
type Timeout struct {
	signer    common.Address
	Round     Round
	Signature Signature
	GapNumber uint64
}

// Hash returns the hash of the timeout
func (t *Timeout) Hash() common.Hash {
	return rlpHash(t)
}

// PoolKey returns the key used to group timeouts in the pool
func (t *Timeout) PoolKey() string {
	return fmt.Sprint(t.Round, ":", t.GapNumber)
}

// GetSigner returns the signer address
func (t *Timeout) GetSigner() common.Address {
	return t.signer
}

// SetSigner sets the signer address
func (t *Timeout) SetSigner(signer common.Address) {
	t.signer = signer
}

// SyncInfo is the BFT sync message containing highest QC and TC
type SyncInfo struct {
	HighestQuorumCert  *QuorumCert
	HighestTimeoutCert *TimeoutCert
}

// Hash returns the hash of the sync info
func (s *SyncInfo) Hash() common.Hash {
	return rlpHash(s)
}

// QuorumCert (QC) represents 2/3 consensus votes
type QuorumCert struct {
	ProposedBlockInfo *BlockInfo  `json:"proposedBlockInfo"`
	Signatures        []Signature `json:"signatures"`
	GapNumber         uint64      `json:"gapNumber"`
}

// TimeoutCert (TC) represents 2/3 timeout votes
type TimeoutCert struct {
	Round      Round
	Signatures []Signature
	GapNumber  uint64
}

// ExtraFields_v2 contains the parsed extra fields in block header for XDPoS 2.0
// The version byte is the first byte in header's extra (value >= 2)
type ExtraFields_v2 struct {
	Round      Round
	QuorumCert *QuorumCert
}

// EncodeToBytes encodes the extra fields to bytes with version prefix
func (e *ExtraFields_v2) EncodeToBytes() ([]byte, error) {
	bytes, err := rlp.EncodeToBytes(e)
	if err != nil {
		return nil, err
	}
	versionByte := []byte{2}
	return append(versionByte, bytes...), nil
}

// EpochSwitchInfo contains information about epoch boundaries
type EpochSwitchInfo struct {
	Penalties                  []common.Address
	Standbynodes               []common.Address
	Masternodes                []common.Address
	MasternodesLen             int
	EpochSwitchBlockInfo       *BlockInfo
	EpochSwitchParentBlockInfo *BlockInfo
}

// VoteForSign is the structure used to generate vote signatures
type VoteForSign struct {
	ProposedBlockInfo *BlockInfo
	GapNumber         uint64
}

// VoteSigHash returns the hash used for vote signing
func VoteSigHash(m *VoteForSign) common.Hash {
	return rlpHash(m)
}

// TimeoutForSign is the structure used to generate timeout signatures
type TimeoutForSign struct {
	Round     Round
	GapNumber uint64
}

// TimeoutSigHash returns the hash used for timeout signing
func TimeoutSigHash(m *TimeoutForSign) common.Hash {
	return rlpHash(m)
}

// Implement eth.Packet interface for BFT messages

// Name returns the packet name for Vote
func (*Vote) Name() string { return "Vote" }

// Kind returns the message type for Vote
func (*Vote) Kind() byte { return 0xe0 } // VoteMsg

// Name returns the packet name for Timeout
func (*Timeout) Name() string { return "Timeout" }

// Kind returns the message type for Timeout
func (*Timeout) Kind() byte { return 0xe1 } // TimeoutMsg

// Name returns the packet name for SyncInfo
func (*SyncInfo) Name() string { return "SyncInfo" }

// Kind returns the message type for SyncInfo
func (*SyncInfo) Kind() byte { return 0xe2 } // SyncInfoMsg

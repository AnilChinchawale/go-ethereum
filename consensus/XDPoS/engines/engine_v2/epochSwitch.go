// Copyright (c) 2018 XDPoSChain
// XDPoS V2 epoch switch handling

package engine_v2

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

// getSwitchEpoch computes the switch epoch from switch block
func (x *XDPoS_v2) getSwitchEpoch() uint64 {
	if x.config.V2 == nil || x.config.V2.SwitchBlock == nil {
		return 0
	}
	return x.config.V2.SwitchBlock.Uint64() / x.config.Epoch
}

// getPreviousEpochSwitchInfoByHash gets epoch switch info from previous epochs
func (x *XDPoS_v2) getPreviousEpochSwitchInfoByHash(chain consensus.ChainReader, hash common.Hash, limit int) (*types.EpochSwitchInfo, error) {
	epochSwitchInfo, err := x.getEpochSwitchInfo(chain, nil, hash)
	if err != nil {
		log.Error("[getPreviousEpochSwitchInfoByHash] getEpochSwitchInfo error", "err", err)
		return nil, err
	}
	for i := 0; i < limit; i++ {
		epochSwitchInfo, err = x.getEpochSwitchInfo(chain, nil, epochSwitchInfo.EpochSwitchParentBlockInfo.Hash)
		if err != nil {
			log.Error("[getPreviousEpochSwitchInfoByHash] getEpochSwitchInfo error", "err", err)
			return nil, err
		}
	}
	return epochSwitchInfo, nil
}

// getEpochSwitchInfo gets epoch switch info for a given header/hash
func (x *XDPoS_v2) getEpochSwitchInfo(chain consensus.ChainReader, header *types.Header, hash common.Hash) (*types.EpochSwitchInfo, error) {
	epochSwitchInfo, ok := x.epochSwitches.Get(hash)
	if ok && epochSwitchInfo != nil {
		log.Debug("[getEpochSwitchInfo] cache hit", "number", epochSwitchInfo.EpochSwitchBlockInfo.Number, "hash", hash.Hex())
		return epochSwitchInfo, nil
	}

	h := header
	if h == nil {
		log.Debug("[getEpochSwitchInfo] header doesn't provide, get header by hash", "hash", hash.Hex())
		h = chain.GetHeaderByHash(hash)
		if h == nil {
			return nil, fmt.Errorf("[getEpochSwitchInfo] can not find header from db hash %v", hash.Hex())
		}
	}

	isEpochSwitch, _, err := x.IsEpochSwitch(h)
	if err != nil {
		return nil, err
	}

	if isEpochSwitch {
		log.Debug("[getEpochSwitchInfo] header is epoch switch", "hash", hash.Hex(), "number", h.Number.Uint64())

		if h.Number.Uint64() == 0 {
			log.Warn("[getEpochSwitchInfo] block 0, init epoch differently")
			// Handle genesis block differently
			masternodes := common.ExtractAddressFromBytes(h.Extra[32 : len(h.Extra)-65])
			penalties := []common.Address{}
			standbynodes := []common.Address{}
			epochSwitchInfo := &types.EpochSwitchInfo{
				Penalties:      penalties,
				Standbynodes:   standbynodes,
				Masternodes:    masternodes,
				MasternodesLen: len(masternodes),
				EpochSwitchBlockInfo: &types.BlockInfo{
					Hash:   hash,
					Number: h.Number,
					Round:  0,
				},
			}
			x.epochSwitches.Add(hash, epochSwitchInfo)
			return epochSwitchInfo, nil
		}

		quorumCert, round, masternodes, err := x.getExtraFields(h)
		if err != nil {
			log.Error("[getEpochSwitchInfo] get extra field", "err", err, "number", h.Number.Uint64())
			return nil, err
		}

		snap, err := x.getSnapshot(chain, h.Number.Uint64(), false)
		if err != nil {
			log.Error("[getEpochSwitchInfo] getSnapshot error", "err", err)
			return nil, err
		}

		penalties := common.ExtractAddressFromBytes(h.Penalties)
		candidates := snap.NextEpochCandidates
		standbynodes := []common.Address{}
		if len(masternodes) != len(candidates) {
			standbynodes = candidates
			standbynodes = common.RemoveItemFromArray(standbynodes, masternodes)
			standbynodes = common.RemoveItemFromArray(standbynodes, penalties)
		}

		epochSwitchInfo := &types.EpochSwitchInfo{
			Penalties:      penalties,
			Standbynodes:   standbynodes,
			Masternodes:    masternodes,
			MasternodesLen: len(masternodes),
			EpochSwitchBlockInfo: &types.BlockInfo{
				Hash:   hash,
				Number: h.Number,
				Round:  round,
			},
		}
		if quorumCert != nil {
			epochSwitchInfo.EpochSwitchParentBlockInfo = quorumCert.ProposedBlockInfo
		}

		x.epochSwitches.Add(hash, epochSwitchInfo)
		return epochSwitchInfo, nil
	}

	// Not epoch switch, recurse to parent
	epochSwitchInfo, err = x.getEpochSwitchInfo(chain, nil, h.ParentHash)
	if err != nil {
		log.Error("[getEpochSwitchInfo] recursive error", "err", err, "hash", hash.Hex(), "number", h.Number.Uint64())
		return nil, err
	}
	log.Debug("[getEpochSwitchInfo] get epoch switch info recursively", "hash", hash.Hex(), "number", h.Number.Uint64())
	x.epochSwitches.Add(hash, epochSwitchInfo)
	return epochSwitchInfo, nil
}

// isEpochSwitchAtRound checks if a block at a given round would be an epoch switch
func (x *XDPoS_v2) isEpochSwitchAtRound(round types.Round, parentHeader *types.Header) (bool, uint64, error) {
	switchEpoch := x.getSwitchEpoch()
	epochNum := switchEpoch + uint64(round)/x.config.Epoch

	// If parent is last v1 block and this is first v2 block, this is epoch switch
	if parentHeader.Number.Cmp(x.config.V2.SwitchBlock) == 0 {
		return true, epochNum, nil
	}

	_, parentRound, _, err := x.getExtraFields(parentHeader)
	if err != nil {
		log.Error("[isEpochSwitchAtRound] decode header error", "err", err, "header", parentHeader, "extra", common.Bytes2Hex(parentHeader.Extra))
		return false, 0, err
	}

	if round <= parentRound {
		// This round is no larger than parentRound
		return false, epochNum, nil
	}

	epochStartRound := round - round%types.Round(x.config.Epoch)
	return parentRound < epochStartRound, epochNum, nil
}

// GetCurrentEpochSwitchBlock gets the current epoch switch block number
func (x *XDPoS_v2) GetCurrentEpochSwitchBlock(chain consensus.ChainReader, blockNum *big.Int) (uint64, uint64, error) {
	header := chain.GetHeaderByNumber(blockNum.Uint64())
	epochSwitchInfo, err := x.getEpochSwitchInfo(chain, header, header.Hash())
	if err != nil {
		log.Error("[GetCurrentEpochSwitchBlock] Fail to get epoch switch info", "Num", header.Number, "Hash", header.Hash())
		return 0, 0, err
	}

	currentCheckpointNumber := epochSwitchInfo.EpochSwitchBlockInfo.Number.Uint64()
	switchEpoch := x.getSwitchEpoch()
	epochNum := switchEpoch + uint64(epochSwitchInfo.EpochSwitchBlockInfo.Round)/x.config.Epoch
	return currentCheckpointNumber, epochNum, nil
}

// IsEpochSwitch checks if a header is an epoch switch block
func (x *XDPoS_v2) IsEpochSwitch(header *types.Header) (bool, uint64, error) {
	// Return true directly if we are examining the last v1 block
	switchBlock := x.config.V2.SwitchBlock
	if header.Number.Cmp(switchBlock) == 0 {
		log.Info("[IsEpochSwitch] examining last v1 block")
		return true, header.Number.Uint64() / x.config.Epoch, nil
	}

	quorumCert, round, _, err := x.getExtraFields(header)
	if err != nil {
		log.Error("[IsEpochSwitch] decode header error", "err", err, "header", header, "extra", common.Bytes2Hex(header.Extra))
		return false, 0, err
	}

	parentRound := quorumCert.ProposedBlockInfo.Round
	epochStartRound := round - round%types.Round(x.config.Epoch)
	switchEpoch := x.getSwitchEpoch()
	epochNum := switchEpoch + uint64(round)/x.config.Epoch

	// If parent is last v1 block and this is first v2 block, this is epoch switch
	if quorumCert.ProposedBlockInfo.Number.Cmp(switchBlock) == 0 {
		log.Info("[IsEpochSwitch] true, parent equals V2.SwitchBlock", "round", round, "number", header.Number.Uint64(), "hash", header.Hash())
		return true, epochNum, nil
	}

	log.Debug("[IsEpochSwitch]", "is", parentRound < epochStartRound, "parentRound", parentRound, "round", round, "number", header.Number.Uint64(), "epochNum", epochNum, "hash", header.Hash().Hex())

	// If isEpochSwitch, add to cache
	if parentRound < epochStartRound {
		x.round2epochBlockInfo.Add(round, &types.BlockInfo{
			Hash:   header.Hash(),
			Number: header.Number,
			Round:  round,
		})
	}
	return parentRound < epochStartRound, epochNum, nil
}

// GetEpochSwitchInfoBetween gets epoch switch info between begin and end headers
func (x *XDPoS_v2) GetEpochSwitchInfoBetween(chain consensus.ChainReader, begin, end *types.Header) ([]*types.EpochSwitchInfo, error) {
	infos := make([]*types.EpochSwitchInfo, 0)
	// After first iteration, it becomes nil since epoch switch info does not have header info
	iteratorHeader := end
	// After first iteration, it becomes the parent hash of the epoch switch block
	iteratorHash := end.Hash()
	iteratorNum := end.Number

	// When iterator is strictly > begin number, do the search
	for iteratorNum.Cmp(begin.Number) > 0 {
		epochSwitchInfo, err := x.getEpochSwitchInfo(chain, iteratorHeader, iteratorHash)
		if err != nil {
			log.Error("[GetEpochSwitchInfoBetween] getEpochSwitchInfo error", "err", err)
			return nil, err
		}
		iteratorHeader = nil
		// V2 switch epoch switch info has nil parent
		if epochSwitchInfo.EpochSwitchParentBlockInfo == nil {
			break
		}
		iteratorHash = epochSwitchInfo.EpochSwitchParentBlockInfo.Hash
		iteratorNum = epochSwitchInfo.EpochSwitchBlockInfo.Number
		if iteratorNum.Cmp(begin.Number) >= 0 {
			infos = append(infos, epochSwitchInfo)
		}
	}

	// Reverse the array
	for i := 0; i < len(infos)/2; i++ {
		infos[i], infos[len(infos)-1-i] = infos[len(infos)-1-i], infos[i]
	}
	return infos, nil
}

// GetBlockByEpochNumber gets block info by epoch number
func (x *XDPoS_v2) GetBlockByEpochNumber(chain consensus.ChainReader, epochNum uint64) (*types.BlockInfo, error) {
	switchEpoch := x.getSwitchEpoch()

	// Check cache first
	startRound := types.Round((epochNum - switchEpoch) * x.config.Epoch)
	for r := startRound; r < startRound+types.Round(x.config.Epoch); r++ {
		if blockInfo, ok := x.round2epochBlockInfo.Get(r); ok {
			return blockInfo, nil
		}
	}

	// Binary search
	currentHeader := chain.CurrentHeader()
	maxBlockNum := currentHeader.Number.Uint64()
	minBlockNum := x.config.V2.SwitchBlock.Uint64()

	// Estimate starting point
	estimatedBlockNum := minBlockNum + (epochNum-switchEpoch)*x.config.Epoch
	if estimatedBlockNum > maxBlockNum {
		estimatedBlockNum = maxBlockNum
	}

	blockInfo, _, err := x.binarySearchBlockByEpochNumber(chain, epochNum, minBlockNum, estimatedBlockNum)
	return blockInfo, err
}

// binarySearchBlockByEpochNumber binary searches for a block by epoch number
func (x *XDPoS_v2) binarySearchBlockByEpochNumber(chain consensus.ChainReader, epochNum uint64, minBlockNum, maxBlockNum uint64) (*types.BlockInfo, *types.Header, error) {
	for minBlockNum <= maxBlockNum {
		midBlockNum := (minBlockNum + maxBlockNum) / 2
		header := chain.GetHeaderByNumber(midBlockNum)
		if header == nil {
			return nil, nil, fmt.Errorf("header not found at block %d", midBlockNum)
		}

		isEpochSwitch, headerEpochNum, err := x.IsEpochSwitch(header)
		if err != nil {
			return nil, nil, err
		}

		if isEpochSwitch && headerEpochNum == epochNum {
			_, round, _, err := x.getExtraFields(header)
			if err != nil {
				return nil, nil, err
			}
			return &types.BlockInfo{
				Hash:   header.Hash(),
				Number: header.Number,
				Round:  round,
			}, header, nil
		}

		if headerEpochNum < epochNum {
			minBlockNum = midBlockNum + 1
		} else {
			maxBlockNum = midBlockNum - 1
		}
	}

	return nil, nil, fmt.Errorf("epoch switch block not found for epoch %d", epochNum)
}

// GetMasternodesByHash returns masternodes for the epoch containing the given hash
func (x *XDPoS_v2) GetMasternodesByHash(chain consensus.ChainReader, hash common.Hash) []common.Address {
	epochSwitchInfo, err := x.getEpochSwitchInfo(chain, nil, hash)
	if err != nil {
		log.Error("[GetMasternodesByHash] getEpochSwitchInfo error", "err", err)
		return []common.Address{}
	}
	return epochSwitchInfo.Masternodes
}

// GetPreviousPenaltyByHash returns penalties from previous epochs
func (x *XDPoS_v2) GetPreviousPenaltyByHash(chain consensus.ChainReader, hash common.Hash, limit int) []common.Address {
	currentEpochSwitchInfo, err := x.getEpochSwitchInfo(chain, nil, hash)
	if err != nil {
		log.Error("[GetPreviousPenaltyByHash] getEpochSwitchInfo error", "err", err)
		return []common.Address{}
	}
	if limit == 0 {
		return currentEpochSwitchInfo.Penalties
	}

	switchEpoch := x.getSwitchEpoch()
	epochNum := switchEpoch + uint64(currentEpochSwitchInfo.EpochSwitchBlockInfo.Round)/x.config.Epoch
	if epochNum < uint64(limit) {
		log.Error("[GetPreviousPenaltyByHash] too large limit", "limit", limit)
		return []common.Address{}
	}

	_, header, err := x.binarySearchBlockByEpochNumber(chain, epochNum-uint64(limit), currentEpochSwitchInfo.EpochSwitchBlockInfo.Number.Uint64()-x.config.Epoch*uint64(limit), currentEpochSwitchInfo.EpochSwitchParentBlockInfo.Number.Uint64())
	if err != nil {
		log.Error("[GetPreviousPenaltyByHash] binarySearchBlockByEpochNumber error", "err", err)
		return []common.Address{}
	}
	return common.ExtractAddressFromBytes(header.Penalties)
}

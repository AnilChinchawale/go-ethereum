// Copyright (c) 2018 XDPoSChain
// Ported to go-ethereum for XDC compatibility

package hooks

import (
	"errors"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/XDPoS"
	"github.com/ethereum/go-ethereum/contracts"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/util"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// AttachConsensusV2Hooks attaches V2 consensus hooks to XDPoS engine
func AttachConsensusV2Hooks(adaptor *XDPoS.XDPoS, bc *core.BlockChain, chainConfig *params.ChainConfig) {
	// Hook calculates reward for masternodes at epoch boundaries
	adaptor.HookReward = func(chain consensus.ChainHeaderReader, stateBlock *state.StateDB, header *types.Header) (map[string]interface{}, error) {
		number := header.Number.Uint64()
		foundationWalletAddr := chainConfig.XDPoS.FoudationWalletAddr
		if foundationWalletAddr == (common.Address{}) {
			log.Error("Foundation Wallet Address is empty", "error", foundationWalletAddr)
			return nil, errors.New("foundation wallet address is empty")
		}
		rewards := make(map[string]interface{})

		// Skip reward for first checkpoint (like v2.6.8)
		rCheckpoint := chainConfig.XDPoS.Epoch
		if number <= rCheckpoint || number-rCheckpoint == 0 {
			log.Debug("Skipping first epoch reward", "number", number)
			return rewards, nil
		}

		// Skip hook reward if this is the first v2 block
		if chainConfig.XDPoS.V2 != nil && chainConfig.XDPoS.V2.SwitchBlock != nil {
			if number == chainConfig.XDPoS.V2.SwitchBlock.Uint64()+1 {
				return rewards, nil
			}
		}

		start := time.Now()

		// Get reward inflation - use a wrapper that works with ChainHeaderReader
		chainReward := new(big.Int).Mul(new(big.Int).SetUint64(chainConfig.XDPoS.Reward), new(big.Int).SetUint64(params.Ether))
		chainReward = util.RewardInflation(nil, chainReward, number, common.BlocksPerYear)

		// Get signers/signing tx count
		totalSigner := new(uint64)
		signers, err := GetSigningTxCount(adaptor, bc, header, chainConfig, totalSigner)

		log.Debug("Time Get Signers", "block", header.Number.Uint64(), "time", common.PrettyDuration(time.Since(start)))
		if err != nil {
			log.Error("[HookReward] Fail to get signers count for reward checkpoint", "error", err)
			return nil, err
		}
		rewards["signers"] = signers

		rewardSigners, err := contracts.CalculateRewardForSigner(chainReward, signers, *totalSigner)
		if err != nil {
			log.Error("[HookReward] Fail to calculate reward for signers", "error", err)
			return nil, err
		}

		// Add reward for coin holders
		voterResults := make(map[common.Address]interface{})
		if len(signers) > 0 {
			for signer, calcReward := range rewardSigners {
				// For V1, we use the current state for holder rewards
				holderRewards, err := contracts.CalculateRewardForHolders(foundationWalletAddr, stateBlock, signer, calcReward, number)
				if err != nil {
					log.Error("[HookReward] Fail to calculate reward for holders.", "error", err)
					return nil, err
				}
				if len(holderRewards) > 0 {
					for holder, reward := range holderRewards {
						// Convert big.Int to uint256.Int for AddBalance
						rewardU256, _ := uint256.FromBig(reward)
						stateBlock.AddBalance(holder, rewardU256, tracing.BalanceIncreaseRewardMineBlock)
					}
				}
				voterResults[signer] = holderRewards
			}
		}
		rewards["rewards"] = voterResults
		log.Debug("Time Calculated HookReward", "block", header.Number.Uint64(), "time", common.PrettyDuration(time.Since(start)))
		return rewards, nil
	}
}

// GetSigningTxCount gets signing transaction sender count for reward calculation
func GetSigningTxCount(c *XDPoS.XDPoS, chain *core.BlockChain, header *types.Header, chainConfig *params.ChainConfig, totalSigner *uint64) (map[common.Address]*contracts.RewardLog, error) {
	number := header.Number.Uint64()
	rCheckpoint := chainConfig.XDPoS.Epoch
	signers := make(map[common.Address]*contracts.RewardLog)

	blockSignersAddr := common.BlockSignersBinary

	if number == 0 {
		return signers, nil
	}

	// Calculate ranges like v2.6.8 - simple math, not epoch detection
	prevCheckpoint := number - (rCheckpoint * 2)
	startBlockNumber := prevCheckpoint + 1
	if startBlockNumber < 1 {
		startBlockNumber = 1
	}
	endBlockNumber := startBlockNumber + rCheckpoint - 1

	data := make(map[common.Hash][]common.Address)
	mapBlkHash := map[uint64]common.Hash{}

	// Scan blocks from number-1 down to startBlockNumber
	currentHeader := header
	for i := number - 1; i >= startBlockNumber && i > 0; i-- {
		parentHash := currentHeader.ParentHash
		currentHeader = chain.GetHeader(parentHash, i)
		if currentHeader == nil {
			break
		}
		mapBlkHash[i] = currentHeader.Hash()

		block := chain.GetBlock(currentHeader.Hash(), i)
		if block != nil {
			for _, tx := range block.Transactions() {
				if tx.To() != nil && *tx.To() == blockSignersAddr {
					txData := tx.Data()
					if len(txData) >= 32 {
						blkHash := common.BytesToHash(txData[len(txData)-32:])
						from, err := types.Sender(types.LatestSignerForChainID(tx.ChainId()), tx)
						if err == nil {
							data[blkHash] = append(data[blkHash], from)
						}
					}
				}
			}
		}
	}

	// Get masternodes from prevCheckpoint header
	prevHeader := chain.GetHeaderByNumber(prevCheckpoint)
	var masternodes []common.Address
	if prevHeader != nil {
		masternodes = c.GetMasternodesFromCheckpointHeader(prevHeader, prevCheckpoint, prevCheckpoint/rCheckpoint)
	}

	masternodeMap := make(map[common.Address]bool)
	for _, mn := range masternodes {
		masternodeMap[mn] = true
	}

	// Build signer map for the reward epoch
	for i := startBlockNumber; i <= endBlockNumber; i++ {
		if i%common.MergeSignRange == 0 {
			addrs := data[mapBlkHash[i]]
			if len(addrs) > 0 {
				// Deduplicate signers per block (like v2.6.8)
				addrSigners := make(map[common.Address]bool)
				for _, addr := range addrs {
					if masternodeMap[addr] {
						if _, ok := addrSigners[addr]; !ok {
							addrSigners[addr] = true
						}
					}
				}
				// Now count each unique signer once per block
				for addr := range addrSigners {
					if _, exist := signers[addr]; exist {
						signers[addr].Sign++
					} else {
						signers[addr] = &contracts.RewardLog{Sign: 1, Reward: new(big.Int)}
					}
					*totalSigner++
				}
			}
		}
	}

	log.Info("Calculate reward at checkpoint", "startBlock", startBlockNumber, "endBlock", endBlockNumber, "signers", len(signers))
	return signers, nil
}

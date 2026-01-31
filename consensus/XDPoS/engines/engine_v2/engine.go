// Copyright (c) 2018 XDPoSChain
// XDPoS V2 BFT Consensus Engine

package engine_v2

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/countdown"
	"github.com/ethereum/go-ethereum/common/lru"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/XDPoS/utils"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie"
)

var (
	errUnknownBlock     = errors.New("unknown block")
	errNotReady         = errors.New("V2 engine not ready")
	errInvalidTimestamp = errors.New("invalid timestamp")
)

// SignerFn is a signer callback function to request a hash to be signed
type SignerFn func(accounts.Account, []byte) ([]byte, error)

// XDPoS_v2 is the XDPoS 2.0 BFT consensus engine
type XDPoS_v2 struct {
	chainConfig *params.ChainConfig
	config      *params.XDPoSConfig
	db          ethdb.Database

	isInitilised bool
	whosTurn     common.Address

	// LRU caches
	snapshots       *lru.Cache[common.Hash, *SnapshotV2]
	signatures      *utils.SigLRU
	epochSwitches   *lru.Cache[common.Hash, *types.EpochSwitchInfo]
	verifiedHeaders *lru.Cache[common.Hash, struct{}]

	// Round to epoch block info mapping
	round2epochBlockInfo *lru.Cache[types.Round, *types.BlockInfo]

	// Signing
	signer   common.Address
	signFn   SignerFn
	lock     sync.RWMutex
	signLock sync.RWMutex

	// Channels
	BroadcastCh  chan interface{}
	minePeriodCh chan int
	newRoundCh   chan types.Round

	// Timeout worker
	timeoutWorker *countdown.CountdownTimer
	timeoutCount  int

	// Vote and timeout pools
	timeoutPool *utils.Pool
	votePool    *utils.Pool

	// BFT state
	currentRound          types.Round
	highestSelfMinedRound types.Round
	highestVotedRound     types.Round
	highestQuorumCert     *types.QuorumCert
	lockQuorumCert        *types.QuorumCert
	highestTimeoutCert    *types.TimeoutCert
	highestCommitBlock    *types.BlockInfo

	// Hooks for reward and penalty calculation
	HookReward  func(chain consensus.ChainReader, state *state.StateDB, parentState *state.StateDB, header *types.Header) (map[string]interface{}, error)
	HookPenalty func(chain consensus.ChainReader, number *big.Int, parentHash common.Hash, candidates []common.Address) ([]common.Address, error)

	// Forensics processor for detecting misbehavior
	ForensicsProcessor *Forensics

	// Track vote pool collection time for metrics
	votePoolCollectionTime time.Time
}

// getCertThreshold returns the certificate threshold
func (x *XDPoS_v2) getCertThreshold() float64 {
	if x.config.V2 != nil && x.config.V2.CertThreshold > 0 {
		return float64(x.config.V2.CertThreshold) / 100.0
	}
	return 0.667 // Default 2/3
}

// getMinePeriod returns the mining period
func (x *XDPoS_v2) getMinePeriod() int {
	if x.config.V2 != nil && x.config.V2.MinePeriod > 0 {
		return int(x.config.V2.MinePeriod)
	}
	return 2 // Default 2 seconds
}

// getTimeoutPeriod returns the timeout period
func (x *XDPoS_v2) getTimeoutPeriod() int {
	if x.config.V2 != nil && x.config.V2.TimeoutPeriod > 0 {
		return int(x.config.V2.TimeoutPeriod)
	}
	return 10 // Default 10 seconds
}

// getTimeoutSyncThreshold returns the timeout sync threshold
func (x *XDPoS_v2) getTimeoutSyncThreshold() int {
	if x.config.V2 != nil && x.config.V2.TimeoutSyncThreshold > 0 {
		return int(x.config.V2.TimeoutSyncThreshold)
	}
	return 5 // Default 5
}

// New creates a new XDPoS V2 engine
func New(chainConfig *params.ChainConfig, db ethdb.Database, minePeriodCh chan int, newRoundCh chan types.Round) *XDPoS_v2 {
	config := chainConfig.XDPoS

	// Setup timeout timer with exponential backoff
	timeoutPeriod := 10 // Default
	if config.V2 != nil && config.V2.TimeoutPeriod > 0 {
		timeoutPeriod = int(config.V2.TimeoutPeriod)
	}
	duration := time.Duration(timeoutPeriod) * time.Second

	// Default exponential backoff parameters
	base := 1.5
	maxExponent := uint8(5)

	timeoutTimer, err := countdown.NewExpCountDown(duration, base, maxExponent)
	if err != nil {
		log.Crit("Failed to create exp countdown", "err", err)
	}

	timeoutPool := utils.NewPool()
	votePool := utils.NewPool()

	engine := &XDPoS_v2{
		chainConfig:  chainConfig,
		config:       config,
		db:           db,
		isInitilised: false,

		signatures:      lru.NewCache[common.Hash, common.Address](utils.InmemorySignatures),
		verifiedHeaders: lru.NewCache[common.Hash, struct{}](utils.InmemorySnapshots),
		snapshots:       lru.NewCache[common.Hash, *SnapshotV2](utils.InmemorySnapshots),
		epochSwitches:   lru.NewCache[common.Hash, *types.EpochSwitchInfo](int(utils.InmemoryEpochs)),

		round2epochBlockInfo: lru.NewCache[types.Round, *types.BlockInfo](utils.InmemoryRound2Epochs),

		timeoutWorker: timeoutTimer,
		BroadcastCh:   make(chan interface{}),
		minePeriodCh:  minePeriodCh,
		newRoundCh:    newRoundCh,

		timeoutPool: timeoutPool,
		votePool:    votePool,

		highestSelfMinedRound: types.Round(0),
		highestVotedRound:     types.Round(0),

		highestTimeoutCert: &types.TimeoutCert{
			Round:      types.Round(0),
			Signatures: []types.Signature{},
		},
		highestQuorumCert: &types.QuorumCert{
			ProposedBlockInfo: &types.BlockInfo{
				Hash:   common.Hash{},
				Round:  types.Round(0),
				Number: big.NewInt(0),
			},
			Signatures: []types.Signature{},
			GapNumber:  0,
		},
		highestCommitBlock: nil,
		ForensicsProcessor: NewForensics(),
	}

	// Add callback to the timer
	timeoutTimer.OnTimeoutFn = engine.OnCountdownTimeout

	engine.periodicJob()

	return engine
}

// UpdateParams updates engine parameters based on header
func (x *XDPoS_v2) UpdateParams(header *types.Header) {
	// In the simplified config, parameters are static
	// Just update the timeout timer if needed
	minePeriod := x.getMinePeriod()
	go func() {
		x.minePeriodCh <- minePeriod
	}()
}

// SignHash returns the hash to be signed for a block header
func (x *XDPoS_v2) SignHash(header *types.Header) common.Hash {
	return sigHash(header)
}

// Initial initializes V2 related parameters
func (x *XDPoS_v2) Initial(chain consensus.ChainReader, header *types.Header) error {
	return x.initial(chain, header)
}

func (x *XDPoS_v2) initial(chain consensus.ChainReader, header *types.Header) error {
	log.Warn("[initial] Initializing V2 related parameters")

	if x.highestQuorumCert.ProposedBlockInfo.Hash != (common.Hash{}) {
		log.Info("[initial] Already initialized", "hash", x.highestQuorumCert.ProposedBlockInfo.Hash)
		x.isInitilised = true
		return nil
	}

	var quorumCert *types.QuorumCert
	var err error

	switchBlock := x.config.V2.SwitchBlock
	if header.Number.Int64() == switchBlock.Int64() {
		log.Info("[initial] Initializing from V2 switch block")
		blockInfo := &types.BlockInfo{
			Hash:   header.Hash(),
			Round:  types.Round(0),
			Number: header.Number,
		}
		gapNumber := header.Number.Uint64() - x.config.Gap
		if header.Number.Uint64() < x.config.Gap {
			gapNumber = 0
		}
		quorumCert = &types.QuorumCert{
			ProposedBlockInfo: blockInfo,
			Signatures:        nil,
			GapNumber:         gapNumber,
		}
		x.currentRound = 1
		x.highestQuorumCert = quorumCert
	} else {
		log.Info("[initial] Initializing from current header")
		quorumCert, _, _, err = x.getExtraFields(header)
		if err != nil {
			return err
		}
		err = x.processQC(chain, quorumCert)
		if err != nil {
			return err
		}
	}

	// Initialize first V2 snapshot
	lastGapNum := switchBlock.Uint64() - x.config.Gap
	if switchBlock.Uint64() < x.config.Gap {
		lastGapNum = 0
	}
	lastGapHeader := chain.GetHeaderByNumber(lastGapNum)

	snap, _ := loadSnapshot(x.db, lastGapHeader.Hash())

	if snap == nil {
		checkpointHeader := chain.GetHeaderByNumber(switchBlock.Uint64())
		log.Info("[initial] Creating first snapshot")

		_, _, masternodes, err := x.getExtraFields(checkpointHeader)
		if err != nil {
			log.Error("[initial] Error getting masternodes", "error", err)
			return err
		}

		if len(masternodes) == 0 {
			log.Error("[initial] masternodes are empty", "v2switch", switchBlock.Uint64())
			return fmt.Errorf("masternodes are empty at v2 switch number: %d", switchBlock.Uint64())
		}

		snap := newSnapshot(lastGapNum, lastGapHeader.Hash(), masternodes)
		x.snapshots.Add(snap.Hash, snap)
		err = storeSnapshot(snap, x.db)
		if err != nil {
			log.Error("[initial] Error storing snapshot", "error", err)
			return err
		}
	}

	// Initialize timeout
	minePeriod := x.getMinePeriod()
	log.Warn("[initial] miner wait period", "period", minePeriod)
	go func() {
		x.minePeriodCh <- minePeriod
	}()

	// Kick-off the countdown timer
	x.timeoutWorker.Reset(chain, 0, 0)
	x.isInitilised = true

	log.Warn("[initial] Initialization complete")
	return nil
}

// YourTurn checks if it's this node's turn to mine
func (x *XDPoS_v2) YourTurn(chain consensus.ChainReader, parent *types.Header, signer common.Address) (bool, error) {
	x.lock.RLock()
	defer x.lock.RUnlock()

	if !x.isInitilised {
		err := x.initial(chain, parent)
		if err != nil {
			log.Error("[YourTurn] Error initializing", "ParentBlockHash", parent.Hash(), "Error", err)
			return false, err
		}
	}

	minePeriod := x.getMinePeriod()
	waitedTime := time.Now().Unix() - int64(parent.Time)
	if waitedTime < int64(minePeriod) {
		log.Trace("[YourTurn] wait after mine period", "minePeriod", minePeriod, "waitedTime", waitedTime)
		return false, nil
	}

	round := x.currentRound
	isMyTurn, err := x.yourturn(chain, round, parent, signer)
	if err != nil {
		log.Warn("[YourTurn] Error checking turn", "round", round, "error", err)
	}

	return isMyTurn, err
}

// yourturn checks if signer is the leader for the given round
func (x *XDPoS_v2) yourturn(chain consensus.ChainReader, round types.Round, parent *types.Header, signer common.Address) (bool, error) {
	masternodes := x.GetMasternodes(chain, parent)
	if len(masternodes) == 0 {
		return false, errors.New("empty masternode list")
	}

	leaderIndex := int(round) % len(masternodes)
	leader := masternodes[leaderIndex]

	x.whosTurn = leader
	return leader == signer, nil
}

// Prepare implements consensus.Engine, preparing all consensus fields
func (x *XDPoS_v2) Prepare(chain consensus.ChainReader, header *types.Header) error {
	x.lock.RLock()
	currentRound := x.currentRound
	highestQC := x.highestQuorumCert
	x.lock.RUnlock()

	if header.ParentHash != highestQC.ProposedBlockInfo.Hash {
		log.Warn("[Prepare] parent hash and QC hash mismatch", "blockNum", header.Number, "QCNumber", highestQC.ProposedBlockInfo.Number, "blockHash", header.ParentHash, "QCHash", highestQC.ProposedBlockInfo.Hash)
		return utils.ErrNotReadyToPropose
	}

	extra := types.ExtraFields_v2{
		Round:      currentRound,
		QuorumCert: highestQC,
	}

	extraBytes, err := extra.EncodeToBytes()
	if err != nil {
		return err
	}
	header.Extra = extraBytes
	header.Nonce = types.BlockNonce{}

	number := header.Number.Uint64()
	parent := chain.GetHeader(header.ParentHash, number-1)

	log.Info("Preparing new block!", "Number", number, "Parent Hash", parent.Hash())
	if parent == nil {
		return utils.ErrUnknownAncestor
	}

	x.signLock.RLock()
	signer := x.signer
	x.signLock.RUnlock()

	isMyTurn, err := x.yourturn(chain, currentRound, parent, signer)
	if err != nil {
		log.Error("[Prepare] Error checking turn", "currentRound", currentRound, "ParentHash", parent.Hash().Hex(), "ParentNumber", parent.Number.Uint64(), "error", err)
		return err
	}
	if !isMyTurn {
		return utils.ErrNotReadyToMine
	}

	// Set difficulty
	header.Difficulty = x.calcDifficulty(chain, parent, signer)
	log.Debug("CalcDifficulty", "number", header.Number, "difficulty", header.Difficulty)

	isEpochSwitchBlock, _, err := x.IsEpochSwitch(header)
	if err != nil {
		log.Error("[Prepare] Error checking epoch switch", "header", header, "Error", err)
		return err
	}

	if isEpochSwitchBlock {
		masterNodes, penalties, err := x.calcMasternodes(chain, header.Number, header.ParentHash, currentRound)
		if err != nil {
			return err
		}
		for _, v := range masterNodes {
			header.Validators = append(header.Validators, v[:]...)
		}
		for _, v := range penalties {
			header.Penalties = append(header.Penalties, v[:]...)
		}
	}

	// Mix digest is reserved, set to empty
	header.MixDigest = common.Hash{}

	// Set timestamp
	header.Time = parent.Time + uint64(x.config.Period)
	if header.Time < uint64(time.Now().Unix()) {
		header.Time = uint64(time.Now().Unix())
	}

	if header.Coinbase != signer {
		log.Error("[Prepare] Coinbase mismatch", "headerCoinbase", header.Coinbase.Hex(), "WalletAddress", signer.Hex())
		return utils.ErrCoinbaseMismatch
	}

	return nil
}

// Finalize implements consensus.Engine
func (x *XDPoS_v2) Finalize(chain consensus.ChainReader, header *types.Header, state *state.StateDB, parentState *state.StateDB, txs []*types.Transaction, uncles []*types.Header, receipts []*types.Receipt) (*types.Block, error) {
	isEpochSwitch, _, err := x.IsEpochSwitch(header)
	if err != nil {
		log.Error("[Finalize] IsEpochSwitch error!", "err", err)
		return nil, err
	}

	if x.HookReward != nil && isEpochSwitch {
		rewards, err := x.HookReward(chain, state, parentState, header)
		if err != nil {
			return nil, err
		}
		if len(common.StoreRewardFolder) > 0 {
			data, err := json.Marshal(rewards)
			if err == nil {
				err = os.WriteFile(filepath.Join(common.StoreRewardFolder, header.Number.String()+"."+header.Hash().Hex()), data, 0644)
			}
			if err != nil {
				log.Error("Error saving reward info", "number", header.Number, "hash", header.Hash().Hex(), "err", err)
			}
		}
	}

	// Set state root and uncle hash
	header.Root = state.IntermediateRoot(chain.Config().IsEIP158(header.Number))
	header.UncleHash = types.CalcUncleHash(nil)

	// Assemble and return the final block
	return types.NewBlock(header, &types.Body{Transactions: txs}, receipts, trie.NewStackTrie(nil)), nil
}

// Authorize injects a private key into the consensus engine
func (x *XDPoS_v2) Authorize(signer common.Address, signFn SignerFn) {
	x.signLock.Lock()
	defer x.signLock.Unlock()
	x.signer = signer
	x.signFn = signFn
}

// Author retrieves the block author
func (x *XDPoS_v2) Author(header *types.Header) (common.Address, error) {
	return ecrecover(header, x.signatures)
}

// Seal implements consensus.Engine, creating a sealed block
func (x *XDPoS_v2) Seal(chain consensus.ChainReader, block *types.Block, stop <-chan struct{}) (*types.Block, error) {
	header := block.Header()

	// Sealing genesis is not supported
	number := header.Number.Uint64()
	if number == 0 {
		return nil, utils.ErrUnknownBlock
	}

	x.signLock.RLock()
	signer, signFn := x.signer, x.signFn
	x.signLock.RUnlock()

	select {
	case <-stop:
		return nil, nil
	default:
	}

	// Sign the header
	signature, err := signFn(accounts.Account{Address: signer}, sigHash(header).Bytes())
	if err != nil {
		return nil, err
	}
	header.Validator = signature

	// Mark the highest self-mined round
	var decodedExtraField types.ExtraFields_v2
	err = utils.DecodeBytesExtraFields(header.Extra, &decodedExtraField)
	if err != nil {
		log.Error("[Seal] Error decoding extra field", "Hash", header.Hash().Hex(), "Number", header.Number.Uint64(), "Error", err)
		return nil, err
	}
	x.highestSelfMinedRound = decodedExtraField.Round

	return block.WithSeal(header), nil
}

// CalcDifficulty is the difficulty adjustment algorithm
func (x *XDPoS_v2) CalcDifficulty(chain consensus.ChainReader, time uint64, parent *types.Header) *big.Int {
	return x.calcDifficulty(chain, parent, x.signer)
}

func (x *XDPoS_v2) calcDifficulty(chain consensus.ChainReader, parent *types.Header, signer common.Address) *big.Int {
	// In V2, difficulty is always 1
	return big.NewInt(1)
}

// VerifyUncles implements consensus.Engine
func (x *XDPoS_v2) VerifyUncles(chain consensus.ChainReader, block *types.Block) error {
	if len(block.Uncles()) > 0 {
		return errors.New("uncles not allowed in XDPoS_v2")
	}
	return nil
}

// VerifyHeader implements consensus.Engine
func (x *XDPoS_v2) VerifyHeader(chain consensus.ChainReader, header *types.Header, fullVerify bool) error {
	err := x.verifyHeader(chain, header, nil, fullVerify)
	if err != nil {
		log.Debug("[VerifyHeader] Verification failed", "fullVerify", fullVerify, "blockNum", header.Number, "blockHash", header.Hash(), "error", err)
	}
	return err
}

// VerifyHeaders implements consensus.Engine for batch verification
func (x *XDPoS_v2) VerifyHeaders(chain consensus.ChainReader, headers []*types.Header, fullVerifies []bool, abort <-chan struct{}, results chan<- error) {
	go func() {
		for i, header := range headers {
			err := x.verifyHeader(chain, header, headers[:i], fullVerifies[i])
			if err != nil {
				log.Warn("[VerifyHeaders] Verification failed", "fullVerify", fullVerifies[i], "blockNum", header.Number, "blockHash", header.Hash(), "error", err)
			}
			select {
			case <-abort:
				return
			case results <- err:
			}
		}
	}()
}

// verifyHeader checks whether a header conforms to the consensus rules
func (x *XDPoS_v2) verifyHeader(chain consensus.ChainReader, header *types.Header, parents []*types.Header, fullVerify bool) error {
	if header.Number == nil {
		return errUnknownBlock
	}

	number := header.Number.Uint64()

	// Check if already verified
	if _, ok := x.verifiedHeaders.Get(header.Hash()); ok {
		return nil
	}

	// Check timestamp is not too far in the future
	if header.Time > uint64(time.Now().Unix())+15 {
		return utils.ErrFutureBlock
	}

	// For V2 blocks, verify the extra field and QC
	switchBlock := x.config.V2.SwitchBlock
	if header.Number.Cmp(switchBlock) > 0 {
		quorumCert, _, _, err := x.getExtraFields(header)
		if err != nil {
			return err
		}

		if fullVerify && quorumCert != nil {
			// Find parent header for QC verification
			var parentHeader *types.Header
			if len(parents) > 0 {
				parentHeader = parents[len(parents)-1]
			} else {
				parentHeader = chain.GetHeader(header.ParentHash, number-1)
			}

			err = x.verifyQC(chain, quorumCert, parentHeader)
			if err != nil {
				return err
			}
		}
	}

	// Add to verified cache
	x.verifiedHeaders.Add(header.Hash(), struct{}{})

	return nil
}

// GetSnapshot returns the V2 snapshot for a header
func (x *XDPoS_v2) GetSnapshot(chain consensus.ChainReader, header *types.Header) (*SnapshotV2, error) {
	number := header.Number.Uint64()
	log.Trace("get snapshot", "number", number)
	snap, err := x.getSnapshot(chain, number, false)
	if err != nil {
		return nil, err
	}
	return snap, nil
}

// IsAuthorisedAddress checks if address is in the masternode list
func (x *XDPoS_v2) IsAuthorisedAddress(chain consensus.ChainReader, header *types.Header, address common.Address) bool {
	snap, err := x.GetSnapshot(chain, header)
	if err != nil {
		log.Error("[IsAuthorisedAddress] Can't get snapshot", "number", header.Number, "hash", header.Hash().Hex(), "err", err)
		return false
	}
	for _, mn := range snap.NextEpochCandidates {
		if mn == address {
			return true
		}
	}
	return false
}

// UpdateMasternodes updates the masternode list snapshot
func (x *XDPoS_v2) UpdateMasternodes(chain consensus.ChainReader, header *types.Header, ms []utils.Masternode) error {
	number := header.Number.Uint64()
	log.Trace("[UpdateMasternodes]")

	masterNodes := []common.Address{}
	for _, m := range ms {
		masterNodes = append(masterNodes, m.Address)
	}

	x.lock.RLock()
	snap := newSnapshot(number, header.Hash(), masterNodes)
	log.Info("[UpdateMasternodes] take snapshot", "number", number, "hash", header.Hash())
	x.lock.RUnlock()

	err := storeSnapshot(snap, x.db)
	if err != nil {
		log.Error("[UpdateMasternodes] Error storing snapshot", "hash", header.Hash(), "currentRound", x.currentRound, "error", err)
		return err
	}
	x.snapshots.Add(snap.Hash, snap)

	log.Info("[UpdateMasternodes] New masternode set updated", "number", snap.Number, "hash", snap.Hash)
	for i, n := range ms {
		log.Info("masternode", "index", i, "address", n.Address.String())
	}

	return nil
}

// FindParentBlockToAssign returns the parent block from highest QC
func (x *XDPoS_v2) FindParentBlockToAssign(chain consensus.ChainReader) *types.Block {
	parent := chain.GetBlock(x.highestQuorumCert.ProposedBlockInfo.Hash, x.highestQuorumCert.ProposedBlockInfo.Number.Uint64())
	if parent == nil {
		log.Error("[FindParentBlockToAssign] Can not find parent block", "hash", x.highestQuorumCert.ProposedBlockInfo.Hash, "number", x.highestQuorumCert.ProposedBlockInfo.Number.Uint64())
	}
	return parent
}

// GetLatestCommittedBlockInfo returns the latest committed block
func (x *XDPoS_v2) GetLatestCommittedBlockInfo() *types.BlockInfo {
	return x.highestCommitBlock
}

// GetCurrentRound returns the current round
func (x *XDPoS_v2) GetCurrentRound() types.Round {
	x.lock.RLock()
	defer x.lock.RUnlock()
	return x.currentRound
}

// GetHighestQC returns the highest quorum certificate
func (x *XDPoS_v2) GetHighestQC() *types.QuorumCert {
	x.lock.RLock()
	defer x.lock.RUnlock()
	return x.highestQuorumCert
}

// setNewRound sets the new round and resets pools and timers
func (x *XDPoS_v2) setNewRound(blockChainReader consensus.ChainReader, round types.Round) {
	log.Info("[setNewRound] new round and reset pools and workers", "round", round)
	x.currentRound = round
	x.timeoutCount = 0
	x.timeoutWorker.Reset(blockChainReader, x.currentRound, x.highestQuorumCert.ProposedBlockInfo.Round)
	x.timeoutPool.Clear()

	// Send signal to newRoundCh, but don't block if full
	select {
	case x.newRoundCh <- round:
	default:
	}
}

// broadcastToBftChannel sends a message to the BFT broadcast channel
func (x *XDPoS_v2) broadcastToBftChannel(msg interface{}) {
	go func() {
		x.BroadcastCh <- msg
	}()
}

// getSyncInfo returns the current sync info
func (x *XDPoS_v2) getSyncInfo() *types.SyncInfo {
	return &types.SyncInfo{
		HighestQuorumCert:  x.highestQuorumCert,
		HighestTimeoutCert: x.highestTimeoutCert,
	}
}

// allowedToSend checks if the node is allowed to send BFT messages
func (x *XDPoS_v2) allowedToSend(chain consensus.ChainReader, blockHeader *types.Header, sendType string) bool {
	x.signLock.RLock()
	signer := x.signer
	x.signLock.RUnlock()

	masterNodes := x.GetMasternodes(chain, blockHeader)
	for i, mn := range masterNodes {
		if signer == mn {
			log.Debug("[allowedToSend] Yes, I'm allowed to send", "sendType", sendType, "MyAddress", signer.Hex(), "Index", i)
			return true
		}
	}
	log.Debug("[allowedToSend] Not in Masternode list", "sendType", sendType, "MyAddress", signer.Hex())
	return false
}

// periodicJob runs periodic cleanup tasks
func (x *XDPoS_v2) periodicJob() {
	go func() {
		ticker := time.NewTicker(utils.PeriodicJobPeriod * time.Second)
		defer ticker.Stop()
		for {
			<-ticker.C
			x.hygieneVotePool()
			x.hygieneTimeoutPool()
		}
	}()
}

// ReceivedVotes returns all received votes
func (x *XDPoS_v2) ReceivedVotes() map[string]map[common.Hash]utils.PoolObj {
	return x.votePool.Get()
}

// ReceivedTimeouts returns all received timeouts
func (x *XDPoS_v2) ReceivedTimeouts() map[string]map[common.Hash]utils.PoolObj {
	return x.timeoutPool.Get()
}

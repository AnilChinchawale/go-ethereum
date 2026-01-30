// Copyright (c) 2024 XDC Network
// XDPoS 2.0 BFT Consensus Engine
// This implements the HotStuff-based BFT consensus for XDC Network

package engine_v2

import (
	"errors"
	"fmt"
	"math/big"
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
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"golang.org/x/crypto/sha3"
)

const (
	// Cache sizes
	InMemorySnapshots  = 128
	InMemorySignatures = 4096
	InMemoryEpochs     = 10
	InMemoryRound2Epochs = 100

	// Pool hygiene
	PoolHygieneRound = 10
	
	// Periodic job interval
	PeriodicJobPeriod = 60 // seconds
)

// SignerFn is a signer callback function
type SignerFn func(accounts.Account, []byte) ([]byte, error)

// XDPoS_v2 is the XDPoS 2.0 BFT consensus engine
type XDPoS_v2 struct {
	chainConfig *params.ChainConfig
	config      *params.XDPoSConfig
	db          ethdb.Database
	isInitialized bool
	whosTurn    common.Address

	// Caches
	snapshots       *lru.Cache[common.Hash, *SnapshotV2]
	signatures      *lru.Cache[common.Hash, common.Address]
	epochSwitches   *lru.Cache[common.Hash, *types.EpochSwitchInfo]
	verifiedHeaders *lru.Cache[common.Hash, struct{}]
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

	// Timeout handling
	timeoutWorker *countdown.ExpCountDown
	timeoutCount  int

	// Pools
	timeoutPool *utils.Pool
	votePool    *utils.Pool

	// Round state
	currentRound          types.Round
	highestSelfMinedRound types.Round
	highestVotedRound     types.Round
	highestQuorumCert     *types.QuorumCert
	lockQuorumCert        *types.QuorumCert
	highestTimeoutCert    *types.TimeoutCert
	highestCommitBlock    *types.BlockInfo

	// Hooks
	HookReward  func(chain consensus.ChainReader, state *state.StateDB, parentState *state.StateDB, header *types.Header) (map[string]interface{}, error)
	HookPenalty func(chain consensus.ChainReader, number *big.Int, parentHash common.Hash, candidates []common.Address) ([]common.Address, error)

	votePoolCollectionTime time.Time
}

// New creates a new XDPoS 2.0 engine
func New(chainConfig *params.ChainConfig, db ethdb.Database, minePeriodCh chan int, newRoundCh chan types.Round) *XDPoS_v2 {
	config := chainConfig.XDPoS
	
	// Get timeout config from V2 config
	timeoutPeriod := 10 // default
	expBase := 2.0
	maxExponent := 6
	
	if config.V2 != nil && config.V2.CurrentConfig != nil {
		timeoutPeriod = config.V2.CurrentConfig.TimeoutPeriod
		expBase = config.V2.CurrentConfig.ExpTimeoutConfig.Base
		maxExponent = config.V2.CurrentConfig.ExpTimeoutConfig.MaxExponent
	}

	duration := time.Duration(timeoutPeriod) * time.Second
	timeoutTimer, err := countdown.NewExpCountDown(duration, expBase, maxExponent)
	if err != nil {
		log.Crit("create exp countdown", "err", err)
	}

	engine := &XDPoS_v2{
		chainConfig: chainConfig,
		config:      config,
		db:          db,
		isInitialized: false,

		signatures:      lru.NewCache[common.Hash, common.Address](InMemorySignatures),
		verifiedHeaders: lru.NewCache[common.Hash, struct{}](InMemorySnapshots),
		snapshots:       lru.NewCache[common.Hash, *SnapshotV2](InMemorySnapshots),
		epochSwitches:   lru.NewCache[common.Hash, *types.EpochSwitchInfo](InMemoryEpochs),
		round2epochBlockInfo: lru.NewCache[types.Round, *types.BlockInfo](InMemoryRound2Epochs),
		
		timeoutWorker: timeoutTimer,
		BroadcastCh:   make(chan interface{}),
		minePeriodCh:  minePeriodCh,
		newRoundCh:    newRoundCh,

		timeoutPool: utils.NewPool(),
		votePool:    utils.NewPool(),

		highestSelfMinedRound: types.Round(0),
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
		highestVotedRound:  types.Round(0),
		highestCommitBlock: nil,
	}

	// Set timeout callback
	timeoutTimer.OnTimeoutFn = engine.OnCountdownTimeout

	// Start periodic job
	engine.periodicJob()

	return engine
}

// sigHash returns the hash which is used for signing
func sigHash(header *types.Header) common.Hash {
	hasher := sha3.NewLegacyKeccak256()

	enc := []interface{}{
		header.ParentHash,
		header.UncleHash,
		header.Coinbase,
		header.Root,
		header.TxHash,
		header.ReceiptHash,
		header.Bloom,
		header.Difficulty,
		header.Number,
		header.GasLimit,
		header.GasUsed,
		header.Time,
		header.Extra,
		header.MixDigest,
		header.Nonce,
	}
	// V2 blocks include Validators and Penalties
	if len(header.Validators) > 0 {
		enc = append(enc, header.Validators)
	}
	if len(header.Penalties) > 0 {
		enc = append(enc, header.Penalties)
	}
	if header.BaseFee != nil {
		enc = append(enc, header.BaseFee)
	}
	if err := rlp.Encode(hasher, enc); err != nil {
		panic("rlp.Encode fail: " + err.Error())
	}
	var hash common.Hash
	hasher.Sum(hash[:0])
	return hash
}

// ecrecover extracts the signer address from a signed header
func ecrecover(header *types.Header, sigcache *lru.Cache[common.Hash, common.Address]) (common.Address, error) {
	hash := header.Hash()
	if address, known := sigcache.Get(hash); known {
		return address, nil
	}

	// V2 blocks store signature in header.Validator field
	if len(header.Validator) == 0 {
		return common.Address{}, errors.New("no validator signature")
	}

	pubkey, err := crypto.Ecrecover(sigHash(header).Bytes(), header.Validator)
	if err != nil {
		return common.Address{}, err
	}
	var signer common.Address
	copy(signer[:], crypto.Keccak256(pubkey[1:])[12:])

	sigcache.Add(hash, signer)
	return signer, nil
}

// SignHash returns the hash for signing
func (x *XDPoS_v2) SignHash(header *types.Header) common.Hash {
	return sigHash(header)
}

// Initial initializes V2 engine state from the current chain state
func (x *XDPoS_v2) Initial(chain consensus.ChainReader, header *types.Header) error {
	x.lock.Lock()
	defer x.lock.Unlock()
	return x.initial(chain, header)
}

func (x *XDPoS_v2) initial(chain consensus.ChainReader, header *types.Header) error {
	log.Warn("[initial] initializing v2 related parameters")

	if x.highestQuorumCert.ProposedBlockInfo.Hash != (common.Hash{}) {
		log.Info("[initial] Already initialized", "hash", x.highestQuorumCert.ProposedBlockInfo.Hash)
		x.isInitialized = true
		return nil
	}

	var quorumCert *types.QuorumCert
	var err error

	if header.Number.Int64() == x.config.V2.SwitchBlock.Int64() {
		// First V2 block - create initial QC
		log.Info("[initial] highest QC for consensus v2 first block")
		blockInfo := &types.BlockInfo{
			Hash:   header.Hash(),
			Round:  types.Round(0),
			Number: header.Number,
		}
		gapNumber := header.Number.Uint64()
		if gapNumber > x.config.Gap {
			gapNumber -= x.config.Gap
		} else {
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
		// Get QC from header
		log.Info("[initial] highest QC from current header")
		quorumCert, _, _, err = x.getExtraFields(header)
		if err != nil {
			return err
		}
		err = x.processQC(chain, quorumCert)
		if err != nil {
			return err
		}
	}

	// Initialize first v2 snapshot
	lastGapNum := uint64(0)
	if x.config.V2.SwitchBlock.Uint64() > x.config.Gap {
		lastGapNum = x.config.V2.SwitchBlock.Uint64() - x.config.Gap
	}
	lastGapHeader := chain.GetHeaderByNumber(lastGapNum)

	snap, _ := loadSnapshot(x.db, lastGapHeader.Hash())
	if snap == nil {
		checkpointHeader := chain.GetHeaderByNumber(x.config.V2.SwitchBlock.Uint64())
		log.Info("[initial] init first snapshot")
		
		_, _, masternodes, err := x.getExtraFields(checkpointHeader)
		if err != nil {
			log.Error("[initial] Error while get masternodes", "error", err)
			return err
		}

		if len(masternodes) == 0 {
			log.Error("[initial] masternodes are empty", "v2switch", x.config.V2.SwitchBlock.Uint64())
			return fmt.Errorf("masternodes are empty v2 switch number: %d", x.config.V2.SwitchBlock.Uint64())
		}

		snap := newSnapshot(lastGapNum, lastGapHeader.Hash(), masternodes)
		x.snapshots.Add(snap.Hash, snap)
		if err := storeSnapshot(snap, x.db); err != nil {
			log.Error("[initial] Error while store snapshot", "error", err)
			return err
		}
	}

	// Initialize timeout
	minePeriod := x.config.V2.CurrentConfig.MinePeriod
	log.Warn("[initial] miner wait period", "period", minePeriod)
	go func() {
		x.minePeriodCh <- minePeriod
	}()

	// Start countdown timer
	x.timeoutWorker.Reset(chain, 0, 0)
	x.isInitialized = true

	log.Warn("[initial] finish initialisation")
	return nil
}

// Author returns the signer of a block
func (x *XDPoS_v2) Author(header *types.Header) (common.Address, error) {
	return ecrecover(header, x.signatures)
}

// VerifyHeader verifies a header for V2 consensus
func (x *XDPoS_v2) VerifyHeader(chain consensus.ChainReader, header *types.Header, fullVerify bool) error {
	err := x.verifyHeader(chain, header, nil, fullVerify)
	if err != nil {
		log.Debug("[VerifyHeader] Fail to verify header", "fullVerify", fullVerify, "blockNum", header.Number, "error", err)
	}
	return err
}

// VerifyHeaders verifies a batch of headers
func (x *XDPoS_v2) VerifyHeaders(chain consensus.ChainReader, headers []*types.Header, fullVerifies []bool, abort <-chan struct{}, results chan<- error) {
	go func() {
		for i, header := range headers {
			err := x.verifyHeader(chain, header, headers[:i], fullVerifies[i])
			if err != nil {
				log.Warn("[VerifyHeaders] Fail to verify header", "fullVerify", fullVerifies[i], "blockNum", header.Number, "error", err)
			}
			select {
			case <-abort:
				return
			case results <- err:
			}
		}
	}()
}

// verifyHeader performs header verification
func (x *XDPoS_v2) verifyHeader(chain consensus.ChainReader, header *types.Header, parents []*types.Header, fullVerify bool) error {
	if header.Number == nil {
		return utils.ErrUnknownBlock
	}

	// Check if already verified
	if _, ok := x.verifiedHeaders.Get(header.Hash()); ok {
		return nil
	}

	number := header.Number.Uint64()

	// Don't verify future blocks
	if header.Time > uint64(time.Now().Unix()+15) {
		return consensus.ErrFutureBlock
	}

	// Get parent
	var parent *types.Header
	if len(parents) > 0 {
		parent = parents[len(parents)-1]
	} else {
		parent = chain.GetHeader(header.ParentHash, number-1)
	}
	if parent == nil {
		return consensus.ErrUnknownAncestor
	}

	// Verify gas limit
	if header.GasUsed > header.GasLimit {
		return fmt.Errorf("gas used exceeds gas limit: %d > %d", header.GasUsed, header.GasLimit)
	}

	// Verify no uncles
	if header.UncleHash != types.CalcUncleHash(nil) {
		return errors.New("uncles not allowed in XDPoS_v2")
	}

	// For full verification, check QC
	if fullVerify {
		quorumCert, _, _, err := x.getExtraFields(header)
		if err != nil {
			return err
		}
		if err := x.verifyQC(chain, quorumCert, parent); err != nil {
			return err
		}
	}

	x.verifiedHeaders.Add(header.Hash(), struct{}{})
	return nil
}

// VerifyUncles always returns an error since XDPoS doesn't allow uncles
func (x *XDPoS_v2) VerifyUncles(chain consensus.ChainReader, block *types.Block) error {
	if len(block.Uncles()) > 0 {
		return errors.New("uncles not allowed in XDPoS_v2")
	}
	return nil
}

// Prepare prepares a header for mining
func (x *XDPoS_v2) Prepare(chain consensus.ChainReader, header *types.Header) error {
	x.lock.RLock()
	currentRound := x.currentRound
	highestQC := x.highestQuorumCert
	x.lock.RUnlock()

	if header.ParentHash != highestQC.ProposedBlockInfo.Hash {
		log.Warn("[Prepare] parent hash and QC hash mismatch",
			"blockNum", header.Number,
			"QCNumber", highestQC.ProposedBlockInfo.Number,
			"blockHash", header.ParentHash,
			"QCHash", highestQC.ProposedBlockInfo.Hash)
		return utils.ErrNotReadyToPropose
	}

	// Set extra fields
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
	if parent == nil {
		return consensus.ErrUnknownAncestor
	}

	log.Info("Preparing new block!", "Number", number, "Parent Hash", parent.Hash())

	x.signLock.RLock()
	signer := x.signer
	x.signLock.RUnlock()

	// Verify it's our turn
	isMyTurn, err := x.yourturn(chain, currentRound, parent, signer)
	if err != nil {
		log.Error("[Prepare] Error checking turn", "currentRound", currentRound, "error", err)
		return err
	}
	if !isMyTurn {
		return utils.ErrNotReadyToMine
	}

	// Set difficulty
	header.Difficulty = x.calcDifficulty(chain, parent, signer)

	// Handle epoch switch
	isEpochSwitch, _, err := x.IsEpochSwitch(header)
	if err != nil {
		log.Error("[Prepare] Error checking epoch switch", "error", err)
		return err
	}
	if isEpochSwitch {
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

	header.MixDigest = common.Hash{}

	// Set timestamp
	header.Time = parent.Time + uint64(x.config.Period)
	if header.Time < uint64(time.Now().Unix()) {
		header.Time = uint64(time.Now().Unix())
	}

	if header.Coinbase != signer {
		log.Error("[Prepare] Coinbase mismatch", "headerCoinbase", header.Coinbase, "signer", signer)
		return errors.New("coinbase mismatch with signer")
	}

	return nil
}

// Finalize finalizes a block
func (x *XDPoS_v2) Finalize(chain consensus.ChainReader, header *types.Header, state *state.StateDB, parentState *state.StateDB, txs []*types.Transaction, uncles []*types.Header, receipts []*types.Receipt) (*types.Block, error) {
	isEpochSwitch, _, err := x.IsEpochSwitch(header)
	if err != nil {
		log.Error("[Finalize] IsEpochSwitch bug!", "err", err)
		return nil, err
	}

	if x.HookReward != nil && isEpochSwitch {
		_, err := x.HookReward(chain, state, parentState, header)
		if err != nil {
			return nil, err
		}
	}

	parentHeader := chain.GetHeader(header.ParentHash, header.Number.Uint64()-1)
	if parentHeader == nil {
		return nil, consensus.ErrUnknownAncestor
	}

	header.Root = state.IntermediateRoot(chain.Config().IsEIP158(header.Number))
	header.UncleHash = types.CalcUncleHash(nil)

	return types.NewBlock(header, &types.Body{Transactions: txs}, receipts, trie.NewStackTrie(nil)), nil
}

// Seal seals a block
func (x *XDPoS_v2) Seal(chain consensus.ChainReader, block *types.Block, stop <-chan struct{}) (*types.Block, error) {
	header := block.Header()

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

	// Sign the block
	signature, err := signFn(accounts.Account{Address: signer}, sigHash(header).Bytes())
	if err != nil {
		return nil, err
	}
	header.Validator = signature

	// Track highest self-mined round
	var decodedExtra types.ExtraFields_v2
	if err := DecodeExtraFields(header.Extra, &decodedExtra); err != nil {
		log.Error("[Seal] Error decoding extra field", "error", err)
		return nil, err
	}
	x.highestSelfMinedRound = decodedExtra.Round

	return block.WithSeal(header), nil
}

// Authorize sets the signer
func (x *XDPoS_v2) Authorize(signer common.Address, signFn SignerFn) {
	x.signLock.Lock()
	defer x.signLock.Unlock()
	x.signer = signer
	x.signFn = signFn
}

// CalcDifficulty returns the block difficulty
func (x *XDPoS_v2) CalcDifficulty(chain consensus.ChainReader, time uint64, parent *types.Header) *big.Int {
	return x.calcDifficulty(chain, parent, x.signer)
}

func (x *XDPoS_v2) calcDifficulty(chain consensus.ChainReader, parent *types.Header, signer common.Address) *big.Int {
	// V2 uses a simple difficulty of 1
	return big.NewInt(1)
}

// YourTurn checks if it's the signer's turn
func (x *XDPoS_v2) YourTurn(chain consensus.ChainReader, parent *types.Header, signer common.Address) (bool, error) {
	x.lock.RLock()
	defer x.lock.RUnlock()

	if !x.isInitialized {
		if err := x.initial(chain, parent); err != nil {
			log.Error("[YourTurn] Error initializing", "error", err)
			return false, err
		}
	}

	// Check if enough time has passed
	waitedTime := time.Now().Unix() - int64(parent.Time)
	minePeriod := x.config.V2.CurrentConfig.MinePeriod
	if waitedTime < int64(minePeriod) {
		return false, nil
	}

	round := x.currentRound
	return x.yourturn(chain, round, parent, signer)
}

func (x *XDPoS_v2) yourturn(chain consensus.ChainReader, round types.Round, parent *types.Header, signer common.Address) (bool, error) {
	snap, err := x.getSnapshot(chain, parent.Number.Uint64(), false)
	if err != nil {
		return false, err
	}

	masternodes := snap.NextEpochCandidates
	if len(masternodes) == 0 {
		return false, errors.New("no masternodes")
	}

	// Calculate whose turn it is
	idx := uint64(round) % uint64(len(masternodes))
	expected := masternodes[idx]

	x.whosTurn = expected
	return signer == expected, nil
}

// GetSnapshot returns the snapshot for a header
func (x *XDPoS_v2) GetSnapshot(chain consensus.ChainReader, header *types.Header) (*SnapshotV2, error) {
	return x.getSnapshot(chain, header.Number.Uint64(), false)
}

// getSnapshot retrieves or creates a snapshot
func (x *XDPoS_v2) getSnapshot(chain consensus.ChainReader, number uint64, forSigning bool) (*SnapshotV2, error) {
	// Try cache first
	gapNumber := number - number%x.config.Epoch
	if gapNumber > x.config.Gap {
		gapNumber -= x.config.Gap
	} else {
		gapNumber = 0
	}

	gapHeader := chain.GetHeaderByNumber(gapNumber)
	if gapHeader == nil {
		return nil, fmt.Errorf("no header at gap number %d", gapNumber)
	}

	// Check cache
	if snap, ok := x.snapshots.Get(gapHeader.Hash()); ok {
		return snap, nil
	}

	// Try loading from DB
	snap, err := loadSnapshot(x.db, gapHeader.Hash())
	if err == nil && snap != nil {
		x.snapshots.Add(snap.Hash, snap)
		return snap, nil
	}

	// Create new snapshot from checkpoint
	checkpointNumber := number - number%x.config.Epoch
	if checkpointNumber == 0 {
		checkpointNumber = x.config.Epoch
	}
	checkpointHeader := chain.GetHeaderByNumber(checkpointNumber)
	if checkpointHeader == nil {
		return nil, fmt.Errorf("no checkpoint header at %d", checkpointNumber)
	}

	masternodes := x.GetMasternodesFromEpochSwitchHeader(checkpointHeader)
	snap = newSnapshot(gapNumber, gapHeader.Hash(), masternodes)
	x.snapshots.Add(snap.Hash, snap)

	return snap, nil
}

// GetMasternodesFromEpochSwitchHeader extracts masternodes from epoch switch header
func (x *XDPoS_v2) GetMasternodesFromEpochSwitchHeader(header *types.Header) []common.Address {
	if header == nil || len(header.Validators) == 0 {
		return []common.Address{}
	}
	masternodes := make([]common.Address, len(header.Validators)/common.AddressLength)
	for i := 0; i < len(masternodes); i++ {
		copy(masternodes[i][:], header.Validators[i*common.AddressLength:])
	}
	return masternodes
}

// GetMasternodes returns masternodes for a header
func (x *XDPoS_v2) GetMasternodes(chain consensus.ChainReader, header *types.Header) []common.Address {
	epochSwitchInfo, err := x.getEpochSwitchInfo(chain, header, header.Hash())
	if err != nil {
		log.Error("[GetMasternodes] Error getting epoch switch info", "err", err)
		return []common.Address{}
	}
	return epochSwitchInfo.Masternodes
}

// IsEpochSwitch checks if a header is an epoch switch block
func (x *XDPoS_v2) IsEpochSwitch(header *types.Header) (bool, uint64, error) {
	number := header.Number.Uint64()
	if number == 0 {
		return false, 0, nil
	}

	// V2 epoch switches happen at round boundaries
	_, round, _, err := x.getExtraFields(header)
	if err != nil {
		return false, 0, err
	}

	epochNum := uint64(round) / x.config.Epoch
	isSwitch := uint64(round)%x.config.Epoch == 0

	return isSwitch, epochNum, nil
}

// calcMasternodes calculates masternodes for a block
func (x *XDPoS_v2) calcMasternodes(chain consensus.ChainReader, blockNum *big.Int, parentHash common.Hash, round types.Round) ([]common.Address, []common.Address, error) {
	maxMasternodes := x.config.V2.CurrentConfig.MaxMasternodes
	
	snap, err := x.getSnapshot(chain, blockNum.Uint64(), false)
	if err != nil {
		return nil, nil, err
	}
	
	candidates := snap.NextEpochCandidates
	
	// First V2 block
	if blockNum.Uint64() == x.config.V2.SwitchBlock.Uint64()+1 {
		if len(candidates) > maxMasternodes {
			candidates = candidates[:maxMasternodes]
		}
		return candidates, []common.Address{}, nil
	}

	if x.HookPenalty == nil {
		if len(candidates) > maxMasternodes {
			candidates = candidates[:maxMasternodes]
		}
		return candidates, []common.Address{}, nil
	}

	penalties, err := x.HookPenalty(chain, blockNum, parentHash, candidates)
	if err != nil {
		return nil, nil, err
	}

	masternodes := removeItemFromArray(candidates, penalties)
	if len(masternodes) > maxMasternodes {
		masternodes = masternodes[:maxMasternodes]
	}

	return masternodes, penalties, nil
}

// UpdateMasternodes updates the masternode list
func (x *XDPoS_v2) UpdateMasternodes(chain consensus.ChainReader, header *types.Header, ms []common.Address) error {
	number := header.Number.Uint64()
	if number%x.config.Epoch != x.config.Epoch-x.config.Gap {
		return fmt.Errorf("not gap block: %d", number)
	}

	snap := newSnapshot(number, header.Hash(), ms)
	log.Info("[UpdateMasternodes] take snapshot", "number", number, "hash", header.Hash())

	if err := storeSnapshot(snap, x.db); err != nil {
		return err
	}
	x.snapshots.Add(snap.Hash, snap)

	log.Info("[UpdateMasternodes] New masternodes updated", "number", snap.Number, "hash", snap.Hash)
	return nil
}

// getExtraFields extracts V2 extra fields from a header
func (x *XDPoS_v2) getExtraFields(header *types.Header) (*types.QuorumCert, types.Round, []common.Address, error) {
	var masternodes []common.Address

	// Last v1 block
	if header.Number.Cmp(x.config.V2.SwitchBlock) == 0 {
		masternodes = decodeMasternodesFromHeaderExtra(header)
		return nil, types.Round(0), masternodes, nil
	}

	// V2 block
	masternodes = x.GetMasternodesFromEpochSwitchHeader(header)
	var decodedExtra types.ExtraFields_v2
	if err := DecodeExtraFields(header.Extra, &decodedExtra); err != nil {
		log.Error("[getExtraFields] error decoding extra", "err", err, "extra", header.Extra)
		return nil, types.Round(0), masternodes, err
	}
	return decodedExtra.QuorumCert, decodedExtra.Round, masternodes, nil
}

// decodeMasternodesFromHeaderExtra extracts masternodes from V1 header extra
func decodeMasternodesFromHeaderExtra(header *types.Header) []common.Address {
	extraVanity := 32
	extraSeal := 65
	masternodes := make([]common.Address, (len(header.Extra)-extraVanity-extraSeal)/common.AddressLength)
	for i := 0; i < len(masternodes); i++ {
		copy(masternodes[i][:], header.Extra[extraVanity+i*common.AddressLength:])
	}
	return masternodes
}

// DecodeExtraFields decodes V2 extra fields
func DecodeExtraFields(extra []byte, decoded *types.ExtraFields_v2) error {
	if len(extra) < 1 {
		return errors.New("extra too short")
	}
	if extra[0] != 2 {
		return errors.New("not V2 extra format")
	}
	return rlp.DecodeBytes(extra[1:], decoded)
}

// getEpochSwitchInfo returns epoch switch information
func (x *XDPoS_v2) getEpochSwitchInfo(chain consensus.ChainReader, header *types.Header, hash common.Hash) (*types.EpochSwitchInfo, error) {
	// Check cache
	if info, ok := x.epochSwitches.Get(hash); ok {
		return info, nil
	}

	if header == nil {
		header = chain.GetHeaderByHash(hash)
		if header == nil {
			return nil, fmt.Errorf("header not found: %s", hash.Hex())
		}
	}

	// Find epoch switch block
	number := header.Number.Uint64()
	epochSwitchNum := number - number%x.config.Epoch
	if epochSwitchNum == 0 && x.config.V2.SwitchBlock.Uint64() > 0 {
		epochSwitchNum = x.config.V2.SwitchBlock.Uint64()
	}

	epochSwitchHeader := chain.GetHeaderByNumber(epochSwitchNum)
	if epochSwitchHeader == nil {
		return nil, fmt.Errorf("epoch switch header not found: %d", epochSwitchNum)
	}

	masternodes := x.GetMasternodesFromEpochSwitchHeader(epochSwitchHeader)
	
	_, round, _, _ := x.getExtraFields(epochSwitchHeader)

	info := &types.EpochSwitchInfo{
		Masternodes:    masternodes,
		MasternodesLen: len(masternodes),
		EpochSwitchBlockInfo: &types.BlockInfo{
			Hash:   epochSwitchHeader.Hash(),
			Round:  round,
			Number: epochSwitchHeader.Number,
		},
	}

	x.epochSwitches.Add(hash, info)
	return info, nil
}

// Broadcast sends a message to the BFT channel
func (x *XDPoS_v2) broadcastToBftChannel(msg interface{}) {
	go func() {
		x.BroadcastCh <- msg
	}()
}

// getSyncInfo returns current sync info
func (x *XDPoS_v2) getSyncInfo() *types.SyncInfo {
	return &types.SyncInfo{
		HighestQuorumCert:  x.highestQuorumCert,
		HighestTimeoutCert: x.highestTimeoutCert,
	}
}

// setNewRound sets a new round
func (x *XDPoS_v2) setNewRound(chain consensus.ChainReader, round types.Round) {
	log.Info("[setNewRound] new round", "round", round)
	x.currentRound = round
	x.timeoutCount = 0
	x.timeoutWorker.Reset(chain, uint64(x.currentRound), uint64(x.highestQuorumCert.ProposedBlockInfo.Round))
	x.timeoutPool.Clear()

	select {
	case x.newRoundCh <- round:
	default:
	}
}

// periodicJob runs periodic maintenance
func (x *XDPoS_v2) periodicJob() {
	go func() {
		ticker := time.NewTicker(PeriodicJobPeriod * time.Second)
		defer ticker.Stop()
		for {
			<-ticker.C
			x.hygieneVotePool()
			x.hygieneTimeoutPool()
		}
	}()
}

// allowedToSend checks if this node can send consensus messages
func (x *XDPoS_v2) allowedToSend(chain consensus.ChainReader, header *types.Header, sendType string) bool {
	x.signLock.RLock()
	signer := x.signer
	x.signLock.RUnlock()

	masternodes := x.GetMasternodes(chain, header)
	for _, mn := range masternodes {
		if signer == mn {
			log.Debug("[allowedToSend] Yes, allowed", "sendType", sendType, "signer", signer)
			return true
		}
	}
	log.Debug("[allowedToSend] Not in masternode list", "sendType", sendType, "signer", signer)
	return false
}

// GetLatestCommittedBlockInfo returns the highest committed block
func (x *XDPoS_v2) GetLatestCommittedBlockInfo() *types.BlockInfo {
	return x.highestCommitBlock
}

// FindParentBlockToAssign finds the parent block for mining
func (x *XDPoS_v2) FindParentBlockToAssign(chain consensus.ChainReader) *types.Block {
	parent := chain.GetBlock(x.highestQuorumCert.ProposedBlockInfo.Hash, x.highestQuorumCert.ProposedBlockInfo.Number.Uint64())
	if parent == nil {
		log.Error("[FindParentBlockToAssign] Parent not found",
			"hash", x.highestQuorumCert.ProposedBlockInfo.Hash,
			"number", x.highestQuorumCert.ProposedBlockInfo.Number)
	}
	return parent
}

// Utility functions

func removeItemFromArray(array []common.Address, toRemove []common.Address) []common.Address {
	result := make([]common.Address, 0)
	for _, item := range array {
		found := false
		for _, r := range toRemove {
			if item == r {
				found = true
				break
			}
		}
		if !found {
			result = append(result, item)
		}
	}
	return result
}

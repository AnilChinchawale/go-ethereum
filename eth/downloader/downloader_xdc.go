// Copyright 2024 XDC Network
// XDC pre-merge sync implementation for go-ethereum compatibility

package downloader

import (
	"errors"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/trie"
)

var (
	errUnknownPeer     = errors.New("peer is unknown or unhealthy")
	errEmptyHeaderSet  = errors.New("empty header set")
	errInvalidAncestor = errors.New("invalid ancestor")
	errPeersUnavailable = errors.New("no peers available for sync")
	errStallingPeer    = errors.New("peer is stalling")
	errTooOld          = errors.New("peer is too old")
	MaxForkAncestry    = uint64(3600 * 24 * 7 / 2) // ~1 week at 2s blocks
)

// XDCSyncEnabled indicates this build supports XDC sync
var XDCSyncEnabled atomic.Bool

func init() {
	XDCSyncEnabled.Store(true)
}

// xdcHeaderCh is used to receive headers from the legacy (non-RequestId) handler
var xdcHeaderCh = make(chan xdcHeaderResponse, 16)

type xdcHeaderResponse struct {
	peerId  string
	headers []*types.Header
}

// DeliverHeadersXDC delivers headers received from XDC peers (legacy format)
func (d *Downloader) DeliverHeadersXDC(peerId string, headers []*types.Header) {
	select {
	case xdcHeaderCh <- xdcHeaderResponse{peerId: peerId, headers: headers}:
	default:
		log.Warn("XDC header delivery channel full", "peer", peerId)
	}
}

// xdcBodyCh is used to receive bodies from the legacy (non-RequestId) handler
var xdcBodyCh = make(chan xdcBodyResponse, 16)

type xdcBodyResponse struct {
	peerId string
	txs    [][]*types.Transaction
	uncles [][]*types.Header
}

// DeliverBodiesXDC delivers bodies received from XDC peers (legacy format)
func (d *Downloader) DeliverBodiesXDC(peerId string, txs [][]*types.Transaction, uncles [][]*types.Header) {
	select {
	case xdcBodyCh <- xdcBodyResponse{peerId: peerId, txs: txs, uncles: uncles}:
	default:
		log.Warn("XDC body delivery channel full", "peer", peerId)
	}
}

// SynchroniseXDC starts a sync with the given peer (XDC pre-merge style)
func (d *Downloader) SynchroniseXDC(id string, head common.Hash, td *big.Int, mode SyncMode) error {
	err := d.synchroniseXDC(id, head, td, mode)

	switch err {
	case nil, errBusy, errCanceled:
		return err
	}

	if errors.Is(err, errInvalidChain) || errors.Is(err, errBadPeer) || errors.Is(err, errTimeout) ||
		errors.Is(err, errStallingPeer) || errors.Is(err, errEmptyHeaderSet) ||
		errors.Is(err, errPeersUnavailable) || errors.Is(err, errTooOld) || errors.Is(err, errInvalidAncestor) {
		log.Warn("XDC sync failed, dropping peer", "peer", id, "err", err)
		if d.dropPeer != nil {
			d.dropPeer(id)
		}
		return err
	}
	log.Warn("XDC sync failed, retrying", "err", err)
	return err
}

// synchroniseXDC performs the actual sync with the given peer
func (d *Downloader) synchroniseXDC(id string, hash common.Hash, td *big.Int, mode SyncMode) error {
	// Make sure only one goroutine is ever allowed past this point at once
	if !d.synchronising.CompareAndSwap(false, true) {
		return errBusy
	}
	defer d.synchronising.Store(false)

	// Post a user notification of the sync (only once per session)
	if d.notified.CompareAndSwap(false, true) {
		log.Info("XDC block synchronisation started")
	}

	// Reset the queue and peer state
	d.queue.Reset(blockCacheMaxItems, blockCacheInitialItems)
	d.peers.Reset()

	// Drain channels
	for _, ch := range []chan bool{d.queue.blockWakeCh, d.queue.receiptWakeCh} {
		select {
		case <-ch:
		default:
		}
	}
	for empty := false; !empty; {
		select {
		case <-d.headerProcCh:
		default:
			empty = true
		}
	}
	// Drain XDC header channel
	for {
		select {
		case <-xdcHeaderCh:
		default:
			goto done
		}
	}
done:

	// Create cancel channel for aborting mid-flight
	d.cancelLock.Lock()
	d.cancelCh = make(chan struct{})
	d.cancelLock.Unlock()

	defer d.Cancel()

	// Set the sync mode
	d.mode.Store(uint32(mode))
	defer d.mode.Store(0)

	// Get the peer
	peer := d.peers.Peer(id)
	if peer == nil {
		return errUnknownPeer
	}

	return d.syncWithPeerXDC(peer, hash, td)
}

// syncWithPeerXDC starts sync with a specific peer
func (d *Downloader) syncWithPeerXDC(p *peerConnection, hash common.Hash, td *big.Int) (err error) {
	d.mux.Post(StartEvent{})
	defer func() {
		if err != nil {
			d.mux.Post(FailedEvent{err})
		} else {
			latest := d.blockchain.CurrentHeader()
			d.mux.Post(DoneEvent{latest})
		}
	}()

	mode := d.getMode()
	log.Info("XDC sync: synchronising with peer", "peer", p.id, "head", hash.Hex()[:16], "td", td, "mode", mode)

	defer func(start time.Time) {
		log.Debug("XDC sync: terminated", "elapsed", time.Since(start))
	}(time.Now())

	// Fetch the peer's head header using legacy format
	latest, err := d.fetchHeightXDC(p, hash)
	if err != nil {
		return err
	}
	height := latest.Number.Uint64()
	log.Info("XDC sync: remote head identified", "number", height, "hash", latest.Hash().Hex()[:16])

	// Find common ancestor
	origin, err := d.findAncestorXDC(p, latest)
	if err != nil {
		return err
	}
	log.Info("XDC sync: common ancestor found", "number", origin)

	// For XDPoS: adjust origin to include the most recent checkpoint
	// This ensures we have masternode lists for validating subsequent blocks
	// XDC uses 900 block epochs
	const xdposEpoch = uint64(900)
	if origin > xdposEpoch {
		// Find the checkpoint at or before origin
		checkpoint := origin - (origin % xdposEpoch)
		if checkpoint > 0 && checkpoint < origin {
			log.Info("XDC sync: adjusting origin to include checkpoint", "original", origin, "checkpoint", checkpoint)
			origin = checkpoint
		}
	}

	// Update sync stats
	d.syncStatsLock.Lock()
	if d.syncStatsChainHeight <= origin || d.syncStatsChainOrigin > origin {
		d.syncStatsChainOrigin = origin
	}
	d.syncStatsChainHeight = height
	d.syncStatsLock.Unlock()

	// Calculate pivot for snap sync
	pivot := uint64(0)
	if mode == ethconfig.SnapSync {
		if height <= uint64(fsMinFullBlocks) {
			origin = 0
		} else {
			pivot = height - uint64(fsMinFullBlocks)
			if pivot <= origin {
				origin = pivot - 1
			}
		}
	}

	d.committed.Store(true)
	if mode == ethconfig.SnapSync && pivot != 0 {
		d.committed.Store(false)
	}

	// Prepare the queue
	d.queue.Prepare(origin+1, mode)

	// Run the sync fetchers
	fetchers := []func() error{
		func() error { return d.fetchHeadersXDC(p, origin+1, pivot, height) },
		func() error { return d.fetchBodiesXDC(p, origin+1) },
	}
	if mode == ethconfig.SnapSync {
		fetchers = append(fetchers, func() error { return d.fetchReceipts(origin + 1) })
	}
	fetchers = append(fetchers, func() error { return d.processHeaders(origin + 1) })

	if mode == ethconfig.SnapSync {
		fetchers = append(fetchers, d.processSnapSyncContent)
	} else {
		fetchers = append(fetchers, d.processFullSyncContent)
	}

	return d.spawnSync(fetchers)
}

// fetchHeightXDC gets the header for the given hash from the peer using legacy format
func (d *Downloader) fetchHeightXDC(p *peerConnection, hash common.Hash) (*types.Header, error) {
	log.Debug("XDC sync: fetching head header (legacy)", "hash", hash.Hex()[:16])

	// Use legacy request (no RequestId wrapper)
	if err := p.peer.RequestHeadersByHashLegacy(hash, 1, 0, false); err != nil {
		return nil, fmt.Errorf("failed to request header: %w", err)
	}

	// Wait for response on the XDC header channel
	timeout := time.NewTimer(15 * time.Second)
	defer timeout.Stop()

	for {
		select {
		case resp := <-xdcHeaderCh:
			if resp.peerId != p.id {
				// Response from different peer, put it back and continue
				select {
				case xdcHeaderCh <- resp:
				default:
				}
				continue
			}
			if len(resp.headers) != 1 {
				return nil, fmt.Errorf("expected 1 header, got %d", len(resp.headers))
			}
			return resp.headers[0], nil

		case <-timeout.C:
			return nil, errTimeout

		case <-d.cancelCh:
			return nil, errCanceled
		}
	}
}

// drainHeaderChannel removes any stale responses from the header channel
func drainHeaderChannel() {
	for {
		select {
		case <-xdcHeaderCh:
			// Discard stale response
		default:
			return
		}
	}
}

// requestHeadersByNumberXDC requests headers with timeout handling using legacy format
func (d *Downloader) requestHeadersByNumberXDC(p *peerConnection, from uint64, count, skip int, reverse bool) ([]*types.Header, error) {
	// Drain any stale responses before making new request
	drainHeaderChannel()
	
	// Use legacy request (no RequestId wrapper)
	if err := p.peer.RequestHeadersByNumberLegacy(from, count, skip, reverse); err != nil {
		return nil, fmt.Errorf("failed to request headers: %w", err)
	}

	timeout := time.NewTimer(15 * time.Second)
	defer timeout.Stop()

	for {
		select {
		case resp := <-xdcHeaderCh:
			if resp.peerId != p.id {
				// Response from different peer, put it back
				select {
				case xdcHeaderCh <- resp:
				default:
				}
				continue
			}
			// Match by first header number (most reliable)
			if len(resp.headers) > 0 {
				firstNum := resp.headers[0].Number.Uint64()
				// For binary search (count=1), check exact match
				if count == 1 && firstNum != from {
					log.Debug("XDC sync: skipping response (wrong first header)", "expected", from, "got", firstNum)
					continue
				}
				// For span search, check approximate match
				if count > 1 && len(resp.headers) != count {
					// Different count - might be from different request
					// But accept if first header matches our range
					if firstNum < from || firstNum > from+uint64(count*skip) {
						log.Debug("XDC sync: skipping response (out of range)", "from", from, "firstNum", firstNum)
						continue
					}
				}
			}
			return resp.headers, nil

		case <-timeout.C:
			return nil, errTimeout

		case <-d.cancelCh:
			return nil, errCanceled
		}
	}
}

// findAncestorXDC finds common ancestor using span search then binary search
func (d *Downloader) findAncestorXDC(p *peerConnection, remoteHeader *types.Header) (uint64, error) {
	var (
		floor        = int64(-1)
		localHeight  = d.blockchain.CurrentBlock().Number.Uint64()
		remoteHeight = remoteHeader.Number.Uint64()
	)

	log.Debug("XDC sync: finding ancestor", "local", localHeight, "remote", remoteHeight)

	if localHeight >= MaxForkAncestry {
		floor = int64(localHeight - MaxForkAncestry)
	}

	// If we're starting from scratch, ancestor is 0
	if localHeight == 0 {
		log.Info("XDC sync: starting from genesis, ancestor is 0")
		return 0, nil
	}

	// Calculate span parameters
	from, count, skip, max := calculateRequestSpanXDC(remoteHeight, localHeight)
	log.Debug("XDC sync: span search", "from", from, "count", count, "skip", skip, "max", max)

	// Request headers for span search
	headers, err := d.requestHeadersByNumberXDC(p, uint64(from), count, skip, false)
	if err != nil {
		return 0, err
	}

	if len(headers) == 0 {
		log.Warn("XDC sync: empty header set in ancestor search")
		return 0, errEmptyHeaderSet
	}

	// Find the highest known block in the response
	number, hash := uint64(0), common.Hash{}
	for i := len(headers) - 1; i >= 0; i-- {
		if headers[i].Number.Int64() < from || headers[i].Number.Uint64() > max {
			continue
		}
		h := headers[i].Hash()
		n := headers[i].Number.Uint64()

		if d.blockchain.HasBlock(h, n) {
			number, hash = n, h
			break
		}
	}

	// If we found an ancestor, return it
	if hash != (common.Hash{}) {
		if int64(number) <= floor {
			log.Warn("XDC sync: ancestor below floor", "number", number, "floor", floor)
			return 0, errInvalidAncestor
		}
		log.Info("XDC sync: found ancestor in span", "number", number)
		return number, nil
	}

	// Binary search for ancestor
	log.Debug("XDC sync: binary searching for ancestor")
	start, end := uint64(0), remoteHeight
	if floor > 0 {
		start = uint64(floor)
	}

	for start+1 < end {
		check := (start + end) / 2

		headers, err := d.requestHeadersByNumberXDC(p, check, 1, 0, false)
		if err != nil {
			return 0, err
		}
		if len(headers) != 1 {
			return 0, fmt.Errorf("expected 1 header, got %d", len(headers))
		}

		h := headers[0].Hash()
		n := headers[0].Number.Uint64()

		if d.blockchain.HasBlock(h, n) {
			start = check
			hash = h
		} else {
			end = check
		}
	}

	if int64(start) <= floor {
		return 0, errInvalidAncestor
	}

	log.Info("XDC sync: found ancestor via binary search", "number", start)
	return start, nil
}

// fetchHeadersXDC downloads headers from the peer and feeds them to the processor
func (d *Downloader) fetchHeadersXDC(p *peerConnection, from uint64, pivot uint64, targetHeight uint64) error {
	log.Info("XDC sync: downloading headers", "from", from, "pivot", pivot, "target", targetHeight)

	batchSize := MaxHeaderFetch

	for from <= targetHeight {
		select {
		case <-d.cancelCh:
			return errCanceled
		default:
		}

		count := batchSize
		if from+uint64(count) > targetHeight {
			count = int(targetHeight - from + 1)
		}

		log.Debug("XDC sync: requesting headers", "from", from, "count", count)

		headers, err := d.requestHeadersByNumberXDC(p, from, count, 0, false)
		if err != nil {
			return err
		}

		if len(headers) == 0 {
			log.Warn("XDC sync: no headers received")
			return errEmptyHeaderSet
		}

		log.Debug("XDC sync: received headers", "count", len(headers), "first", headers[0].Number, "last", headers[len(headers)-1].Number)

		// Compute hashes for the headers
		hashes := make([]common.Hash, len(headers))
		for i, h := range headers {
			hashes[i] = h.Hash()
		}

		// Feed to the header processor
		select {
		case d.headerProcCh <- &headerTask{headers: headers, hashes: hashes}:
		case <-d.cancelCh:
			return errCanceled
		}

		// Update from for next batch
		from = headers[len(headers)-1].Number.Uint64() + 1

		// Check if we've reached the target
		if headers[len(headers)-1].Number.Uint64() >= targetHeight {
			log.Info("XDC sync: header download complete", "target", targetHeight)
			break
		}

		// Small delay to avoid hammering peer
		time.Sleep(10 * time.Millisecond)
	}

	return nil
}

// fetchBodiesXDC downloads block bodies using legacy XDC format
func (d *Downloader) fetchBodiesXDC(p *peerConnection, from uint64) error {
	log.Info("XDC sync: downloading bodies", "from", from)

	idleCount := 0
	const maxIdleCount = 100 // Wait up to 5 seconds (100 * 50ms) for headers to arrive

	for {
		select {
		case <-d.cancelCh:
			return errCanceled
		default:
		}

		// Get pending headers that need bodies
		request, _, _ := d.queue.ReserveBodies(p, 128)
		if request == nil {
			// Check if we're done
			if !d.queue.InFlightBlocks() && d.queue.PendingBodies() == 0 {
				idleCount++
				if idleCount > maxIdleCount {
					log.Info("XDC sync: body download complete (no more work)")
					return nil
				}
				// Headers might still be processing, wait a bit
				time.Sleep(50 * time.Millisecond)
				continue
			}
			// Wait a bit for more work
			time.Sleep(50 * time.Millisecond)
			continue
		}
		idleCount = 0 // Reset idle counter when we have work

		// Build hash list for body request
		hashes := make([]common.Hash, len(request.Headers))
		for i, header := range request.Headers {
			hashes[i] = header.Hash()
		}

		log.Debug("XDC sync: requesting bodies", "count", len(hashes), "first", request.Headers[0].Number)

		// Use legacy body request (no RequestId)
		if err := p.peer.RequestBodiesLegacy(hashes); err != nil {
			d.queue.ExpireBodies(p.id)
			return fmt.Errorf("failed to request bodies: %w", err)
		}

		// Wait for response
		timeout := time.NewTimer(15 * time.Second)

		select {
		case resp := <-xdcBodyCh:
			timeout.Stop()
			if resp.peerId != p.id {
				// Response from different peer, put it back
				select {
				case xdcBodyCh <- resp:
				default:
				}
				continue
			}

			bodyCount := len(resp.txs)
			log.Debug("XDC sync: received bodies", "count", bodyCount)

			if bodyCount == 0 {
				log.Warn("XDC sync: empty body response")
				continue
			}

			// Ensure uncles array matches txs array length
			if len(resp.uncles) != bodyCount {
				log.Warn("XDC sync: tx/uncle count mismatch", "txs", bodyCount, "uncles", len(resp.uncles))
				continue
			}

			// Deliver bodies to the queue
			// Note: XDC doesn't have withdrawals (pre-Shanghai), so pass nil arrays
			// Hash arrays are flat - one hash per body (computed from transactions/uncles in that body)
			hasher := trie.NewStackTrie(nil)
			txHashes := make([]common.Hash, bodyCount)
			uncleHashes := make([]common.Hash, bodyCount)
			withdrawals := make([][]*types.Withdrawal, bodyCount) // All nil entries
			withdrawalHashes := make([]common.Hash, bodyCount)    // All zero hashes
			
			for i := 0; i < bodyCount; i++ {
				txHashes[i] = types.DeriveSha(types.Transactions(resp.txs[i]), hasher)
				uncleHashes[i] = types.CalcUncleHash(resp.uncles[i])
				// withdrawals[i] remains nil (no withdrawals for XDC)
				// withdrawalHashes[i] remains zero (not used when header.WithdrawalsHash is nil)
			}

			accepted, err := d.queue.DeliverBodies(p.id, resp.txs, txHashes, resp.uncles, uncleHashes, withdrawals, withdrawalHashes)
			if err != nil {
				log.Warn("XDC sync: body delivery failed", "err", err)
			} else {
				log.Debug("XDC sync: bodies delivered", "accepted", accepted)
			}

		case <-timeout.C:
			d.queue.ExpireBodies(p.id)
			log.Warn("XDC sync: body request timed out")
			// Continue trying

		case <-d.cancelCh:
			timeout.Stop()
			return errCanceled
		}

		// Small delay to avoid hammering peer
		time.Sleep(10 * time.Millisecond)
	}
}

// calculateRequestSpanXDC calculates header request parameters
func calculateRequestSpanXDC(remoteHeight, localHeight uint64) (int64, int, int, uint64) {
	var (
		from     int
		count    int
		MaxCount = MaxHeaderFetch / 16
	)

	requestHead := int(remoteHeight) - 1
	if requestHead < 0 {
		requestHead = 0
	}

	requestBottom := int(localHeight - 1)
	if requestBottom < 0 {
		requestBottom = 0
	}

	totalSpan := requestHead - requestBottom
	span := 1 + totalSpan/MaxCount
	if span < 2 {
		span = 2
	}
	if span > 16 {
		span = 16
	}

	count = 1 + totalSpan/span
	if count > MaxCount {
		count = MaxCount
	}
	if count < 2 {
		count = 2
	}

	from = requestHead - (count-1)*span
	if from < 0 {
		from = 0
	}

	max := from + (count-1)*span
	return int64(from), count, span - 1, uint64(max)
}

// XDCHeaderDeliveryLock protects concurrent header delivery
var XDCHeaderDeliveryLock sync.Mutex

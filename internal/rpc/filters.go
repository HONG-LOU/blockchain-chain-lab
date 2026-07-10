package rpc

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	MaxInstalledFilters          = 1024
	MaxBlockFilterChanges        = 1000
	MaxPendingTransactionFilters = 64
	MaxPendingFilterSeenEntries  = 65_536
	FilterTTL                    = 5 * time.Minute
	filterIDEntropyBytes         = 16
	filterIDGenerationAttempts   = 8
	maxFilterIDBytes             = 2 + filterIDEntropyBytes*2
)

type logFilterStore struct {
	mu                  sync.Mutex
	filters             map[string]storedLogFilter
	blockFilters        map[string]storedBlockFilter
	pendingFilters      map[string]storedPendingFilter
	pendingSeenEntries  int
	pendingReservations int
	maxPendingFilters   int
	maxPendingSeen      int
	now                 func() time.Time
	ttl                 time.Duration
	expiryTimer         *time.Timer
	expiryDeadline      time.Time
	timerGeneration     uint64
	stopped             bool
}

type storedLogFilter struct {
	filter    logFilter
	nextBlock uint64
	expiresAt time.Time
}

type storedBlockFilter struct {
	nextBlock uint64
	expiresAt time.Time
}

type storedPendingFilter struct {
	seen      map[string]struct{}
	expiresAt time.Time
	updating  bool
}

func newLogFilterStore() *logFilterStore {
	return &logFilterStore{
		filters:           make(map[string]storedLogFilter),
		blockFilters:      make(map[string]storedBlockFilter),
		pendingFilters:    make(map[string]storedPendingFilter),
		maxPendingFilters: MaxPendingTransactionFilters,
		maxPendingSeen:    MaxPendingFilterSeenEntries,
		now:               time.Now,
		ttl:               FilterTTL,
	}
}

func (s *logFilterStore) allocateIDLocked(now time.Time) (string, error) {
	if s.stopped {
		return "", fmt.Errorf("filter store is stopped")
	}
	s.pruneExpiredLocked(now)
	if len(s.filters)+len(s.blockFilters)+len(s.pendingFilters)+s.pendingReservations >= MaxInstalledFilters {
		return "", fmt.Errorf("installed filter limit %d reached", MaxInstalledFilters)
	}
	for range filterIDGenerationAttempts {
		id, err := randomFilterID()
		if err != nil {
			return "", err
		}
		if !s.filterIDExistsLocked(id) {
			return id, nil
		}
	}
	return "", fmt.Errorf("could not allocate a unique filter id")
}

func randomFilterID() (string, error) {
	raw := make([]byte, filterIDEntropyBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate filter id: %w", err)
	}
	encoded := strings.TrimLeft(hex.EncodeToString(raw), "0")
	if encoded == "" {
		encoded = "0"
	}
	return "0x" + encoded, nil
}

func (s *logFilterStore) filterIDExistsLocked(id string) bool {
	if _, exists := s.filters[id]; exists {
		return true
	}
	if _, exists := s.blockFilters[id]; exists {
		return true
	}
	_, exists := s.pendingFilters[id]
	return exists
}

func (s *logFilterStore) pruneExpiredLocked(now time.Time) {
	for id, stored := range s.filters {
		if !stored.expiresAt.After(now) {
			delete(s.filters, id)
		}
	}
	for id, stored := range s.blockFilters {
		if !stored.expiresAt.After(now) {
			delete(s.blockFilters, id)
		}
	}
	for id, stored := range s.pendingFilters {
		if !stored.expiresAt.After(now) {
			s.removePendingFilterLocked(id)
		}
	}
}

func (s *logFilterStore) removePendingFilterLocked(id string) bool {
	stored, exists := s.pendingFilters[id]
	if !exists {
		return false
	}
	delete(s.pendingFilters, id)
	if len(stored.seen) <= s.pendingSeenEntries {
		s.pendingSeenEntries -= len(stored.seen)
	} else {
		s.rebuildPendingSeenEntriesLocked()
	}
	return true
}

func (s *logFilterStore) rebuildPendingSeenEntriesLocked() {
	total := 0
	for _, stored := range s.pendingFilters {
		total += len(stored.seen)
	}
	s.pendingSeenEntries = total
}

func (s *logFilterStore) pendingFilterLimitLocked() int {
	if s.maxPendingFilters <= 0 {
		return MaxPendingTransactionFilters
	}
	return s.maxPendingFilters
}

func (s *logFilterStore) pendingSeenLimitLocked() int {
	if s.maxPendingSeen <= 0 {
		return MaxPendingFilterSeenEntries
	}
	return s.maxPendingSeen
}

func (s *logFilterStore) expirationLocked(now time.Time) time.Time {
	ttl := s.ttl
	if ttl <= 0 {
		ttl = FilterTTL
	}
	return now.Add(ttl)
}

func (s *logFilterStore) scheduleExpiryLocked(now time.Time) {
	var earliest time.Time
	if !s.stopped {
		for _, stored := range s.filters {
			if earliest.IsZero() || stored.expiresAt.Before(earliest) {
				earliest = stored.expiresAt
			}
		}
		for _, stored := range s.blockFilters {
			if earliest.IsZero() || stored.expiresAt.Before(earliest) {
				earliest = stored.expiresAt
			}
		}
		for _, stored := range s.pendingFilters {
			if earliest.IsZero() || stored.expiresAt.Before(earliest) {
				earliest = stored.expiresAt
			}
		}
	}
	if s.expiryTimer != nil && earliest.Equal(s.expiryDeadline) {
		return
	}
	if s.expiryTimer != nil {
		s.expiryTimer.Stop()
		s.expiryTimer = nil
	}
	s.expiryDeadline = time.Time{}
	s.timerGeneration++
	if earliest.IsZero() {
		return
	}

	delay := earliest.Sub(now)
	if delay < 0 {
		delay = 0
	}
	generation := s.timerGeneration
	s.expiryDeadline = earliest
	s.expiryTimer = time.AfterFunc(delay, func() {
		s.expire(generation)
	})
}

func (s *logFilterStore) expire(generation uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || generation != s.timerGeneration {
		return
	}
	s.expiryTimer = nil
	s.expiryDeadline = time.Time{}
	now := s.now()
	s.pruneExpiredLocked(now)
	s.scheduleExpiryLocked(now)
}

// stop is used by owners that need deterministic teardown. Normal stores do
// not keep a resident goroutine: the one-shot timer callback exits after each
// prune and is not rescheduled once the store is empty.
func (s *logFilterStore) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	s.timerGeneration++
	if s.expiryTimer != nil {
		s.expiryTimer.Stop()
		s.expiryTimer = nil
	}
	s.expiryDeadline = time.Time{}
	clear(s.filters)
	clear(s.blockFilters)
	clear(s.pendingFilters)
	s.pendingSeenEntries = 0
}

func (s *Server) registerLogFilter(filter logFilter, nextBlock uint64) (string, error) {
	if err := validateLogFilterDefinition(filter); err != nil {
		return "", err
	}
	s.filters.mu.Lock()
	defer s.filters.mu.Unlock()
	now := s.filters.now()
	id, err := s.filters.allocateIDLocked(now)
	if err != nil {
		return "", err
	}
	s.filters.filters[id] = storedLogFilter{filter: filter, nextBlock: nextBlock, expiresAt: s.filters.expirationLocked(now)}
	s.filters.scheduleExpiryLocked(now)
	return id, nil
}

func (s *Server) registerBlockFilter(nextBlock uint64) (string, error) {
	s.filters.mu.Lock()
	defer s.filters.mu.Unlock()
	now := s.filters.now()
	id, err := s.filters.allocateIDLocked(now)
	if err != nil {
		return "", err
	}
	s.filters.blockFilters[id] = storedBlockFilter{nextBlock: nextBlock, expiresAt: s.filters.expirationLocked(now)}
	s.filters.scheduleExpiryLocked(now)
	return id, nil
}

func (s *Server) registerPendingTransactionFilter() (string, error) {
	s.filters.mu.Lock()
	now := s.filters.now()
	s.filters.pruneExpiredLocked(now)
	if s.filters.stopped {
		s.filters.scheduleExpiryLocked(now)
		s.filters.mu.Unlock()
		return "", fmt.Errorf("filter store is stopped")
	}
	if len(s.filters.filters)+len(s.filters.blockFilters)+len(s.filters.pendingFilters)+s.filters.pendingReservations >= MaxInstalledFilters {
		s.filters.scheduleExpiryLocked(now)
		s.filters.mu.Unlock()
		return "", fmt.Errorf("installed filter limit %d reached", MaxInstalledFilters)
	}
	pendingFilterLimit := s.filters.pendingFilterLimitLocked()
	if len(s.filters.pendingFilters)+s.filters.pendingReservations >= pendingFilterLimit {
		s.filters.scheduleExpiryLocked(now)
		s.filters.mu.Unlock()
		return "", fmt.Errorf("pending transaction filter limit %d reached", pendingFilterLimit)
	}
	s.filters.pendingReservations++
	s.filters.scheduleExpiryLocked(now)
	s.filters.mu.Unlock()

	seen := make(map[string]struct{})
	for _, hashValue := range s.pendingTransactionHashes() {
		seen[strings.ToLower(hashValue)] = struct{}{}
	}

	s.filters.mu.Lock()
	defer s.filters.mu.Unlock()
	if s.filters.pendingReservations == 0 {
		return "", fmt.Errorf("pending filter reservation accounting is inconsistent")
	}
	s.filters.pendingReservations--
	now = s.filters.now()
	s.filters.pruneExpiredLocked(now)
	if s.filters.stopped {
		s.filters.scheduleExpiryLocked(now)
		return "", fmt.Errorf("filter store is stopped")
	}
	id, err := s.filters.allocateIDLocked(now)
	if err != nil {
		s.filters.scheduleExpiryLocked(now)
		return "", err
	}
	pendingFilterLimit = s.filters.pendingFilterLimitLocked()
	if len(s.filters.pendingFilters)+s.filters.pendingReservations >= pendingFilterLimit {
		s.filters.scheduleExpiryLocked(now)
		return "", fmt.Errorf("pending transaction filter limit %d reached", pendingFilterLimit)
	}
	pendingSeenLimit := s.filters.pendingSeenLimitLocked()
	if s.filters.pendingSeenEntries > pendingSeenLimit || len(seen) > pendingSeenLimit-s.filters.pendingSeenEntries {
		s.filters.scheduleExpiryLocked(now)
		return "", fmt.Errorf("pending filter seen-entry limit %d reached", pendingSeenLimit)
	}
	s.filters.pendingFilters[id] = storedPendingFilter{seen: seen, expiresAt: s.filters.expirationLocked(now)}
	s.filters.pendingSeenEntries += len(seen)
	s.filters.scheduleExpiryLocked(now)
	return id, nil
}

func (s *Server) pendingTransactionHashes() []string {
	s.pendingHashMu.Lock()
	defer s.pendingHashMu.Unlock()
	if s.pendingHashes == nil && s.node != nil {
		revision := s.node.TxPoolRevision()
		if s.pendingHashValid && revision == s.pendingHashRevision {
			return append([]string(nil), s.pendingHashSnapshot...)
		}
		revision, hashes := s.node.PendingTransactionHashesSnapshot()
		s.pendingHashSnapshot = append(s.pendingHashSnapshot[:0], hashes...)
		s.pendingHashRevision = revision
		s.pendingHashValid = true
		return append([]string(nil), s.pendingHashSnapshot...)
	}
	if s.pendingHashValid {
		return append([]string(nil), s.pendingHashSnapshot...)
	}

	var hashes []string
	if s.pendingHashes != nil {
		hashes = s.pendingHashes()
	}
	s.pendingHashSnapshot = append(s.pendingHashSnapshot[:0], hashes...)
	s.pendingHashValid = true
	return append([]string(nil), s.pendingHashSnapshot...)
}

func (s *Server) invalidatePendingTransactionHashes() {
	s.pendingHashMu.Lock()
	s.pendingHashSnapshot = nil
	s.pendingHashValid = false
	s.pendingHashMu.Unlock()
}

func (s *Server) logFilterLogs(id string, responseByteLimit int) ([]map[string]any, bool, error) {
	latest := s.node.Finality().HeadHeight
	s.filters.mu.Lock()
	now := s.filters.now()
	s.filters.pruneExpiredLocked(now)
	stored, ok := s.filters.filters[id]
	if ok {
		stored.expiresAt = s.filters.expirationLocked(now)
		s.filters.filters[id] = stored
	}
	s.filters.scheduleExpiryLocked(now)
	s.filters.mu.Unlock()
	if !ok {
		return nil, false, nil
	}
	logs, err := evmLogsWithByteLimit(s.node, stored.filter.resolvedToBlock(latest), responseByteLimit)
	return logs, true, err
}

func (s *Server) filterChanges(id string, responseByteLimit int) (any, bool, error) {
	if logs, ok, err := s.logFilterChanges(id, responseByteLimit); ok {
		return logs, true, err
	}
	if blocks, ok := s.blockFilterChanges(id); ok {
		return blocks, true, nil
	}
	pending, ok, err := s.pendingTransactionFilterChanges(id)
	return pending, ok, err
}

func (s *Server) logFilterChanges(id string, responseByteLimit int) ([]map[string]any, bool, error) {
	latest := s.node.Finality().HeadHeight
	return s.logFilterChangesAt(id, latest, responseByteLimit)
}

func (s *Server) logFilterChangesAt(id string, latest uint64, responseByteLimit int) ([]map[string]any, bool, error) {
	s.filters.mu.Lock()
	defer s.filters.mu.Unlock()
	now := s.filters.now()
	s.filters.pruneExpiredLocked(now)
	stored, ok := s.filters.filters[id]
	if !ok {
		s.filters.scheduleExpiryLocked(now)
		return nil, false, nil
	}
	stored.expiresAt = s.filters.expirationLocked(now)
	s.filters.filters[id] = stored
	s.filters.scheduleExpiryLocked(now)
	filter := stored.filter.resolvedToBlock(latest)
	if filter.toBlock > latest {
		filter.toBlock = latest
	}
	filter.fromBlock = stored.nextBlock
	if filter.toBlock < filter.fromBlock {
		s.filters.filters[id] = stored
		return []map[string]any{}, true, nil
	}
	logs, err := evmLogsWithByteLimit(s.node, filter, responseByteLimit)
	if err != nil {
		s.filters.filters[id] = stored
		return nil, true, err
	}
	stored.nextBlock = filter.toBlock + 1
	s.filters.filters[id] = stored
	return logs, true, nil
}

func (s *Server) blockFilterChanges(id string) ([]string, bool) {
	latest := s.node.Finality().HeadHeight
	s.filters.mu.Lock()
	now := s.filters.now()
	s.filters.pruneExpiredLocked(now)
	stored, ok := s.filters.blockFilters[id]
	if !ok {
		s.filters.scheduleExpiryLocked(now)
		s.filters.mu.Unlock()
		return nil, false
	}
	stored.expiresAt = s.filters.expirationLocked(now)
	s.filters.blockFilters[id] = stored
	s.filters.scheduleExpiryLocked(now)
	if stored.nextBlock > latest {
		s.filters.blockFilters[id] = stored
		s.filters.mu.Unlock()
		return []string{}, true
	}
	last := latest
	if latest-stored.nextBlock+1 > MaxBlockFilterChanges {
		last = stored.nextBlock + MaxBlockFilterChanges - 1
	}
	nextBlock := stored.nextBlock
	stored.nextBlock = last + 1
	s.filters.blockFilters[id] = stored
	s.filters.mu.Unlock()

	hashes := make([]string, 0, last-nextBlock+1)
	for height := nextBlock; height <= last; height++ {
		block, ok := s.node.Block(height)
		if ok {
			hashes = append(hashes, block.Hash())
		}
	}
	return hashes, true
}

func (s *Server) pendingTransactionFilterChanges(id string) ([]string, bool, error) {
	s.filters.mu.Lock()
	now := s.filters.now()
	s.filters.pruneExpiredLocked(now)
	stored, ok := s.filters.pendingFilters[id]
	if !ok {
		s.filters.scheduleExpiryLocked(now)
		s.filters.mu.Unlock()
		return nil, false, nil
	}
	if stored.updating {
		stored.expiresAt = s.filters.expirationLocked(now)
		s.filters.pendingFilters[id] = stored
		s.filters.scheduleExpiryLocked(now)
		s.filters.mu.Unlock()
		return nil, true, fmt.Errorf("pending filter update is already in progress")
	}
	stored.updating = true
	stored.expiresAt = s.filters.expirationLocked(now)
	s.filters.pendingFilters[id] = stored
	s.filters.scheduleExpiryLocked(now)
	s.filters.mu.Unlock()

	current := s.pendingTransactionHashes()
	nextSeen := make(map[string]struct{}, len(current))
	for _, hashValue := range current {
		nextSeen[strings.ToLower(hashValue)] = struct{}{}
	}

	s.filters.mu.Lock()
	defer s.filters.mu.Unlock()
	now = s.filters.now()
	s.filters.pruneExpiredLocked(now)
	stored, ok = s.filters.pendingFilters[id]
	if !ok || !stored.updating {
		s.filters.scheduleExpiryLocked(now)
		return nil, false, nil
	}
	if s.filters.pendingSeenEntries < len(stored.seen) {
		s.filters.rebuildPendingSeenEntriesLocked()
	}
	if s.filters.pendingSeenEntries < len(stored.seen) {
		stored.updating = false
		stored.expiresAt = s.filters.expirationLocked(now)
		s.filters.pendingFilters[id] = stored
		s.filters.scheduleExpiryLocked(now)
		return nil, true, fmt.Errorf("pending filter seen-entry accounting is inconsistent")
	}
	retainedEntries := s.filters.pendingSeenEntries - len(stored.seen)
	pendingSeenLimit := s.filters.pendingSeenLimitLocked()
	if retainedEntries > pendingSeenLimit || len(nextSeen) > pendingSeenLimit-retainedEntries {
		stored.updating = false
		stored.expiresAt = s.filters.expirationLocked(now)
		s.filters.pendingFilters[id] = stored
		s.filters.scheduleExpiryLocked(now)
		return nil, true, fmt.Errorf("pending filter seen-entry limit %d reached", pendingSeenLimit)
	}
	hashes := make([]string, 0)
	for _, hashValue := range current {
		key := strings.ToLower(hashValue)
		if _, exists := stored.seen[key]; !exists {
			hashes = append(hashes, hashValue)
		}
	}
	stored.seen = nextSeen
	stored.updating = false
	stored.expiresAt = s.filters.expirationLocked(now)
	s.filters.pendingFilters[id] = stored
	s.filters.pendingSeenEntries = retainedEntries + len(nextSeen)
	s.filters.scheduleExpiryLocked(now)
	return hashes, true, nil
}

func (s *Server) uninstallFilter(id string) bool {
	s.filters.mu.Lock()
	defer s.filters.mu.Unlock()
	now := s.filters.now()
	s.filters.pruneExpiredLocked(now)
	_, logOK := s.filters.filters[id]
	_, blockOK := s.filters.blockFilters[id]
	_, pendingOK := s.filters.pendingFilters[id]
	if !logOK && !blockOK && !pendingOK {
		s.filters.scheduleExpiryLocked(now)
		return false
	}
	delete(s.filters.filters, id)
	delete(s.filters.blockFilters, id)
	s.filters.removePendingFilterLocked(id)
	s.filters.scheduleExpiryLocked(now)
	return true
}

func filterIDParam(params []any) (string, error) {
	if len(params) != 1 {
		return "", fmt.Errorf("filter id is required")
	}
	id, ok := params[0].(string)
	if !ok {
		return "", fmt.Errorf("filter id must be a string")
	}
	if !canonicalFilterID(id) {
		return "", fmt.Errorf("filter id must be a canonical 0x-prefixed lowercase quantity")
	}
	return id, nil
}

func canonicalFilterID(id string) bool {
	if len(id) < 3 || len(id) > maxFilterIDBytes || !strings.HasPrefix(id, "0x") {
		return false
	}
	digits := id[2:]
	if len(digits) > 1 && digits[0] == '0' {
		return false
	}
	for _, digit := range []byte(digits) {
		if (digit < '0' || digit > '9') && (digit < 'a' || digit > 'f') {
			return false
		}
	}
	return true
}

package rpc

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFilterIDsAreRandomAndNotSequentiallyGuessable(t *testing.T) {
	store := newLogFilterStore()
	defer store.stop()
	server := &Server{filters: store}
	seen := make(map[string]struct{})
	for range 64 {
		id, err := server.registerBlockFilter(0)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(id, "0x") || len(id) < 3 || len(id) > 2+filterIDEntropyBytes*2 || (len(id) > 3 && id[2] == '0') {
			t.Fatalf("filter id is not a bounded random hex quantity: %q", id)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("duplicate filter id %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestPendingFilterCountAndSeenEntryBudgets(t *testing.T) {
	n := newResourceLimitTestNode(t, 0)
	store := newLogFilterStore()
	store.maxPendingFilters = 2
	store.maxPendingSeen = 1
	defer store.stop()
	server := &Server{node: n, filters: store}

	firstID, err := server.registerPendingTransactionFilter()
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := server.registerPendingTransactionFilter()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.registerPendingTransactionFilter(); err == nil || !strings.Contains(err.Error(), "filter limit 2") {
		t.Fatalf("pending filter count error = %v", err)
	}

	if _, err := n.RequestFaucet("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 1); err != nil {
		t.Fatal(err)
	}
	server.invalidatePendingTransactionHashes()
	changes, ok, err := server.pendingTransactionFilterChanges(firstID)
	if err != nil || !ok || len(changes) != 1 {
		t.Fatalf("first pending changes = %#v, %v, %v", changes, ok, err)
	}
	if _, _, err := server.pendingTransactionFilterChanges(secondID); err == nil || !strings.Contains(err.Error(), "seen-entry limit 1") {
		t.Fatalf("pending seen-entry update error = %v", err)
	}
	store.mu.Lock()
	seenEntries := store.pendingSeenEntries
	secondSeen := len(store.pendingFilters[secondID].seen)
	store.mu.Unlock()
	if seenEntries != 1 || secondSeen != 0 {
		t.Fatalf("pending seen accounting = %d, second filter entries = %d", seenEntries, secondSeen)
	}
	if !server.uninstallFilter(firstID) {
		t.Fatal("first pending filter was not uninstalled")
	}
	store.mu.Lock()
	seenEntries = store.pendingSeenEntries
	store.mu.Unlock()
	if seenEntries != 0 {
		t.Fatalf("pending seen entries after uninstall = %d", seenEntries)
	}
}

func TestPendingFilterCapacityAndUnknownIDRejectBeforePoolSnapshot(t *testing.T) {
	store := newLogFilterStore()
	store.maxPendingFilters = 1
	defer store.stop()
	var calls atomic.Int32
	server := &Server{
		filters: store,
		pendingHashes: func() []string {
			calls.Add(1)
			return nil
		},
	}

	if _, err := server.registerPendingTransactionFilter(); err != nil {
		t.Fatal(err)
	}
	calls.Store(0)
	if _, err := server.registerPendingTransactionFilter(); err == nil || !strings.Contains(err.Error(), "filter limit 1") {
		t.Fatalf("pending filter capacity error = %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("capacity rejection took %d pending snapshots", got)
	}

	if _, ok, err := server.pendingTransactionFilterChanges("0x1"); err != nil || ok {
		t.Fatalf("unknown pending filter = ok %v, err %v", ok, err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("unknown filter rejection took %d pending snapshots", got)
	}
}

func TestPendingFilterRegistrationReservationPreventsConcurrentSnapshotAmplification(t *testing.T) {
	store := newLogFilterStore()
	store.maxPendingFilters = 1
	defer store.stop()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	server := &Server{
		filters: store,
		pendingHashes: func() []string {
			if calls.Add(1) == 1 {
				close(started)
				<-release
			}
			return nil
		},
	}
	result := make(chan error, 1)
	go func() {
		_, err := server.registerPendingTransactionFilter()
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first pending snapshot did not start")
	}
	if _, err := server.registerPendingTransactionFilter(); err == nil || !strings.Contains(err.Error(), "filter limit 1") {
		t.Fatalf("concurrent pending registration error = %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("concurrent registration took %d pending snapshots", got)
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestPendingFiltersShareOneSnapshotAcrossConcurrentPolls(t *testing.T) {
	store := newLogFilterStore()
	defer store.stop()
	var calls atomic.Int32
	server := &Server{
		filters: store,
		pendingHashes: func() []string {
			calls.Add(1)
			return []string{fmt.Sprintf("0x%064x", 1)}
		},
	}
	ids := make([]string, 16)
	for index := range ids {
		id, err := server.registerPendingTransactionFilter()
		if err != nil {
			t.Fatal(err)
		}
		ids[index] = id
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("registrations took %d pending snapshots", got)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	calls.Store(0)
	server.pendingHashes = func() []string {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return []string{fmt.Sprintf("0x%064x", 1), fmt.Sprintf("0x%064x", 2)}
	}
	server.invalidatePendingTransactionHashes()
	var wait sync.WaitGroup
	errorsByFilter := make(chan error, len(ids))
	for _, id := range ids {
		wait.Add(1)
		go func() {
			defer wait.Done()
			changes, ok, err := server.pendingTransactionFilterChanges(id)
			if err == nil && (!ok || len(changes) != 1) {
				err = fmt.Errorf("changes = %#v, ok %v", changes, ok)
			}
			errorsByFilter <- err
		}()
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("pending snapshot did not start")
	}
	close(release)
	wait.Wait()
	close(errorsByFilter)
	for err := range errorsByFilter {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("concurrent polls took %d pending snapshots", got)
	}
}

func TestFilterIDParamRequiresCanonicalBoundedQuantity(t *testing.T) {
	for _, id := range []string{"0x0", "0x1", "0xabcdef", "0x1234567890abcdef1234567890abcdef"} {
		got, err := filterIDParam([]any{id})
		if err != nil || got != id {
			t.Fatalf("valid filter id %q = %q, %v", id, got, err)
		}
	}
	for _, params := range [][]any{
		{},
		{"0x1", "0x2"},
		{1},
		{""},
		{" 0x1"},
		{"0x1 "},
		{"0X1"},
		{"0xA"},
		{"0x01"},
		{"0xg"},
		{"0x1234567890abcdef1234567890abcdef0"},
	} {
		if _, err := filterIDParam(params); err == nil {
			t.Fatalf("non-canonical filter params accepted: %#v", params)
		}
	}
}

func TestPendingFilterExpiryReleasesSeenEntryBudget(t *testing.T) {
	n := newResourceLimitTestNode(t, 0)
	if _, err := n.RequestFaucet("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 1); err != nil {
		t.Fatal(err)
	}
	store := newLogFilterStore()
	store.ttl = 20 * time.Millisecond
	defer store.stop()
	server := &Server{node: n, filters: store}
	id, err := server.registerPendingTransactionFilter()
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	initialEntries := store.pendingSeenEntries
	store.mu.Unlock()
	if initialEntries != 1 {
		t.Fatalf("initial pending seen entries = %d", initialEntries)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		store.mu.Lock()
		_, exists := store.pendingFilters[id]
		entries := store.pendingSeenEntries
		store.mu.Unlock()
		if !exists {
			if entries != 0 {
				t.Fatalf("pending seen entries after expiry = %d", entries)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pending filter did not expire")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestLogFilterRejectsNonCanonicalAddressesAndTopics(t *testing.T) {
	canonicalAddress := "0x" + strings.Repeat("ab", 20)
	canonicalTopic := "0x" + strings.Repeat("cd", 32)
	tests := []struct {
		name  string
		value map[string]any
		want  string
	}{
		{name: "short address", value: map[string]any{"address": "0x" + strings.Repeat("ab", 19)}, want: "canonical 20-byte"},
		{name: "long address", value: map[string]any{"address": "0x" + strings.Repeat("ab", 21)}, want: "canonical 20-byte"},
		{name: "uppercase address", value: map[string]any{"address": strings.ToUpper(canonicalAddress)}, want: "canonical 20-byte"},
		{name: "invalid address hex", value: map[string]any{"address": "0x" + strings.Repeat("gg", 20)}, want: "canonical 20-byte"},
		{name: "short topic", value: map[string]any{"topics": []any{"0x" + strings.Repeat("cd", 31)}}, want: "canonical 32-byte"},
		{name: "long topic", value: map[string]any{"topics": []any{"0x" + strings.Repeat("cd", 33)}}, want: "canonical 32-byte"},
		{name: "uppercase topic", value: map[string]any{"topics": []any{strings.ToUpper(canonicalTopic)}}, want: "canonical 32-byte"},
		{name: "invalid topic hex", value: map[string]any{"topics": []any{"0x" + strings.Repeat("gg", 32)}}, want: "canonical 32-byte"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseLogFilter(test.value, blockTags{}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parse error = %v, want %q", err, test.want)
			}
		})
	}

	filter, err := parseLogFilter(map[string]any{
		"address": []any{canonicalAddress},
		"topics":  []any{canonicalTopic, nil},
	}, blockTags{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := filter.addresses[canonicalAddress]; !ok {
		t.Fatalf("canonical address missing from filter: %#v", filter.addresses)
	}
	if _, ok := filter.topics[0].allowed[canonicalTopic]; !ok || !filter.topics[1].any {
		t.Fatalf("canonical topics missing from filter: %#v", filter.topics)
	}
}

func TestLogFilterDefinitionLimitsAreEnforcedBeforeRegistration(t *testing.T) {
	addresses := make([]any, MaxLogAddresses)
	for index := range addresses {
		addresses[index] = fmt.Sprintf("0x%040x", index+1)
	}
	alternatives := func(offset int) []any {
		values := make([]any, MaxLogTopicAlternatives)
		for index := range values {
			values[index] = fmt.Sprintf("0x%064x", offset+index+1)
		}
		return values
	}

	withinLimit, err := parseLogFilter(map[string]any{
		"address": addresses,
		"topics":  []any{alternatives(0)},
	}, blockTags{})
	if err != nil {
		t.Fatalf("individually maximal address/topic filter should fit total byte budget: %v", err)
	}
	store := newLogFilterStore()
	defer store.stop()
	server := &Server{filters: store}
	if _, err := server.registerLogFilter(withinLimit, 0); err != nil {
		t.Fatalf("register bounded filter: %v", err)
	}

	overLimitValue := map[string]any{
		"address": addresses,
		"topics":  []any{alternatives(0), alternatives(MaxLogTopicAlternatives)},
	}
	if _, err := parseLogFilter(overLimitValue, blockTags{}); err == nil || !strings.Contains(err.Error(), "definition exceeds") {
		t.Fatalf("oversized filter parse error = %v", err)
	}

	overLimitFilter := logFilter{
		addresses: withinLimit.addresses,
		topics: []topicCriterion{
			withinLimit.topics[0],
			{allowed: canonicalTopicSet(MaxLogTopicAlternatives, MaxLogTopicAlternatives)},
		},
	}
	if _, err := server.registerLogFilter(overLimitFilter, 0); err == nil || !strings.Contains(err.Error(), "definition exceeds") {
		t.Fatalf("oversized filter registration error = %v", err)
	}

	tooManyAddresses := append(append([]any(nil), addresses...), fmt.Sprintf("0x%040x", MaxLogAddresses+1))
	if _, err := parseLogFilter(map[string]any{"address": tooManyAddresses}, blockTags{}); err == nil || !strings.Contains(err.Error(), "address filter exceeds") {
		t.Fatalf("address count error = %v", err)
	}
	tooManyPositions := []any{nil, nil, nil, nil, nil}
	if _, err := parseLogFilter(map[string]any{"topics": tooManyPositions}, blockTags{}); err == nil || !strings.Contains(err.Error(), "topics filter exceeds") {
		t.Fatalf("topic position count error = %v", err)
	}
	tooManyAlternatives := append(alternatives(0), fmt.Sprintf("0x%064x", MaxLogTopicAlternatives+1))
	if _, err := parseLogFilter(map[string]any{"topics": []any{tooManyAlternatives}}, blockTags{}); err == nil || !strings.Contains(err.Error(), "topic filter position exceeds") {
		t.Fatalf("topic alternative count error = %v", err)
	}
}

func TestFilterTTLActivelyReclaimsWithoutAnotherFilterOperation(t *testing.T) {
	store := newLogFilterStore()
	store.ttl = 20 * time.Millisecond
	defer store.stop()
	server := &Server{filters: store}
	id, err := server.registerBlockFilter(0)
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		store.mu.Lock()
		_, exists := store.blockFilters[id]
		timerActive := store.expiryTimer != nil
		store.mu.Unlock()
		if !exists {
			if timerActive {
				t.Fatal("empty filter store retained an expiry timer")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("filter was not actively reclaimed after TTL")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestFilterStoreStopCancelsTimerAndRejectsRegistration(t *testing.T) {
	store := newLogFilterStore()
	server := &Server{filters: store}
	if _, err := server.registerBlockFilter(0); err != nil {
		t.Fatal(err)
	}
	store.stop()

	store.mu.Lock()
	timer := store.expiryTimer
	filterCount := len(store.filters) + len(store.blockFilters) + len(store.pendingFilters)
	store.mu.Unlock()
	if timer != nil || filterCount != 0 {
		t.Fatalf("stopped store retained timer or filters: timer=%v count=%d", timer, filterCount)
	}
	if _, err := server.registerBlockFilter(0); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("registration after stop error = %v", err)
	}
}

func canonicalTopicSet(count int, offset int) map[string]struct{} {
	values := make(map[string]struct{}, count)
	for index := range count {
		values[fmt.Sprintf("0x%064x", offset+index+1)] = struct{}{}
	}
	return values
}

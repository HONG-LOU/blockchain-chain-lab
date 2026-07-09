package rpc

import (
	"fmt"
	"strings"
	"sync"
)

type logFilterStore struct {
	mu      sync.Mutex
	next    uint64
	filters map[string]storedLogFilter
}

type storedLogFilter struct {
	filter    logFilter
	nextBlock uint64
}

func newLogFilterStore() *logFilterStore {
	return &logFilterStore{filters: make(map[string]storedLogFilter)}
}

func (s *Server) registerLogFilter(filter logFilter, nextBlock uint64) string {
	s.filters.mu.Lock()
	defer s.filters.mu.Unlock()

	s.filters.next++
	id := quantity(s.filters.next)
	s.filters.filters[id] = storedLogFilter{filter: filter, nextBlock: nextBlock}
	return id
}

func (s *Server) logFilterLogs(id string) ([]map[string]any, bool) {
	latest := s.node.Finality().HeadHeight
	s.filters.mu.Lock()
	stored, ok := s.filters.filters[id]
	s.filters.mu.Unlock()
	if !ok {
		return nil, false
	}
	return evmLogs(s.node, stored.filter.resolvedToBlock(latest)), true
}

func (s *Server) logFilterChanges(id string) ([]map[string]any, bool) {
	latest := s.node.Finality().HeadHeight
	s.filters.mu.Lock()
	stored, ok := s.filters.filters[id]
	if !ok {
		s.filters.mu.Unlock()
		return nil, false
	}
	filter := stored.filter.resolvedToBlock(latest)
	if filter.toBlock > latest {
		filter.toBlock = latest
	}
	filter.fromBlock = stored.nextBlock
	if filter.toBlock < filter.fromBlock {
		s.filters.mu.Unlock()
		return []map[string]any{}, true
	}
	stored.nextBlock = filter.toBlock + 1
	s.filters.filters[id] = stored
	s.filters.mu.Unlock()
	return evmLogs(s.node, filter), true
}

func (s *Server) uninstallLogFilter(id string) bool {
	s.filters.mu.Lock()
	defer s.filters.mu.Unlock()

	if _, ok := s.filters.filters[id]; !ok {
		return false
	}
	delete(s.filters.filters, id)
	return true
}

func filterIDParam(params []any) (string, error) {
	if len(params) < 1 {
		return "", fmt.Errorf("filter id is required")
	}
	id, ok := params[0].(string)
	if !ok || strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("filter id must be a string")
	}
	return strings.ToLower(strings.TrimSpace(id)), nil
}

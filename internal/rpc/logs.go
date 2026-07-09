package rpc

import (
	"encoding/hex"
	"fmt"
	"strings"

	"chainlab/internal/hash"
	"chainlab/internal/node"
	"chainlab/internal/types"
)

type logFilter struct {
	fromBlock uint64
	toBlock   uint64
	addresses map[string]struct{}
	topics    []topicCriterion
}

type topicCriterion struct {
	any     bool
	allowed map[string]struct{}
}

func parseLogFilter(value any, tags blockTags) (logFilter, error) {
	raw, ok := value.(map[string]any)
	if !ok {
		return logFilter{}, fmt.Errorf("filter must be an object")
	}
	filter := logFilter{fromBlock: 0, toBlock: tags.latest}
	if value, ok := raw["fromBlock"]; ok {
		height, err := parseBlockNumber(value, tags)
		if err != nil {
			return logFilter{}, err
		}
		filter.fromBlock = height
	}
	if value, ok := raw["toBlock"]; ok {
		height, err := parseBlockNumber(value, tags)
		if err != nil {
			return logFilter{}, err
		}
		filter.toBlock = height
	}
	if value, ok := raw["address"]; ok {
		addresses, err := parseLogAddresses(value)
		if err != nil {
			return logFilter{}, err
		}
		filter.addresses = addresses
	}
	if value, ok := raw["topics"]; ok {
		topics, err := parseTopicCriteria(value)
		if err != nil {
			return logFilter{}, err
		}
		filter.topics = topics
	}
	return filter, nil
}

func parseLogAddresses(value any) (map[string]struct{}, error) {
	addresses := make(map[string]struct{})
	switch typed := value.(type) {
	case string:
		addresses[strings.ToLower(typed)] = struct{}{}
	case []any:
		for _, item := range typed {
			address, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("address filter values must be strings")
			}
			addresses[strings.ToLower(address)] = struct{}{}
		}
	default:
		return nil, fmt.Errorf("address filter must be a string or array")
	}
	return addresses, nil
}

func parseTopicCriteria(value any) ([]topicCriterion, error) {
	rawTopics, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("topics filter must be an array")
	}
	topics := make([]topicCriterion, 0, len(rawTopics))
	for _, raw := range rawTopics {
		if raw == nil {
			topics = append(topics, topicCriterion{any: true})
			continue
		}
		criterion := topicCriterion{allowed: make(map[string]struct{})}
		switch typed := raw.(type) {
		case string:
			criterion.allowed[strings.ToLower(typed)] = struct{}{}
		case []any:
			if len(typed) == 0 {
				criterion.any = true
				break
			}
			for _, item := range typed {
				topic, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("topic filter values must be strings")
				}
				criterion.allowed[strings.ToLower(topic)] = struct{}{}
			}
		default:
			return nil, fmt.Errorf("topic filter entries must be strings, arrays, or null")
		}
		topics = append(topics, criterion)
	}
	return topics, nil
}

func evmLogs(n *node.Node, filter logFilter) []map[string]any {
	if filter.toBlock < filter.fromBlock {
		return []map[string]any{}
	}
	logs := make([]map[string]any, 0)
	for height := filter.fromBlock; height <= filter.toBlock; height++ {
		block, ok := n.Block(height)
		if !ok {
			break
		}
		logs = append(logs, evmBlockLogs(block, filter)...)
	}
	return logs
}

func evmBlockLogs(block types.Block, filter logFilter) []map[string]any {
	logs := make([]map[string]any, 0)
	blockHash := block.Hash()
	blockLogIndex := uint64(0)
	for txIndex, tx := range block.Transactions {
		if txIndex >= len(block.Receipts) {
			continue
		}
		receipt := block.Receipts[txIndex]
		address := logAddress(tx, receipt)
		if address == "" {
			continue
		}
		for _, event := range receipt.Events {
			topics := eventTopics(event)
			log := map[string]any{
				"removed":          false,
				"logIndex":         quantity(blockLogIndex),
				"transactionIndex": quantity(uint64(txIndex)),
				"transactionHash":  tx.Hash(),
				"blockHash":        blockHash,
				"blockNumber":      quantity(block.Header.Height),
				"address":          strings.ToLower(address),
				"data":             eventData(event),
				"topics":           topics,
			}
			blockLogIndex++
			if logMatchesFilter(log, topics, filter) {
				logs = append(logs, log)
			}
		}
	}
	return logs
}

func evmTransactionLogs(block types.Block, txHash string) []map[string]any {
	logs := evmBlockLogs(block, logFilter{})
	matched := make([]map[string]any, 0)
	for _, log := range logs {
		if log["transactionHash"] == txHash {
			matched = append(matched, log)
		}
	}
	return matched
}

func logAddress(tx types.Transaction, receipt types.Receipt) string {
	if receipt.ContractAddress != "" {
		return receipt.ContractAddress
	}
	if tx.Type == types.TxCall {
		return tx.To
	}
	return ""
}

func eventTopics(event types.Event) []string {
	return []string{hash.KeccakHex([]byte(event.Type))}
}

func eventData(event types.Event) string {
	raw := hash.MustCanonicalBytes(event.Attributes)
	return "0x" + hex.EncodeToString(raw)
}

func logMatchesFilter(log map[string]any, topics []string, filter logFilter) bool {
	if len(filter.addresses) > 0 {
		address, _ := log["address"].(string)
		if _, ok := filter.addresses[strings.ToLower(address)]; !ok {
			return false
		}
	}
	for i, criterion := range filter.topics {
		if criterion.any {
			continue
		}
		if i >= len(topics) {
			return false
		}
		if _, ok := criterion.allowed[strings.ToLower(topics[i])]; !ok {
			return false
		}
	}
	return true
}

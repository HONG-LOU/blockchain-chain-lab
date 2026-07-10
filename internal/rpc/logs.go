package rpc

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"chainlab/internal/hash"
	"chainlab/internal/node"
	"chainlab/internal/types"
)

const (
	MaxLogBlockRange            uint64 = 10_000
	MaxLogResults                      = 1_000
	MaxLogResponseBytes                = 8 * 1024 * 1024
	MaxLogAddresses                    = 256
	MaxLogTopics                       = 4
	MaxLogTopicAlternatives            = 256
	MaxLogFilterDefinitionBytes        = 64 * 1024

	// These charges conservatively include Go string/map/slice metadata in
	// addition to the bytes retained by each canonical filter value.
	logFilterDefinitionBaseBytes = 64
	logFilterSetBaseBytes        = 64
	logFilterSetEntryBytes       = 32
	logFilterTopicCriterionBytes = 32
)

type logFilter struct {
	fromBlock     uint64
	toBlock       uint64
	toBlockLatest bool
	addresses     map[string]struct{}
	topics        []topicCriterion
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
	if err := validateLogFilterDefinition(filter); err != nil {
		return logFilter{}, err
	}
	return filter, nil
}

func parseNewLogFilter(value any, tags blockTags) (logFilter, uint64, error) {
	raw, ok := value.(map[string]any)
	if !ok {
		return logFilter{}, 0, fmt.Errorf("filter must be an object")
	}
	filter, err := parseLogFilter(value, tags)
	if err != nil {
		return logFilter{}, 0, err
	}
	nextBlock := filter.fromBlock
	if _, ok := raw["fromBlock"]; !ok {
		filter.fromBlock = tags.latest
		nextBlock = tags.latest + 1
	}
	if _, ok := raw["toBlock"]; !ok {
		filter.toBlockLatest = true
	}
	return filter, nextBlock, nil
}

func (filter logFilter) resolvedToBlock(latest uint64) logFilter {
	if filter.toBlockLatest {
		filter.toBlock = latest
		filter.toBlockLatest = false
	}
	return filter
}

func parseLogAddresses(value any) (map[string]struct{}, error) {
	addresses := make(map[string]struct{})
	switch typed := value.(type) {
	case string:
		if err := validateCanonicalLogAddress(typed); err != nil {
			return nil, err
		}
		addresses[typed] = struct{}{}
	case []any:
		if len(typed) > MaxLogAddresses {
			return nil, fmt.Errorf("address filter exceeds %d values", MaxLogAddresses)
		}
		for _, item := range typed {
			address, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("address filter values must be strings")
			}
			if err := validateCanonicalLogAddress(address); err != nil {
				return nil, err
			}
			addresses[address] = struct{}{}
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
	if len(rawTopics) > MaxLogTopics {
		return nil, fmt.Errorf("topics filter exceeds %d positions", MaxLogTopics)
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
			if err := validateCanonicalLogTopic(typed); err != nil {
				return nil, err
			}
			criterion.allowed[typed] = struct{}{}
		case []any:
			if len(typed) > MaxLogTopicAlternatives {
				return nil, fmt.Errorf("topic filter position exceeds %d alternatives", MaxLogTopicAlternatives)
			}
			if len(typed) == 0 {
				criterion.any = true
				break
			}
			for _, item := range typed {
				topic, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("topic filter values must be strings")
				}
				if err := validateCanonicalLogTopic(topic); err != nil {
					return nil, err
				}
				criterion.allowed[topic] = struct{}{}
			}
		default:
			return nil, fmt.Errorf("topic filter entries must be strings, arrays, or null")
		}
		topics = append(topics, criterion)
	}
	return topics, nil
}

func validateCanonicalLogAddress(address string) error {
	if len(address) != 42 || !strings.HasPrefix(address, "0x") || address != strings.ToLower(address) {
		return fmt.Errorf("address filter value is not a canonical 20-byte hex address")
	}
	if _, err := hex.DecodeString(address[2:]); err != nil {
		return fmt.Errorf("address filter value is not a canonical 20-byte hex address")
	}
	return nil
}

func validateCanonicalLogTopic(topic string) error {
	if err := types.ValidateCanonicalHash("topic filter value", topic); err != nil {
		return err
	}
	return nil
}

func validateLogFilterDefinition(filter logFilter) error {
	if len(filter.addresses) > MaxLogAddresses {
		return fmt.Errorf("address filter exceeds %d values", MaxLogAddresses)
	}
	if len(filter.topics) > MaxLogTopics {
		return fmt.Errorf("topics filter exceeds %d positions", MaxLogTopics)
	}

	definitionBytes := logFilterDefinitionBaseBytes
	if filter.addresses != nil {
		definitionBytes += logFilterSetBaseBytes
	}
	for address := range filter.addresses {
		if err := validateCanonicalLogAddress(address); err != nil {
			return err
		}
		definitionBytes += len(address) + logFilterSetEntryBytes
	}
	for _, criterion := range filter.topics {
		definitionBytes += logFilterTopicCriterionBytes
		if len(criterion.allowed) > MaxLogTopicAlternatives {
			return fmt.Errorf("topic filter position exceeds %d alternatives", MaxLogTopicAlternatives)
		}
		if criterion.allowed != nil {
			definitionBytes += logFilterSetBaseBytes
		}
		for topic := range criterion.allowed {
			if err := validateCanonicalLogTopic(topic); err != nil {
				return err
			}
			definitionBytes += len(topic) + logFilterSetEntryBytes
		}
	}
	if definitionBytes > MaxLogFilterDefinitionBytes {
		return fmt.Errorf("log filter definition exceeds %d bytes", MaxLogFilterDefinitionBytes)
	}
	return nil
}

func evmLogs(n *node.Node, filter logFilter) ([]map[string]any, error) {
	return evmLogsWithByteLimit(n, filter, MaxLogResponseBytes)
}

func evmLogsWithByteLimit(n *node.Node, filter logFilter, responseByteLimit int) ([]map[string]any, error) {
	if responseByteLimit < 2 {
		return nil, fmt.Errorf("log query response exceeds %d bytes", MaxLogResponseBytes)
	}
	if filter.toBlock < filter.fromBlock {
		return []map[string]any{}, nil
	}
	if filter.toBlock-filter.fromBlock >= MaxLogBlockRange {
		return nil, fmt.Errorf("log query block range exceeds %d blocks", MaxLogBlockRange)
	}
	collector := newEVMLogCollector(responseByteLimit)
	records := n.Events(node.EventFilter{
		FromBlock:      filter.fromBlock,
		ToBlock:        filter.toBlock,
		HasToBlock:     true,
		Addresses:      logFilterAddresses(filter),
		RequireAddress: true,
		Topic0s:        logFilterTopic0s(filter),
		Limit:          MaxLogResults + 1,
	})
	for _, record := range records {
		if record.Address == "" {
			continue
		}
		topics := []string{record.Topic0}
		log := evmEventLog(record, topics)
		if logMatchesFilter(log, topics, filter) {
			if err := collector.append(log); err != nil {
				return nil, err
			}
		}
	}
	return collector.logs, nil
}

type evmLogCollector struct {
	logs          []map[string]any
	responseBytes int
	byteLimit     int
}

func newEVMLogCollector(byteLimit int) *evmLogCollector {
	return &evmLogCollector{
		logs:          make([]map[string]any, 0),
		responseBytes: 2,
		byteLimit:     byteLimit,
	}
}

func (collector *evmLogCollector) append(log map[string]any) error {
	if len(collector.logs) == MaxLogResults {
		return fmt.Errorf("log query returned more than %d results", MaxLogResults)
	}
	encoded, err := json.Marshal(log)
	if err != nil {
		return fmt.Errorf("encode log query result: %w", err)
	}
	separatorBytes := 0
	if len(collector.logs) > 0 {
		separatorBytes = 1
	}
	encodedBytes := len(encoded) + separatorBytes
	if collector.byteLimit < collector.responseBytes || encodedBytes > collector.byteLimit-collector.responseBytes {
		return fmt.Errorf("log query response exceeds %d bytes", MaxLogResponseBytes)
	}
	collector.responseBytes += encodedBytes
	collector.logs = append(collector.logs, log)
	return nil
}

func logFilterAddresses(filter logFilter) []string {
	addresses := make([]string, 0, len(filter.addresses))
	for address := range filter.addresses {
		addresses = append(addresses, address)
	}
	return addresses
}

func logFilterTopic0s(filter logFilter) []string {
	if len(filter.topics) == 0 || filter.topics[0].any {
		return nil
	}
	topics := make([]string, 0, len(filter.topics[0].allowed))
	for topic := range filter.topics[0].allowed {
		topics = append(topics, topic)
	}
	return topics
}

func evmEventLog(record types.EventRecord, topics []string) map[string]any {
	return map[string]any{
		"removed":          false,
		"logIndex":         quantity(record.LogIndex),
		"transactionIndex": quantity(uint64(record.TransactionIndex)),
		"transactionHash":  record.TransactionHash,
		"blockHash":        record.BlockHash,
		"blockNumber":      quantity(record.BlockHeight),
		"address":          strings.ToLower(record.Address),
		"data":             eventData(record.Event),
		"topics":           topics,
	}
}

func evmBlockLogs(block types.Block, filter logFilter) []map[string]any {
	logs := evmBlockLogsUnfiltered(block)
	if len(filter.addresses) == 0 && len(filter.topics) == 0 {
		return logs
	}
	matched := make([]map[string]any, 0, len(logs))
	for _, log := range logs {
		topics, _ := log["topics"].([]string)
		if logMatchesFilter(log, topics, filter) {
			matched = append(matched, log)
		}
	}
	return matched
}

func evmBlockLogsUnfiltered(block types.Block) []map[string]any {
	logs := make([]map[string]any, 0)
	blockHash := block.Hash()
	blockLogIndex := uint64(0)
	for txIndex, tx := range block.Transactions {
		if txIndex >= len(block.Receipts) {
			continue
		}
		receipt := block.Receipts[txIndex]
		address := types.EventSourceAddress(tx, receipt)
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
			logs = append(logs, log)
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

func eventTopics(event types.Event) []string {
	return []string{hash.KeccakHex([]byte(event.Type))}
}

func eventData(event types.Event) string {
	raw := hash.MustCanonicalBytes(event.Attributes)
	return "0x" + hex.EncodeToString(raw)
}

func logMatchesFilter(log map[string]any, topics []string, filter logFilter) bool {
	address, _ := log["address"].(string)
	return eventMatchesLogFilter(address, topics, filter)
}

func eventMatchesLogFilter(address string, topics []string, filter logFilter) bool {
	if len(filter.addresses) > 0 {
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

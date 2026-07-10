package node

import (
	"strings"
	"testing"

	"chainlab/internal/types"
)

func TestEventsMatchesAddressAndTopicSetsBeforeApplyingLimit(t *testing.T) {
	const (
		addressA = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		addressB = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		addressC = "0xcccccccccccccccccccccccccccccccccccccccc"
		topicA   = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		topicB   = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		topicC   = "0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	)

	n := &Node{eventIndex: []types.EventRecord{
		testIndexedEvent(1, addressA, topicA, "outside-low"),
		testIndexedEvent(2, "", topicA, "no-source-address"),
		testIndexedEvent(2, addressB, topicA, "wrong-address-1"),
		testIndexedEvent(2, addressB, topicB, "wrong-address-2"),
		testIndexedEvent(2, addressA, topicB, "wrong-topic"),
		testIndexedEvent(2, addressA, topicA, "match-a"),
		testIndexedEvent(2, addressC, topicC, "match-c"),
		testIndexedEvent(3, addressA, topicC, "match-later"),
		testIndexedEvent(4, addressA, topicA, "outside-high"),
	}}

	events := n.Events(EventFilter{
		FromBlock:      2,
		ToBlock:        3,
		HasToBlock:     true,
		Addresses:      []string{strings.ToUpper(addressA), addressC},
		RequireAddress: true,
		Topic0s:        []string{strings.ToUpper(topicA), topicC},
		Limit:          2,
	})
	if len(events) != 2 || events[0].Event.Type != "match-a" || events[1].Event.Type != "match-c" {
		t.Fatalf("set-filtered events = %+v", events)
	}
	addressed := n.Events(EventFilter{
		FromBlock:      2,
		ToBlock:        2,
		HasToBlock:     true,
		RequireAddress: true,
		Limit:          1,
	})
	if len(addressed) != 1 || addressed[0].Event.Type != "wrong-address-1" {
		t.Fatalf("address-bearing limited events = %+v", addressed)
	}

	descending := n.Events(EventFilter{
		FromBlock:  2,
		ToBlock:    3,
		HasToBlock: true,
		Addresses:  []string{addressA, addressC},
		Topic0s:    []string{topicA, topicC},
		Limit:      2,
		Descending: true,
	})
	if len(descending) != 2 || descending[0].Event.Type != "match-later" || descending[1].Event.Type != "match-a" {
		t.Fatalf("descending set-filtered events = %+v", descending)
	}

	legacy := n.Events(EventFilter{
		FromBlock:  2,
		ToBlock:    3,
		HasToBlock: true,
		Address:    strings.ToUpper(addressC),
		Topic0:     strings.ToUpper(topicC),
	})
	if len(legacy) != 1 || legacy[0].Event.Type != "match-c" {
		t.Fatalf("legacy single-value filter events = %+v", legacy)
	}
}

func testIndexedEvent(height uint64, address string, topic string, eventType string) types.EventRecord {
	return types.EventRecord{
		Event:       types.Event{Type: eventType, Attributes: map[string]string{"type": eventType}},
		Address:     address,
		Topic0:      topic,
		BlockHeight: height,
	}
}

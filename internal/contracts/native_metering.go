package contracts

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"chainlab/internal/state"
	"chainlab/internal/types"
)

const NativeMeteringVersion = "chainlab-native-v1"

const (
	nativeInvocationGas               uint64 = 500
	nativeInputByteGas                uint64 = 2
	nativeStorageReadBaseGas          uint64 = 100
	nativeStorageReadByteGas          uint64 = 1
	nativeStorageWriteBaseGas         uint64 = 500
	nativeStorageWriteByteGas         uint64 = 5
	nativeStorageDeleteBaseGas        uint64 = 300
	nativeStorageDeleteByteGas        uint64 = 2
	nativeStorageScanEntryGas         uint64 = 25
	nativeAccountWriteGas             uint64 = 500
	nativeEventBaseGas                uint64 = 200
	nativeEventByteGas                uint64 = 2
	nativeOutputByteGas               uint64 = 1
	MaxNativeArgumentFields                  = 64
	MaxNativeMethodBytes                     = 128
	MaxNativeArgumentKeyBytes                = 256
	MaxNativeArgumentValueBytes              = 64 * 1024
	MaxNativeInputBytes                      = 256 * 1024
	MaxNativeStorageEntries                  = state.MaxStorageEntriesPerAccount
	MaxNativeStorageKeyBytes                 = state.MaxStorageKeyBytes
	MaxNativeStorageValueBytes               = state.MaxStorageValueBytes
	MaxNativeEvents                          = 64
	MaxNativeEventBytes                      = 64 * 1024
	MaxNativeEventTypeBytes                  = 128
	MaxNativeEventAttributes                 = 64
	MaxNativeEventAttributeKeyBytes          = 256
	MaxNativeEventAttributeValueBytes        = 16 * 1024
	MaxNativeStorageOperations               = 8192
	MaxNativeStorageWrites                   = 128
	MaxNativeStorageIOBytes                  = 1024 * 1024
)

var ErrNativeResourceLimit = errors.New("native contract resource limit exceeded")

type nativeInvocationBudget struct {
	operations int
	writes     int
	ioBytes    int
}

func (ctx Context) GetStorage(key string) (string, error) {
	store, err := ctx.nativeReader()
	if err != nil {
		return "", err
	}
	if err := validateNativeStorageKey(key, false); err != nil {
		return "", err
	}
	value := store.GetStorage(ctx.address, key)
	if len(value) > MaxNativeStorageValueBytes {
		return "", fmt.Errorf("%w: stored value exceeds limit", ErrContractStateFault)
	}
	if err := ctx.consumeNativeStorageBudget(1, 0, len(key)+len(value)); err != nil {
		return "", err
	}
	if err := chargeNativeLinear(ctx.meter, nativeStorageReadBaseGas, nativeStorageReadByteGas, len(key), len(value)); err != nil {
		return "", err
	}
	return value, nil
}

func (ctx Context) SetStorage(key string, value string) error {
	if ctx.readOnly {
		return ErrReadOnlyContract
	}
	store, err := ctx.nativeStore()
	if err != nil {
		return err
	}
	if err := validateNativeStorageKey(key, false); err != nil {
		return err
	}
	if len(value) > MaxNativeStorageValueBytes {
		return fmt.Errorf("%w: storage value bytes", ErrNativeResourceLimit)
	}
	entryCount := store.StorageEntryCount(ctx.address)
	if entryCount > MaxNativeStorageEntries {
		return fmt.Errorf("%w: stored entry count exceeds limit", ErrContractStateFault)
	}
	oldValue, exists := store.GetStorageWithExists(ctx.address, key)
	if !exists && entryCount == MaxNativeStorageEntries {
		return fmt.Errorf("%w: storage entries", ErrNativeResourceLimit)
	}
	if len(oldValue) > MaxNativeStorageValueBytes {
		return fmt.Errorf("%w: stored value exceeds limit", ErrContractStateFault)
	}
	if err := ctx.consumeNativeStorageBudget(1, 1, len(key)+len(oldValue)+len(value)); err != nil {
		return err
	}
	if err := chargeNativeLinear(ctx.meter, nativeStorageWriteBaseGas, nativeStorageWriteByteGas, len(key), len(oldValue), len(value)); err != nil {
		return err
	}
	if err := store.SetStorage(ctx.address, key, value); err != nil {
		if errors.Is(err, state.ErrStorageLimit) {
			return fmt.Errorf("%w: storage entries", ErrNativeResourceLimit)
		}
		return fmt.Errorf("%w: native storage write", ErrContractStateFault)
	}
	return nil
}

func (ctx Context) DeleteStorage(key string) error {
	if ctx.readOnly {
		return ErrReadOnlyContract
	}
	store, err := ctx.nativeStore()
	if err != nil {
		return err
	}
	if err := validateNativeStorageKey(key, false); err != nil {
		return err
	}
	oldValue := store.GetStorage(ctx.address, key)
	if len(oldValue) > MaxNativeStorageValueBytes {
		return fmt.Errorf("%w: stored value exceeds limit", ErrContractStateFault)
	}
	if err := ctx.consumeNativeStorageBudget(1, 1, len(key)+len(oldValue)); err != nil {
		return err
	}
	if err := chargeNativeLinear(ctx.meter, nativeStorageDeleteBaseGas, nativeStorageDeleteByteGas, len(key), len(oldValue)); err != nil {
		return err
	}
	store.DeleteStorage(ctx.address, key)
	return nil
}

func (ctx Context) DeleteStoragePrefix(prefix string) (int, error) {
	if ctx.readOnly {
		return 0, ErrReadOnlyContract
	}
	store, err := ctx.nativeStore()
	if err != nil {
		return 0, err
	}
	if err := validateNativeStorageKey(prefix, true); err != nil {
		return 0, err
	}
	keys := store.SortedStorageKeys(ctx.address)
	if len(keys) > MaxNativeStorageEntries {
		return 0, fmt.Errorf("%w: stored entry count exceeds limit", ErrContractStateFault)
	}
	scanIOBytes := len(prefix)
	for _, key := range keys {
		if err := validateNativeStorageKey(key, false); err != nil {
			return 0, fmt.Errorf("%w: invalid stored key", ErrContractStateFault)
		}
		scanIOBytes += len(key)
	}
	if err := ctx.consumeNativeStorageBudget(len(keys), 0, scanIOBytes); err != nil {
		return 0, err
	}
	if err := chargeNativeLinear(ctx.meter, 0, nativeStorageScanEntryGas, len(keys)); err != nil {
		return 0, err
	}
	removed := 0
	for _, key := range keys {
		if len(key) < len(prefix) || key[:len(prefix)] != prefix {
			continue
		}
		value := store.GetStorage(ctx.address, key)
		if len(value) > MaxNativeStorageValueBytes {
			return removed, fmt.Errorf("%w: stored value exceeds limit", ErrContractStateFault)
		}
		if err := ctx.consumeNativeStorageBudget(1, 1, len(value)); err != nil {
			return removed, err
		}
		if err := chargeNativeLinear(ctx.meter, nativeStorageDeleteBaseGas, nativeStorageDeleteByteGas, len(key), len(value)); err != nil {
			return removed, err
		}
		store.DeleteStorage(ctx.address, key)
		removed++
	}
	return removed, nil
}

func (ctx Context) consumeNativeStorageBudget(operations int, writes int, ioBytes int) error {
	budget := ctx.nativeBudget
	if budget == nil {
		return fmt.Errorf("%w: native invocation budget is nil", ErrContractStateFault)
	}
	if operations < 0 || writes < 0 || ioBytes < 0 ||
		operations > MaxNativeStorageOperations-budget.operations ||
		writes > MaxNativeStorageWrites-budget.writes ||
		ioBytes > MaxNativeStorageIOBytes-budget.ioBytes {
		return fmt.Errorf("%w: storage operation budget", ErrNativeResourceLimit)
	}
	budget.operations += operations
	budget.writes += writes
	budget.ioBytes += ioBytes
	return nil
}

func (ctx Context) nativeStore() (*state.Store, error) {
	if ctx.store == nil {
		return nil, fmt.Errorf("%w: native context store is nil", ErrContractStateFault)
	}
	return ctx.store, nil
}

func (ctx Context) nativeReader() (state.Reader, error) {
	if ctx.reader == nil {
		return nil, fmt.Errorf("%w: native context reader is nil", ErrContractStateFault)
	}
	return ctx.reader, nil
}

func validateNativeStorageKey(key string, allowPrefix bool) error {
	if key == "" {
		return fmt.Errorf("%w: storage key is empty", ErrNativeResourceLimit)
	}
	if len(key) > MaxNativeStorageKeyBytes {
		return fmt.Errorf("%w: storage key bytes", ErrNativeResourceLimit)
	}
	return nil
}

func chargeNativeInvocation(meter *Meter, operation string, args map[string]string) error {
	if operation == "" || len(operation) > MaxNativeMethodBytes || len(operation) > MaxNativeInputBytes {
		return fmt.Errorf("%w: method bytes", ErrNativeResourceLimit)
	}
	if len(args) > MaxNativeArgumentFields {
		return fmt.Errorf("%w: argument fields", ErrNativeResourceLimit)
	}
	total := len(operation)
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := args[key]
		if len(key) > MaxNativeArgumentKeyBytes || len(value) > MaxNativeArgumentValueBytes {
			return fmt.Errorf("%w: argument bytes", ErrNativeResourceLimit)
		}
		if total > MaxNativeInputBytes-len(key) || total+len(key) > MaxNativeInputBytes-len(value) {
			return fmt.Errorf("%w: total input bytes", ErrNativeResourceLimit)
		}
		total += len(key) + len(value)
	}
	return chargeNativeLinear(meter, nativeInvocationGas, nativeInputByteGas, total)
}

func chargeNativeEvents(meter *Meter, events []types.Event) error {
	if len(events) > MaxNativeEvents {
		return fmt.Errorf("%w: event count", ErrNativeResourceLimit)
	}
	total := 0
	for _, event := range events {
		if event.Type == "" || len(event.Type) > MaxNativeEventTypeBytes || len(event.Attributes) > MaxNativeEventAttributes {
			return fmt.Errorf("%w: event shape", ErrNativeResourceLimit)
		}
		if total > MaxNativeEventBytes-len(event.Type) {
			return fmt.Errorf("%w: event bytes", ErrNativeResourceLimit)
		}
		total += len(event.Type)
		keys := make([]string, 0, len(event.Attributes))
		for key := range event.Attributes {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := event.Attributes[key]
			if key == "" || len(key) > MaxNativeEventAttributeKeyBytes || len(value) > MaxNativeEventAttributeValueBytes {
				return fmt.Errorf("%w: event attribute bytes", ErrNativeResourceLimit)
			}
			if total > MaxNativeEventBytes-len(key) || total+len(key) > MaxNativeEventBytes-len(value) {
				return fmt.Errorf("%w: event bytes", ErrNativeResourceLimit)
			}
			total += len(key) + len(value)
		}
	}
	for range events {
		if err := chargeNativeLinear(meter, nativeEventBaseGas, 0); err != nil {
			return err
		}
	}
	return chargeNativeLinear(meter, 0, nativeEventByteGas, total)
}

func chargeNativeOutput(meter *Meter, value string) error {
	if len(value) > MaxNativeStorageValueBytes {
		return fmt.Errorf("%w: output bytes", ErrNativeResourceLimit)
	}
	return chargeNativeLinear(meter, 0, nativeOutputByteGas, len(value))
}

func chargeNativeLinear(meter *Meter, base uint64, perUnit uint64, units ...int) error {
	totalUnits := uint64(0)
	for _, unit := range units {
		if unit < 0 || math.MaxUint64-totalUnits < uint64(unit) {
			return fmt.Errorf("%w: gas size overflow", ErrContractStateFault)
		}
		totalUnits += uint64(unit)
	}
	if perUnit != 0 && totalUnits > (math.MaxUint64-base)/perUnit {
		return fmt.Errorf("%w: gas cost overflow", ErrContractStateFault)
	}
	return meter.Charge(base + totalUnits*perUnit)
}

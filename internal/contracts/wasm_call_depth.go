package contracts

import (
	"errors"
	"fmt"
)

const MaxWASMCallDepth uint32 = 64

func parseWasmDirectCalls(instructions []byte) ([]uint32, error) {
	reader := wasmSectionReader{data: instructions}
	directCalls := make([]uint32, 0)
	for !reader.exhausted() {
		opcode, ok := reader.readByte()
		if !ok {
			return nil, errors.New("invalid wasm instruction")
		}
		switch {
		case opcode == 0x00 || opcode == 0x01 || opcode == 0x05 || opcode == 0x0b ||
			opcode == 0x0f || opcode == 0x1a || opcode == 0x1b || opcode == 0xd1:
		case opcode >= 0x45 && opcode <= 0xbf:
		case opcode >= 0xc0 && opcode <= 0xc4:
		case opcode == 0x02 || opcode == 0x03 || opcode == 0x04:
			if !reader.readSignedLEB(33) {
				return nil, invalidWasmInstructionImmediate(opcode)
			}
		case opcode == 0x0c || opcode == 0x0d || opcode >= 0x20 && opcode <= 0x26:
			if _, ok := reader.readU32(); !ok {
				return nil, invalidWasmInstructionImmediate(opcode)
			}
		case opcode == 0x0e:
			if !reader.readU32Vector() {
				return nil, invalidWasmInstructionImmediate(opcode)
			}
		case opcode == 0x10:
			functionIndex, ok := reader.readU32()
			if !ok {
				return nil, invalidWasmInstructionImmediate(opcode)
			}
			directCalls = append(directCalls, functionIndex)
		case opcode == 0x11:
			return nil, errors.New("wasm call_indirect is forbidden in version 1")
		case opcode >= 0x12 && opcode <= 0x15:
			return nil, errors.New("wasm tail and function-reference calls are forbidden in version 1")
		case opcode == 0x1c:
			if !reader.readValueTypeVector() {
				return nil, invalidWasmInstructionImmediate(opcode)
			}
		case opcode >= 0x28 && opcode <= 0x3e:
			if _, ok := reader.readU32(); !ok {
				return nil, invalidWasmInstructionImmediate(opcode)
			}
			if _, ok := reader.readU32(); !ok {
				return nil, invalidWasmInstructionImmediate(opcode)
			}
		case opcode == 0x3f || opcode == 0x40:
			if _, ok := reader.readU32(); !ok {
				return nil, invalidWasmInstructionImmediate(opcode)
			}
		case opcode == 0x41:
			if !reader.readSignedLEB(32) {
				return nil, invalidWasmInstructionImmediate(opcode)
			}
		case opcode == 0x42:
			if !reader.readSignedLEB(64) {
				return nil, invalidWasmInstructionImmediate(opcode)
			}
		case opcode == 0x43:
			if _, ok := reader.readBytes(4); !ok {
				return nil, invalidWasmInstructionImmediate(opcode)
			}
		case opcode == 0x44:
			if _, ok := reader.readBytes(8); !ok {
				return nil, invalidWasmInstructionImmediate(opcode)
			}
		case opcode == 0xd0:
			if !reader.readSignedLEB(33) {
				return nil, invalidWasmInstructionImmediate(opcode)
			}
		case opcode == 0xd2:
			if _, ok := reader.readU32(); !ok {
				return nil, invalidWasmInstructionImmediate(opcode)
			}
		case opcode == 0xfc:
			if err := reader.readAllowedMiscInstruction(); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("wasm opcode 0x%02x is not allowed in version 1", opcode)
		}
	}
	return directCalls, nil
}

func (p *wasmBinaryPolicy) validateDirectCallGraph() error {
	importCount := uint32(len(p.importedFunctionTypes))
	totalFunctions := importCount + p.functionCount
	if uint32(len(p.codeBodies)) != p.functionCount {
		return errors.New("wasm direct call graph does not match code bodies")
	}
	graph := make([][]uint32, totalFunctions)
	for definedIndex, body := range p.codeBodies {
		calls, err := parseWasmDirectCalls(body)
		if err != nil {
			return fmt.Errorf("wasm function %d: %w", definedIndex, err)
		}
		functionIndex := importCount + uint32(definedIndex)
		graph[functionIndex] = calls
		for _, target := range calls {
			if target >= totalFunctions {
				return fmt.Errorf("wasm direct call target %d is out of bounds", target)
			}
		}
	}

	const (
		callUnvisited uint8 = iota
		callVisiting
		callVisited
	)
	states := make([]uint8, totalFunctions)
	depths := make([]uint32, totalFunctions)
	type callFrame struct {
		functionIndex uint32
		nextCall      int
	}
	for root := uint32(0); root < totalFunctions; root++ {
		if states[root] == callVisited {
			continue
		}
		states[root] = callVisiting
		stack := []callFrame{{functionIndex: root}}
		for len(stack) != 0 {
			frame := &stack[len(stack)-1]
			calls := graph[frame.functionIndex]
			if frame.nextCall < len(calls) {
				target := calls[frame.nextCall]
				frame.nextCall++
				switch states[target] {
				case callVisiting:
					return errors.New("wasm direct call graph contains recursion")
				case callVisited:
					continue
				default:
					states[target] = callVisiting
					stack = append(stack, callFrame{functionIndex: target})
					continue
				}
			}

			depth := uint32(1)
			for _, target := range calls {
				targetDepth := depths[target]
				if targetDepth >= MaxWASMCallDepth {
					return fmt.Errorf("wasm direct call depth exceeds the version-1 limit of %d", MaxWASMCallDepth)
				}
				if targetDepth+1 > depth {
					depth = targetDepth + 1
				}
			}
			states[frame.functionIndex] = callVisited
			depths[frame.functionIndex] = depth
			stack = stack[:len(stack)-1]
		}
	}
	return nil
}

func invalidWasmInstructionImmediate(opcode byte) error {
	return fmt.Errorf("invalid immediate for wasm opcode 0x%02x", opcode)
}

func (r *wasmSectionReader) readSignedLEB(bitWidth uint) bool {
	maximumBytes := (bitWidth + 6) / 7
	for byteIndex := uint(0); byteIndex < maximumBytes; byteIndex++ {
		value, ok := r.readByte()
		if !ok {
			return false
		}
		if value&0x80 != 0 {
			continue
		}
		if byteIndex == maximumBytes-1 {
			usedBits := bitWidth - byteIndex*7
			valueMask := byte((uint16(1) << usedBits) - 1)
			unusedMask := ^valueMask & 0x7f
			payload := value & 0x7f
			signBit := byte(1) << (usedBits - 1)
			if payload&signBit == 0 && payload&unusedMask != 0 {
				return false
			}
			if payload&signBit != 0 && payload&unusedMask != unusedMask {
				return false
			}
		}
		return true
	}
	return false
}

func (r *wasmSectionReader) readU32Vector() bool {
	count, ok := r.readU32()
	if !ok || uint64(count)+1 > uint64(len(r.data)-r.offset) {
		return false
	}
	for index := uint64(0); index <= uint64(count); index++ {
		if _, ok := r.readU32(); !ok {
			return false
		}
	}
	return true
}

func (r *wasmSectionReader) readValueTypeVector() bool {
	count, ok := r.readU32()
	if !ok || uint64(count) > uint64(len(r.data)-r.offset) {
		return false
	}
	for index := uint32(0); index < count; index++ {
		valueType, ok := r.readByte()
		if !ok {
			return false
		}
		switch valueType {
		case 0x7f, 0x7e, 0x7d, 0x7c, 0x70, 0x6f:
		default:
			return false
		}
	}
	return true
}

func (r *wasmSectionReader) readAllowedMiscInstruction() error {
	opcode, ok := r.readU32()
	if !ok {
		return invalidWasmInstructionImmediate(0xfc)
	}
	switch {
	case opcode <= 0x07:
		return nil
	case opcode >= 0x0f && opcode <= 0x11:
		if _, ok := r.readU32(); !ok {
			return invalidWasmInstructionImmediate(0xfc)
		}
		return nil
	default:
		return fmt.Errorf("wasm miscellaneous opcode 0xfc/%d is not allowed in version 1", opcode)
	}
}

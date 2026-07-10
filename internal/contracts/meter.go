package contracts

import (
	"errors"
	"math"
)

var ErrContractOutOfGas = errors.New("contract out of gas")

type Meter struct {
	gasUsed   uint64
	gasLimit  uint64
	limited   bool
	exhausted bool
}

func NewMeter() *Meter {
	return &Meter{}
}

func NewLimitedMeter(gasLimit uint64) *Meter {
	return &Meter{gasLimit: gasLimit, limited: true}
}

func (m *Meter) Charge(amount uint64) error {
	if m == nil {
		return nil
	}
	if m.exhausted {
		return ErrContractOutOfGas
	}
	if amount == 0 {
		return nil
	}
	if m.limited {
		if amount > m.gasLimit-m.gasUsed {
			m.gasUsed = m.gasLimit
			m.exhausted = true
			return ErrContractOutOfGas
		}
		m.gasUsed += amount
		return nil
	}
	if math.MaxUint64-m.gasUsed < amount {
		return errors.New("contract gas meter overflow")
	}
	m.gasUsed += amount
	return nil
}

func (m *Meter) GasUsed() uint64 {
	if m == nil {
		return 0
	}
	return m.gasUsed
}

func (m *Meter) Remaining() uint64 {
	if m == nil {
		return math.MaxUint64
	}
	if m.exhausted {
		return 0
	}
	if m.limited {
		return m.gasLimit - m.gasUsed
	}
	return math.MaxUint64 - m.gasUsed
}

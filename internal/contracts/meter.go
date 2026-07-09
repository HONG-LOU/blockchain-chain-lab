package contracts

import (
	"errors"
	"math"
)

type Meter struct {
	gasUsed uint64
}

func NewMeter() *Meter {
	return &Meter{}
}

func (m *Meter) Charge(amount uint64) error {
	if m == nil || amount == 0 {
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

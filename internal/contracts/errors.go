package contracts

import "errors"

var ErrContractStateFault = errors.New("contract state invariant violated")

var ErrNativeRuntimeFault = errors.New("native contract runtime fault")

var errUnknownContractCode = errors.New("unknown contract code id")

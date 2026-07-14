package cometnode

import (
	"errors"
	"fmt"
	"sync"

	cmtlog "github.com/cometbft/cometbft/libs/log"
)

const (
	consensusFailureMessage       = "CONSENSUS FAILURE!!!"
	voteSigningFailureMessage     = "failed signing vote"
	proposalSigningFailureMessage = "propose step; failed signing proposal"
)

type consensusFailureReporter struct {
	once sync.Once
	done chan error
}

type consensusFailureLogger struct {
	next     cmtlog.Logger
	reporter *consensusFailureReporter
}

func newConsensusFailureLogger(next cmtlog.Logger) (cmtlog.Logger, <-chan error) {
	reporter := &consensusFailureReporter{done: make(chan error, 1)}
	return consensusFailureLogger{next: next, reporter: reporter}, reporter.done
}

func (logger consensusFailureLogger) Debug(message string, keyvals ...any) {
	logger.next.Debug(message, keyvals...)
}

func (logger consensusFailureLogger) Info(message string, keyvals ...any) {
	logger.next.Info(message, keyvals...)
}

func (logger consensusFailureLogger) Warn(message string, keyvals ...any) {
	logger.next.Warn(message, keyvals...)
}

func (logger consensusFailureLogger) Error(message string, keyvals ...any) {
	logger.next.Error(message, keyvals...)
	if message != consensusFailureMessage && message != voteSigningFailureMessage && message != proposalSigningFailureMessage {
		return
	}
	failure := errors.New("CometBFT consensus failed: " + message)
	fatal := message == consensusFailureMessage
	for index := 0; index+1 < len(keyvals); index += 2 {
		if key, ok := keyvals[index].(string); ok && key == "err" {
			cause, isError := keyvals[index+1].(error)
			if message == consensusFailureMessage || (isError && errors.Is(cause, errValidatorStatePersistence)) {
				fatal = true
				failure = fmt.Errorf("CometBFT consensus failed (%s): %v", message, keyvals[index+1])
			}
			break
		}
	}
	if !fatal {
		return
	}
	logger.reporter.once.Do(func() { logger.reporter.done <- failure })
}

func (logger consensusFailureLogger) With(keyvals ...any) cmtlog.Logger {
	return consensusFailureLogger{next: logger.next.With(keyvals...), reporter: logger.reporter}
}

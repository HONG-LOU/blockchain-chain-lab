package abci

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	abciserver "github.com/cometbft/cometbft/abci/server"
	cmtlog "github.com/cometbft/cometbft/libs/log"
)

const MaxGenesisDocumentBytes int64 = 64 * 1024 * 1024

func LoadGenesisDocument(path string) (GenesisDocument, error) {
	if strings.TrimSpace(path) == "" {
		return GenesisDocument{}, errors.New("genesis path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return GenesisDocument{}, fmt.Errorf("open genesis document: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return GenesisDocument{}, fmt.Errorf("stat genesis document: %w", err)
	}
	if !info.Mode().IsRegular() {
		return GenesisDocument{}, errors.New("genesis document must be a regular file")
	}
	if info.Size() <= 0 {
		return GenesisDocument{}, errors.New("genesis document is empty")
	}
	if info.Size() > MaxGenesisDocumentBytes {
		return GenesisDocument{}, fmt.Errorf("genesis document exceeds %d bytes", MaxGenesisDocumentBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(file, MaxGenesisDocumentBytes+1))
	if err != nil {
		return GenesisDocument{}, fmt.Errorf("read genesis document: %w", err)
	}
	if int64(len(raw)) > MaxGenesisDocumentBytes {
		return GenesisDocument{}, fmt.Errorf("genesis document exceeds %d bytes", MaxGenesisDocumentBytes)
	}
	return ParseGenesisDocument(raw)
}

func ParseGenesisDocument(raw []byte) (GenesisDocument, error) {
	document, err := decodeGenesisDocument(raw)
	if err != nil {
		return GenesisDocument{}, err
	}
	canonical, err := document.CanonicalBytes()
	if err != nil {
		return GenesisDocument{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return GenesisDocument{}, errors.New("genesis document is not canonically encoded")
	}
	return document, nil
}

func Serve(ctx context.Context, listen string, genesis GenesisDocument, logger cmtlog.Logger) error {
	return ServeWithConfig(ctx, listen, Config{Genesis: genesis}, logger)
}

func ServeWithConfig(ctx context.Context, listen string, config Config, logger cmtlog.Logger) (returnErr error) {
	if ctx == nil {
		return errors.New("server context is required")
	}
	if strings.TrimSpace(listen) == "" {
		return errors.New("ABCI listen address is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	application, err := NewApplication(config)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, application.Close())
	}()
	server, err := abciserver.NewServer(listen, "socket", application)
	if err != nil {
		return fmt.Errorf("create ABCI socket server: %w", err)
	}
	if logger == nil {
		logger = cmtlog.NewNopLogger()
	}
	server.SetLogger(logger)
	if err := server.Start(); err != nil {
		return fmt.Errorf("start ABCI socket server: %w", err)
	}
	select {
	case <-ctx.Done():
		if err := server.Stop(); err != nil {
			return fmt.Errorf("stop ABCI socket server: %w", err)
		}
		<-server.Quit()
		return nil
	case <-server.Quit():
		return errors.New("ABCI socket server stopped unexpectedly")
	}
}

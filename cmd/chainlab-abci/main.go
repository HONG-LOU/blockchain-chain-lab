package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	chainabci "chainlab/internal/abci"

	cmtlog "github.com/cometbft/cometbft/libs/log"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("chainlab-abci", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	genesisPath := flags.String("genesis", "", "canonical ChainLab application genesis")
	listen := flags.String("listen", "tcp://127.0.0.1:26658", "ABCI socket listen address")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("chainlab-abci does not accept positional arguments")
	}
	if strings.TrimSpace(*genesisPath) == "" {
		return errors.New("chainlab-abci requires --genesis")
	}
	genesis, err := chainabci.LoadGenesisDocument(*genesisPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "chainlab ABCI listening on %s chain_id=%s\n", *listen, genesis.ChainID)
	logger := cmtlog.NewFilter(cmtlog.NewTMLogger(cmtlog.NewSyncWriter(out)), cmtlog.AllowInfo())
	return chainabci.Serve(ctx, *listen, genesis, logger)
}

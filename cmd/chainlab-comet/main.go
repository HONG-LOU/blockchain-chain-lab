package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	chainabci "chainlab/internal/abci"
	"chainlab/internal/cometnode"
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
	if len(args) == 0 {
		return errors.New("usage: chainlab-comet <init|node>")
	}
	switch args[0] {
	case "init":
		return runInit(args[1:], out)
	case "node":
		return runNode(ctx, args[1:], out)
	default:
		return fmt.Errorf("unsupported chainlab-comet command %q", args[0])
	}
}

func runInit(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("chainlab-comet init", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	output := flags.String("out", "", "new private-network directory")
	chainID := flags.String("chain-id", "chainlab-private", "chain id")
	validators := flags.Int("validators", cometnode.DefaultValidatorCount, "validator count")
	abciBasePort := flags.Int("abci-base-port", 26658, "first ABCI port")
	rpcBasePort := flags.Int("rpc-base-port", 26670, "first RPC port")
	p2pBasePort := flags.Int("p2p-base-port", 26680, "first P2P port")
	applicationProtocol := flags.String("application-protocol", chainabci.ProtocolVersion, "application genesis protocol: chainlab-v1 or chainlab-v2")
	upgradeV3Height := flags.Int64("upgrade-v3-height", 0, "activate chainlab-v3 proof roots at this height")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("chainlab-comet init does not accept positional arguments")
	}
	if strings.TrimSpace(*output) == "" {
		return errors.New("chainlab-comet init requires --out")
	}
	var validatorPolicy *chainabci.ValidatorPolicy
	if *applicationProtocol == chainabci.ProtocolVersionV2 {
		policy := chainabci.DefaultValidatorPolicy()
		validatorPolicy = &policy
	}
	var upgrades []chainabci.ProtocolUpgrade
	if *upgradeV3Height != 0 {
		upgrades = []chainabci.ProtocolUpgrade{{Height: *upgradeV3Height, Protocol: chainabci.ProtocolVersionV3}}
	}
	document, err := cometnode.InitializeNetwork(cometnode.NetworkConfig{
		OutputRoot:          *output,
		ChainID:             *chainID,
		ValidatorCount:      *validators,
		ABCIBasePort:        *abciBasePort,
		RPCBasePort:         *rpcBasePort,
		P2PBasePort:         *p2pBasePort,
		ApplicationProtocol: *applicationProtocol,
		ValidatorPolicy:     validatorPolicy,
		ProtocolUpgrades:    upgrades,
	})
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(document)
}

func runNode(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("chainlab-comet node", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	home := flags.String("home", "", "generated CometBFT node home")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("chainlab-comet node does not accept positional arguments")
	}
	if strings.TrimSpace(*home) == "" {
		return errors.New("chainlab-comet node requires --home")
	}
	fmt.Fprintf(out, "starting ChainLab CometBFT node home=%s\n", *home)
	return cometnode.Run(ctx, *home, out)
}

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
	dataDir := flags.String("data-dir", "", "transactional ChainLab application data directory")
	listen := flags.String("listen", "tcp://127.0.0.1:26658", "ABCI socket listen address")
	storageMode := flags.String("storage-mode", string(chainabci.StorageModeFull), "storage profile: pruned, full, or archive")
	retainHeights := flags.Uint64("retain-heights", chainabci.DefaultFullRetainHeights, "minimum recent heights retained by full storage")
	checkpointInterval := flags.Uint64("checkpoint-interval", chainabci.DefaultCheckpointInterval, "full/archive state checkpoint interval")
	backupTo := flags.String("backup-to", "", "create a consistent Pebble checkpoint and exit")
	compact := flags.Bool("compact", false, "compact application storage before serving or backup")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("chainlab-abci does not accept positional arguments")
	}
	explicit := make(map[string]bool)
	flags.Visit(func(value *flag.Flag) {
		explicit[value.Name] = true
	})
	if strings.TrimSpace(*genesisPath) == "" {
		return errors.New("chainlab-abci requires --genesis")
	}
	if strings.TrimSpace(*dataDir) == "" {
		return errors.New("chainlab-abci requires --data-dir")
	}
	genesis, err := chainabci.LoadGenesisDocument(*genesisPath)
	if err != nil {
		return err
	}
	profile := storageProfileFromFlags(
		chainabci.StorageMode(*storageMode), *retainHeights, *checkpointInterval,
		explicit["retain-heights"], explicit["checkpoint-interval"],
	)
	if *backupTo != "" || *compact {
		application, err := chainabci.NewApplication(chainabci.Config{
			Genesis: genesis, DataDir: *dataDir, Storage: profile,
		})
		if err != nil {
			return err
		}
		if *compact {
			if err := application.CompactStorage(); err != nil {
				_ = application.Close()
				return err
			}
		}
		if *backupTo != "" {
			if err := application.Backup(*backupTo); err != nil {
				_ = application.Close()
				return err
			}
			if err := application.Close(); err != nil {
				return err
			}
			fmt.Fprintf(out, "chainlab application backup created at %s\n", *backupTo)
			return nil
		}
		if err := application.Close(); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "chainlab ABCI listening on %s chain_id=%s storage_mode=%s\n", *listen, genesis.ChainID, profile.Mode)
	logger := cmtlog.NewFilter(cmtlog.NewTMLogger(cmtlog.NewSyncWriter(out)), cmtlog.AllowInfo())
	return chainabci.ServeWithConfig(ctx, *listen, chainabci.Config{
		Genesis: genesis,
		DataDir: *dataDir,
		Storage: profile,
	}, logger)
}

func storageProfileFromFlags(
	mode chainabci.StorageMode,
	retainHeights uint64,
	checkpointInterval uint64,
	retainExplicit bool,
	checkpointExplicit bool,
) chainabci.StorageProfile {
	if !retainExplicit {
		switch mode {
		case chainabci.StorageModePruned:
			retainHeights = 1
		case chainabci.StorageModeArchive:
			retainHeights = 0
		}
	}
	if !checkpointExplicit && mode == chainabci.StorageModePruned {
		checkpointInterval = 0
	}
	return chainabci.StorageProfile{
		Mode: mode, RetainHeights: retainHeights, CheckpointInterval: checkpointInterval,
	}
}

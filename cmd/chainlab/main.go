package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"chainlab/examples"
	"chainlab/internal/crypto"
	"chainlab/internal/node"
	chainrpc "chainlab/internal/rpc"
)

type GenesisFile struct {
	ChainID    string            `json:"chain_id"`
	Proposer   string            `json:"proposer"`
	PrivateKey string            `json:"private_key"`
	Balances   map[string]uint64 `json:"balances"`
}

type nodeOptions struct {
	Listen        string
	PrivateKeyHex string
	GenesisPath   string
	DataDir       string
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "keygen":
		key, err := crypto.GenerateKey()
		if err != nil {
			log.Fatal(err)
		}
		write(map[string]string{
			"address":     crypto.AddressFromPrivateKey(key),
			"private_key": crypto.PrivateKeyToHex(key),
		})
	case "demo":
		summary, err := examples.RunDemo()
		if err != nil {
			log.Fatal(err)
		}
		write(summary)
	case "init":
		initCommand(os.Args[2:])
	case "node":
		nodeCommand(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func initCommand(args []string) {
	flags := flag.NewFlagSet("init", flag.ExitOnError)
	out := flags.String("out", "", "optional genesis output file")
	if err := flags.Parse(args); err != nil {
		log.Fatal(err)
	}
	if *out == "" {
		key, err := crypto.GenerateKey()
		if err != nil {
			log.Fatal(err)
		}
		write(GenesisFile{
			ChainID:    "chainlab-local",
			Proposer:   crypto.AddressFromPrivateKey(key),
			PrivateKey: crypto.PrivateKeyToHex(key),
			Balances: map[string]uint64{
				crypto.AddressFromPrivateKey(key): 1_000_000_000,
			},
		})
		return
	}
	genesis, err := createGenesisFile(*out)
	if err != nil {
		log.Fatal(err)
	}
	write(genesis)
}

func nodeCommand(args []string) {
	flags := flag.NewFlagSet("node", flag.ExitOnError)
	listen := flags.String("listen", ":8547", "HTTP listen address")
	privateKeyHex := flags.String("private-key", "", "proposer private key")
	genesisPath := flags.String("genesis", "", "genesis file path")
	dataDir := flags.String("data-dir", "", "persistent chain data directory")
	if err := flags.Parse(args); err != nil {
		log.Fatal(err)
	}
	config, err := buildNodeConfig(nodeOptions{
		Listen:        *listen,
		PrivateKeyHex: *privateKeyHex,
		GenesisPath:   *genesisPath,
		DataDir:       *dataDir,
	})
	if err != nil {
		log.Fatal(err)
	}
	addr := crypto.AddressFromPrivateKey(config.ProposerKey)
	n, err := node.New(config)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("chainlab node listening on %s proposer=%s\n", *listen, addr)
	log.Fatal(http.ListenAndServe(*listen, chainrpc.NewServer(n)))
}

func buildNodeConfig(options nodeOptions) (node.Config, error) {
	chainID := "chainlab-local"
	balances := make(map[string]uint64)
	privateKeyHex := options.PrivateKeyHex
	if options.GenesisPath != "" {
		genesis, err := readGenesisFile(options.GenesisPath)
		if err != nil {
			return node.Config{}, err
		}
		chainID = genesis.ChainID
		privateKeyHex = genesis.PrivateKey
		balances = genesis.Balances
	}
	var key crypto.PrivateKey
	var err error
	if privateKeyHex == "" {
		key, err = crypto.GenerateKey()
	} else {
		key, err = crypto.PrivateKeyFromHex(privateKeyHex)
	}
	if err != nil {
		return node.Config{}, err
	}
	addr := crypto.AddressFromPrivateKey(key)
	if len(balances) == 0 {
		balances[addr] = 1_000_000_000
	}
	return node.Config{
		ChainID:        chainID,
		ProposerKey:    key,
		GenesisBalance: balances,
		DataDir:        options.DataDir,
	}, nil
}

func usage() {
	fmt.Println("usage: chainlab <keygen|init|demo|node>")
}

func createGenesisFile(path string) (GenesisFile, error) {
	key, err := crypto.GenerateKey()
	if err != nil {
		return GenesisFile{}, err
	}
	proposer := crypto.AddressFromPrivateKey(key)
	genesis := GenesisFile{
		ChainID:    "chainlab-local",
		Proposer:   proposer,
		PrivateKey: crypto.PrivateKeyToHex(key),
		Balances:   map[string]uint64{proposer: 1_000_000_000},
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return GenesisFile{}, err
	}
	raw, err := json.MarshalIndent(genesis, "", "  ")
	if err != nil {
		return GenesisFile{}, err
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return GenesisFile{}, err
	}
	return genesis, nil
}

func readGenesisFile(path string) (GenesisFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return GenesisFile{}, err
	}
	var genesis GenesisFile
	if err := json.Unmarshal(raw, &genesis); err != nil {
		return GenesisFile{}, err
	}
	if genesis.ChainID == "" || genesis.PrivateKey == "" {
		return GenesisFile{}, fmt.Errorf("genesis requires chain_id and private_key")
	}
	if genesis.Balances == nil {
		genesis.Balances = make(map[string]uint64)
	}
	return genesis, nil
}

func write(value any) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(encoded))
}

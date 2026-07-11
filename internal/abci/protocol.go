package abci

import (
	"errors"
	"fmt"
	"math"
	"time"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

func DefaultValidatorPolicy() ValidatorPolicy {
	return ValidatorPolicy{
		EpochLength: 100, DuplicateVoteSlashBasisPoints: 500,
		LightClientAttackSlashBasisPoints: 10_000,
		EvidenceMaxAgeNumBlocks:           100_000,
		EvidenceMaxAgeDurationNanos:       int64(7 * 24 * time.Hour),
	}
}

type ProtocolUpgrade struct {
	Height   int64  `json:"height"`
	Protocol string `json:"protocol"`
}

func validateProtocolUpgrades(genesis GenesisDocument) error {
	if len(genesis.Upgrades) == 0 {
		return nil
	}
	if genesis.Protocol != ProtocolVersionV2 {
		return errors.New("protocol upgrades currently require a chainlab-v2 genesis")
	}
	if len(genesis.Upgrades) > 2 {
		return errors.New("at most two protocol upgrades are supported")
	}
	for index, upgrade := range genesis.Upgrades {
		if upgrade.Height < 2 {
			return errors.New("protocol upgrade height must be at least 2")
		}
		if index > 0 && upgrade.Height <= genesis.Upgrades[index-1].Height {
			return errors.New("protocol upgrade heights must be strictly increasing")
		}
		want := ProtocolVersionV3
		if index == 1 {
			want = ProtocolVersionV4
		}
		if upgrade.Protocol != want {
			return fmt.Errorf("protocol upgrade %d must target %q", index+1, want)
		}
	}
	return nil
}

func protocolAtHeight(genesis GenesisDocument, height int64) string {
	protocol := genesis.Protocol
	for _, upgrade := range genesis.Upgrades {
		if height < upgrade.Height {
			break
		}
		protocol = upgrade.Protocol
	}
	return protocol
}

func appVersionAtHeight(genesis GenesisDocument, height int64) uint64 {
	return appVersion(protocolAtHeight(genesis, height))
}

func nextApplicationHeight(height int64) int64 {
	if height >= math.MaxInt64 {
		return math.MaxInt64
	}
	return height + 1
}

func protocolUsesValidatorLifecycle(protocol string) bool {
	return protocol == ProtocolVersionV2 || protocol == ProtocolVersionV3 || protocol == ProtocolVersionV4
}

func protocolUsesMerkleProofs(protocol string) bool {
	return protocol == ProtocolVersionV3 || protocol == ProtocolVersionV4
}

func protocolUsesSparseState(protocol string) bool {
	return protocol == ProtocolVersionV4
}

func normalizeMaxAppVersion(value uint64) (uint64, error) {
	if value == 0 {
		return AppVersionV4, nil
	}
	if value < AppVersion || value > AppVersionV4 {
		return 0, fmt.Errorf("maximum application version must be between %d and %d", AppVersion, AppVersionV4)
	}
	return value, nil
}

func (a *Application) requireProtocolSupportLocked(height int64, includeNext bool) error {
	required := appVersionAtHeight(a.genesis, height)
	if includeNext && height < math.MaxInt64 {
		required = max(required, appVersionAtHeight(a.genesis, nextApplicationHeight(height)))
	}
	if required > a.maxAppVersion {
		return fmt.Errorf(
			"application binary supports app version %d but height %d requires version %d",
			a.maxAppVersion, height, required,
		)
	}
	return nil
}

func (a *Application) consensusParamUpdatesLocked(height int64) (*cmtproto.ConsensusParams, error) {
	if height >= math.MaxInt64 {
		return nil, nil
	}
	current := appVersionAtHeight(a.genesis, height)
	next := appVersionAtHeight(a.genesis, nextApplicationHeight(height))
	if next == current {
		return nil, nil
	}
	if next > a.maxAppVersion {
		return nil, fmt.Errorf(
			"application binary supports app version %d but scheduled update requires version %d",
			a.maxAppVersion, next,
		)
	}
	return &cmtproto.ConsensusParams{Version: &cmtproto.VersionParams{App: next}}, nil
}

package config

import (
	"time"

	"github.com/cometbft/cometbft/config"
)

const (
	// DefaultListenAddress is a default listen address for P2P client.
	DefaultListenAddress = "/ip4/0.0.0.0/tcp/7676"
	// Version is the current rollkit version
	// Please keep updated with each new release
	Version = "0.38.5"
	// DefaultSequencerAddress
	DefaultSequencerAddress = "localhost:50051"
)

// DefaultNodeConfig keeps default values of NodeConfig
var DefaultNodeConfig = NodeConfig{
	P2P: P2PConfig{
		ListenAddress: DefaultListenAddress,
		Seeds:         "",
	},
	Aggregator: false,
	BlockManagerConfig: BlockManagerConfig{
		BlockTime:      1 * time.Second,
		DABlockTime:    15 * time.Second,
		LazyAggregator: false,
		LazyBlockTime:  60 * time.Second,
	},
	DAAddress:       "http://localhost:26658",
	DAGasPrice:      -1,
	DAGasMultiplier: 0,
	Light:           false,
	HeaderConfig: HeaderConfig{
		TrustedHash: "",
	},
	Instrumentation:   config.DefaultInstrumentationConfig(),
	SequencerAddress:  DefaultSequencerAddress,
	SequencerRollupID: "mock-rollup",
}

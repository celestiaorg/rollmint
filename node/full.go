package node

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	ds "github.com/ipfs/go-datastore"
	ktds "github.com/ipfs/go-datastore/keytransform"
	"github.com/libp2p/go-libp2p/core/crypto"
	"go.uber.org/multierr"

	abci "github.com/cometbft/cometbft/abci/types"
	llcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/service"
	corep2p "github.com/cometbft/cometbft/p2p"
	proxy "github.com/cometbft/cometbft/proxy"
	cmtypes "github.com/cometbft/cometbft/types"

	"github.com/rollkit/rollkit/block"
	"github.com/rollkit/rollkit/config"
	"github.com/rollkit/rollkit/da"
	"github.com/rollkit/rollkit/da/registry"
	"github.com/rollkit/rollkit/mempool"
	mempoolv1 "github.com/rollkit/rollkit/mempool/v1"
	"github.com/rollkit/rollkit/p2p"
	"github.com/rollkit/rollkit/state/indexer"
	blockidxkv "github.com/rollkit/rollkit/state/indexer/block/kv"
	"github.com/rollkit/rollkit/state/txindex"
	"github.com/rollkit/rollkit/state/txindex/kv"
	"github.com/rollkit/rollkit/store"
)

// prefixes used in KV store to separate main node data from DALC data
var (
	mainPrefix    = "0"
	dalcPrefix    = "1"
	indexerPrefix = "2" // indexPrefix uses "i", so using "0-2" to avoid clash
)

const (
	// genesisChunkSize is the maximum size, in bytes, of each
	// chunk in the genesis structure for the chunked API
	genesisChunkSize = 16 * 1024 * 1024 // 16 MiB
)

var _ Node = &FullNode{}

// FullNode represents a client node in Rollkit network.
// It connects all the components and orchestrates their work.
type FullNode struct {
	service.BaseService

	genesis *cmtypes.GenesisDoc
	// cache of chunked genesis data.
	genChunks []string

	nodeConfig config.NodeConfig

	proxyApp     proxy.AppConns
	eventBus     *cmtypes.EventBus
	dalc         da.DataAvailabilityLayerClient
	p2pClient    *p2p.Client
	hSyncService *block.HeaderSynceService
	bSyncService *block.BlockSyncService
	// TODO(tzdybal): consider extracting "mempool reactor"
	Mempool      mempool.Mempool
	mempoolIDs   *mempoolIDs
	Store        store.Store
	blockManager *block.Manager

	// Preserves cometBFT compatibility
	TxIndexer      txindex.TxIndexer
	BlockIndexer   indexer.BlockIndexer
	IndexerService *txindex.IndexerService

	// keep context here only because of API compatibility
	// - it's used in `OnStart` (defined in service.Service interface)
	ctx    context.Context
	cancel context.CancelFunc
}

// newFullNode creates a new Rollkit full node.
func newFullNode(
	ctx context.Context,
	nodeConfig config.NodeConfig,
	p2pKey crypto.PrivKey,
	signingKey crypto.PrivKey,
	clientCreator proxy.ClientCreator,
	genesis *cmtypes.GenesisDoc,
	logger log.Logger,
) (*FullNode, error) {
	proxyApp, err := initProxyApp(clientCreator, logger)
	if err != nil {
		return nil, err
	}

	eventBus, err := initEventBus(logger)
	if err != nil {
		return nil, err
	}

	baseKV, err := initBaseKV(nodeConfig, logger)
	if err != nil {
		return nil, err
	}

	dalcKV := newPrefixKV(baseKV, dalcPrefix)
	dalc, err := initDALC(nodeConfig, dalcKV, logger)
	if err != nil {
		return nil, err
	}

	p2pClient, err := p2p.NewClient(nodeConfig.P2P, p2pKey, genesis.ChainID, baseKV, logger.With("module", "p2p"))
	if err != nil {
		return nil, err
	}

	mainKV := newPrefixKV(baseKV, mainPrefix)
	headerSyncService, err := initHeaderSyncService(ctx, mainKV, nodeConfig, genesis, p2pClient, logger)
	if err != nil {
		return nil, err
	}

	blockSyncService, err := initBlockSyncService(ctx, mainKV, nodeConfig, genesis, p2pClient, logger)
	if err != nil {
		return nil, err
	}

	mempool := initMempool(logger, proxyApp)

	store := store.New(ctx, mainKV)
	blockManager, err := initBlockManager(signingKey, nodeConfig, genesis, store, mempool, proxyApp, dalc, eventBus, logger, blockSyncService)
	if err != nil {
		return nil, err
	}

	indexerKV := newPrefixKV(baseKV, indexerPrefix)
	indexerService, txIndexer, blockIndexer, err := createAndStartIndexerService(ctx, nodeConfig, indexerKV, eventBus, logger)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)

	node := &FullNode{
		proxyApp:       proxyApp,
		eventBus:       eventBus,
		genesis:        genesis,
		nodeConfig:     nodeConfig,
		p2pClient:      p2pClient,
		blockManager:   blockManager,
		dalc:           dalc,
		Mempool:        mempool,
		mempoolIDs:     newMempoolIDs(),
		Store:          store,
		TxIndexer:      txIndexer,
		IndexerService: indexerService,
		BlockIndexer:   blockIndexer,
		hSyncService:   headerSyncService,
		bSyncService:   blockSyncService,
		ctx:            ctx,
		cancel:         cancel,
	}

	node.BaseService = *service.NewBaseService(logger, "Node", node)
	node.p2pClient.SetTxValidator(node.newTxValidator())

	return node, nil
}

func initProxyApp(clientCreator proxy.ClientCreator, logger log.Logger) (proxy.AppConns, error) {
	proxyApp := proxy.NewAppConns(clientCreator, proxy.NopMetrics())
	proxyApp.SetLogger(logger.With("module", "proxy"))
	if err := proxyApp.Start(); err != nil {
		return nil, fmt.Errorf("error while starting proxy app connections: %v", err)
	}
	return proxyApp, nil
}

func initEventBus(logger log.Logger) (*cmtypes.EventBus, error) {
	eventBus := cmtypes.NewEventBus()
	eventBus.SetLogger(logger.With("module", "events"))
	if err := eventBus.Start(); err != nil {
		return nil, err
	}
	return eventBus, nil
}

// initBaseKV initializes the base key-value store.
func initBaseKV(nodeConfig config.NodeConfig, logger log.Logger) (ds.TxnDatastore, error) {
	if nodeConfig.RootDir == "" && nodeConfig.DBPath == "" { // this is used for testing
		logger.Info("WARNING: working in in-memory mode")
		return store.NewDefaultInMemoryKVStore()
	}
	return store.NewDefaultKVStore(nodeConfig.RootDir, nodeConfig.DBPath, "rollkit")
}

func initDALC(nodeConfig config.NodeConfig, dalcKV ds.TxnDatastore, logger log.Logger) (da.DataAvailabilityLayerClient, error) {
	dalc := registry.GetClient(nodeConfig.DALayer)
	if dalc == nil {
		return nil, fmt.Errorf("errror while getting data availability client named '%s'", nodeConfig.DALayer)
	}
	err := dalc.Init(nodeConfig.NamespaceID, []byte(nodeConfig.DAConfig), dalcKV, logger.With("module", "da_client"))
	if err != nil {
		return nil, fmt.Errorf("error while initializing data availability layer client: %w", err)
	}
	return dalc, nil
}

func initMempool(logger log.Logger, proxyApp proxy.AppConns) *mempoolv1.TxMempool {
	mempool := mempoolv1.NewTxMempool(logger, llcfg.DefaultMempoolConfig(), proxyApp.Mempool(), 0)
	mempool.EnableTxsAvailable()
	return mempool
}

func initHeaderSyncService(ctx context.Context, mainKV ds.TxnDatastore, nodeConfig config.NodeConfig, genesis *cmtypes.GenesisDoc, p2pClient *p2p.Client, logger log.Logger) (*block.HeaderSynceService, error) {
	headerSyncService, err := block.NewHeaderSynceService(ctx, mainKV, nodeConfig, genesis, p2pClient, logger.With("module", "HeaderSyncService"))
	if err != nil {
		return nil, fmt.Errorf("error while initializing HeaderSyncService: %w", err)
	}
	return headerSyncService, nil
}

func initBlockSyncService(ctx context.Context, mainKV ds.TxnDatastore, nodeConfig config.NodeConfig, genesis *cmtypes.GenesisDoc, p2pClient *p2p.Client, logger log.Logger) (*block.BlockSyncService, error) {
	blockSyncService, err := block.NewBlockSyncService(ctx, mainKV, nodeConfig, genesis, p2pClient, logger.With("module", "BlockSyncService"))
	if err != nil {
		return nil, fmt.Errorf("error while initializing HeaderSyncService: %w", err)
	}
	return blockSyncService, nil
}

func initBlockManager(signingKey crypto.PrivKey, nodeConfig config.NodeConfig, genesis *cmtypes.GenesisDoc, store store.Store, mempool mempool.Mempool, proxyApp proxy.AppConns, dalc da.DataAvailabilityLayerClient, eventBus *cmtypes.EventBus, logger log.Logger, blockSyncService *block.BlockSyncService) (*block.Manager, error) {
	blockManager, err := block.NewManager(signingKey, nodeConfig.BlockManagerConfig, genesis, store, mempool, proxyApp.Consensus(), dalc, eventBus, logger.With("module", "BlockManager"), blockSyncService.BlockStore())
	if err != nil {
		return nil, fmt.Errorf("error while initializing BlockManager: %w", err)
	}
	return blockManager, nil
}

// initGenesisChunks creates a chunked format of the genesis document to make it easier to
// iterate through larger genesis structures.
func (n *FullNode) initGenesisChunks() error {
	if n.genChunks != nil {
		return nil
	}

	if n.genesis == nil {
		return nil
	}

	data, err := json.Marshal(n.genesis)
	if err != nil {
		return err
	}

	for i := 0; i < len(data); i += genesisChunkSize {
		end := i + genesisChunkSize

		if end > len(data) {
			end = len(data)
		}

		n.genChunks = append(n.genChunks, base64.StdEncoding.EncodeToString(data[i:end]))
	}

	return nil
}

func (n *FullNode) headerPublishLoop(ctx context.Context) {
	for {
		select {
		case signedHeader := <-n.blockManager.HeaderCh:
			err := n.hSyncService.WriteToHeaderStoreAndBroadcast(ctx, signedHeader)
			if err != nil {
				// failed to init or start headerstore
				n.Logger.Error(err.Error())
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func (n *FullNode) blockPublishLoop(ctx context.Context) {
	for {
		select {
		case block := <-n.blockManager.BlockCh:
			err := n.bSyncService.WriteToBlockStoreAndBroadcast(ctx, block)
			if err != nil {
				// failed to init or start blockstore
				n.Logger.Error(err.Error())
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// Cancel calls the underlying context's cancel function.
func (n *FullNode) Cancel() {
	n.cancel()
}

// OnStart is a part of Service interface.
func (n *FullNode) OnStart() error {
	n.Logger.Info("starting P2P client")
	err := n.p2pClient.Start(n.ctx)
	if err != nil {
		return fmt.Errorf("error while starting P2P client: %w", err)
	}

	if err = n.hSyncService.Start(); err != nil {
		return fmt.Errorf("error while starting header sync service: %w", err)
	}

	if err = n.bSyncService.Start(); err != nil {
		return fmt.Errorf("error while starting block sync service: %w", err)
	}

	if err = n.dalc.Start(); err != nil {
		return fmt.Errorf("error while starting data availability layer client: %w", err)
	}

	if n.nodeConfig.Aggregator {
		n.Logger.Info("working in aggregator mode", "block time", n.nodeConfig.BlockTime)
		go n.blockManager.AggregationLoop(n.ctx, n.nodeConfig.LazyAggregator)
		go n.blockManager.BlockSubmissionLoop(n.ctx)
		go n.headerPublishLoop(n.ctx)
		go n.blockPublishLoop(n.ctx)
	}
	go n.blockManager.RetrieveLoop(n.ctx)
	go n.blockManager.BlockStoreRetrieveLoop(n.ctx)
	go n.blockManager.SyncLoop(n.ctx, n.cancel)
	return nil
}

// GetGenesis returns entire genesis doc.
func (n *FullNode) GetGenesis() *cmtypes.GenesisDoc {
	return n.genesis
}

// GetGenesisChunks returns chunked version of genesis.
func (n *FullNode) GetGenesisChunks() ([]string, error) {
	err := n.initGenesisChunks()
	if err != nil {
		return nil, err
	}
	return n.genChunks, err
}

// OnStop is a part of Service interface.
func (n *FullNode) OnStop() {
	n.Logger.Info("halting full node...")
	n.cancel()
	err := n.dalc.Stop()
	err = multierr.Append(err, n.p2pClient.Close())
	err = multierr.Append(err, n.hSyncService.Stop())
	err = multierr.Append(err, n.bSyncService.Stop())
	n.Logger.Error("errors while stopping node:", "errors", err)
}

// OnReset is a part of Service interface.
func (n *FullNode) OnReset() error {
	panic("OnReset - not implemented!")
}

// SetLogger sets the logger used by node.
func (n *FullNode) SetLogger(logger log.Logger) {
	n.Logger = logger
}

// GetLogger returns logger.
func (n *FullNode) GetLogger() log.Logger {
	return n.Logger
}

// EventBus gives access to Node's event bus.
func (n *FullNode) EventBus() *cmtypes.EventBus {
	return n.eventBus
}

// AppClient returns ABCI proxy connections to communicate with application.
func (n *FullNode) AppClient() proxy.AppConns {
	return n.proxyApp
}

// newTxValidator creates a pubsub validator that uses the node's mempool to check the
// transaction. If the transaction is valid, then it is added to the mempool
func (n *FullNode) newTxValidator() p2p.GossipValidator {
	return func(m *p2p.GossipMessage) bool {
		n.Logger.Debug("transaction received", "bytes", len(m.Data))
		checkTxResCh := make(chan *abci.Response, 1)
		err := n.Mempool.CheckTx(m.Data, func(resp *abci.Response) {
			select {
			case <-n.ctx.Done():
				return
			case checkTxResCh <- resp:
			}
		}, mempool.TxInfo{
			SenderID:    n.mempoolIDs.GetForPeer(m.From),
			SenderP2PID: corep2p.ID(m.From),
		})
		switch {
		case errors.Is(err, mempool.ErrTxInCache):
			return true
		case errors.Is(err, mempool.ErrMempoolIsFull{}):
			return true
		case errors.Is(err, mempool.ErrTxTooLarge{}):
			return false
		case errors.Is(err, mempool.ErrPreCheck{}):
			return false
		default:
		}
		res := <-checkTxResCh
		checkTxResp := res.GetCheckTx()

		return checkTxResp.Code == abci.CodeTypeOK
	}
}

func newPrefixKV(kvStore ds.Datastore, prefix string) ds.TxnDatastore {
	return (ktds.Wrap(kvStore, ktds.PrefixTransform{Prefix: ds.NewKey(prefix)}).Children()[0]).(ds.TxnDatastore)
}

func createAndStartIndexerService(
	ctx context.Context,
	conf config.NodeConfig,
	kvStore ds.TxnDatastore,
	eventBus *cmtypes.EventBus,
	logger log.Logger,
) (*txindex.IndexerService, txindex.TxIndexer, indexer.BlockIndexer, error) {
	var (
		txIndexer    txindex.TxIndexer
		blockIndexer indexer.BlockIndexer
	)

	txIndexer = kv.NewTxIndex(ctx, kvStore)
	blockIndexer = blockidxkv.New(ctx, newPrefixKV(kvStore, "block_events"))

	indexerService := txindex.NewIndexerService(ctx, txIndexer, blockIndexer, eventBus)
	indexerService.SetLogger(logger.With("module", "txindex"))

	if err := indexerService.Start(); err != nil {
		return nil, nil, nil, err
	}

	return indexerService, txIndexer, blockIndexer, nil
}

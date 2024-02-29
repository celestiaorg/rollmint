package block

import (
	"context"
	"errors"
	"fmt"
	"github.com/rollkit/rollkit/third_party/log"
	"strconv"
	"sync/atomic"

	ds "github.com/ipfs/go-datastore"

	"github.com/rollkit/rollkit/store"

	"github.com/rollkit/rollkit/types"
)

// LastSubmittedHeightKey is the key used for persisting the last submitted height in store.
const LastSubmittedHeightKey = "last submitted"

// PendingBlocks maintains blocks that need to be published to DA layer
//
// Important assertions:
// - blocks are safely stored in database before submission to DA
// - blocks are always pushed to DA in order (by height)
// - DA submission of multiple blocks is atomic - it's impossible to submit only part of a batch
//
// lastSubmittedHeight is updated only after receiving confirmation from DA.
// Worst case scenario is when blocks was successfully submitted to DA, but confirmation was not received (e.g. node was
// restarted, networking issue occurred). In this case blocks are re-submitted to DA (it's extra cost).
// rollkit is able to skip duplicate blocks so this shouldn't affect full nodes.
// TODO(tzdybal): batch size
type PendingBlocks struct {
	store  store.Store
	logger log.Logger

	// lastSubmittedHeight holds information about last block successfully submitted to DA
	lastSubmittedHeight atomic.Uint64
}

// NewPendingBlocks returns a new PendingBlocks struct
func NewPendingBlocks(store store.Store, logger log.Logger) *PendingBlocks {
	return &PendingBlocks{
		store:  store,
		logger: logger,
	}
}

// getPendingBlocks returns a sorted slice of pending blocks
// that need to be published to DA layer in order of block height
func (pb *PendingBlocks) getPendingBlocks(ctx context.Context) ([]*types.Block, error) {
	lastSubmitted := pb.lastSubmittedHeight.Load()
	if lastSubmitted == 0 {
		err := pb.loadFromStore(ctx)
		if err != nil {
			return nil, err
		}
		lastSubmitted = pb.lastSubmittedHeight.Load()
	}
	height := pb.store.Height()

	if lastSubmitted == height {
		return nil, nil
	}
	if lastSubmitted > height {
		panic(fmt.Sprintf("height of last block submitted to DA (%d) is greater than height of last block (%d)",
			lastSubmitted, height))
	}

	blocks := make([]*types.Block, 0, height-lastSubmitted)
	for i := lastSubmitted + 1; i <= height; i++ {
		block, err := pb.store.GetBlock(ctx, i)
		if err != nil {
			// return as much as possible + error information
			return blocks, err
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

func (pb *PendingBlocks) isEmpty() bool {
	return pb.store.Height() == pb.lastSubmittedHeight.Load()
}

// TODO(tzdybal): change signature (accept height, maybe context?)
func (pb *PendingBlocks) removeSubmittedBlocks(blocks []*types.Block) {
	if len(blocks) == 0 {
		return
	}
	latestBlockHeight := blocks[len(blocks)-1].Height()
	lsh := pb.lastSubmittedHeight.Load()

	if latestBlockHeight > lsh {
		if pb.lastSubmittedHeight.CompareAndSwap(lsh, latestBlockHeight) {
			err := pb.store.SetMetadata(context.TODO(), LastSubmittedHeightKey, []byte(strconv.FormatUint(latestBlockHeight, 10)))
			pb.logger.Error("failed to store height of latest block submitted to DA", "err", err)
		}
	}
}

func (pb *PendingBlocks) loadFromStore(ctx context.Context) error {
	raw, err := pb.store.GetMetadata(ctx, LastSubmittedHeightKey)
	if errors.Is(err, ds.ErrNotFound) {
		// LastSubmittedHeightKey was never used, it's special case not actual error
		// we don't need to modify lastSubmittedHeight
		return nil
	}
	if err != nil {
		return err
	}
	lsh, err := strconv.ParseUint(string(raw), 10, 64)
	if err != nil {
		return err
	}
	pb.lastSubmittedHeight.CompareAndSwap(0, lsh)
	return nil
}

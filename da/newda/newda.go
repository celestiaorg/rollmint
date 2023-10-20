package newda

import (
	"context"
	"encoding/binary"

	pb "github.com/rollkit/rollkit/types/pb/rollkit"

	"github.com/gogo/protobuf/proto"
	ds "github.com/ipfs/go-datastore"

	newda "github.com/rollkit/go-da"
	"github.com/rollkit/rollkit/da"
	"github.com/rollkit/rollkit/third_party/log"
	"github.com/rollkit/rollkit/types"
)

type NewDA struct {
	DA     newda.DA
	logger log.Logger
}

func (n *NewDA) Init(namespaceID types.NamespaceID, config []byte, kvStore ds.Datastore, logger log.Logger) error {
	n.logger = logger
	return nil
}

func (n *NewDA) Start() error {
	return nil
}

func (n *NewDA) Stop() error {
	return nil
}

func (n *NewDA) SubmitBlocks(ctx context.Context, blocks []*types.Block) da.ResultSubmitBlocks {
	blobs := make([][]byte, len(blocks))
	for i := range blocks {
		blob, err := blocks[i].MarshalBinary()
		if err != nil {
			return da.ResultSubmitBlocks{
				BaseResult: da.BaseResult{
					Code:    da.StatusError,
					Message: "failed to serialize block",
				},
			}
		}
		blobs[i] = blob
	}
	ids, _, err := n.DA.Submit(blobs)
	if err != nil {
		return da.ResultSubmitBlocks{
			BaseResult: da.BaseResult{
				Code:    da.StatusError,
				Message: "failed to submit blocks: " + err.Error(),
			},
		}
	}

	return da.ResultSubmitBlocks{
		BaseResult: da.BaseResult{
			Code:     da.StatusSuccess,
			DAHeight: binary.LittleEndian.Uint64(ids[0]),
		},
	}
}

func (n *NewDA) RetrieveBlocks(ctx context.Context, dataLayerHeight uint64) da.ResultRetrieveBlocks {
	ids, err := n.DA.GetIDs(dataLayerHeight)
	if err != nil {

	}

	blobs, err := n.DA.Get(ids)
	if err != nil {

	}

	blocks := make([]*types.Block, len(blobs))
	for i, blob := range blobs {
		var block pb.Block
		err = proto.Unmarshal(blob, &block)
		if err != nil {
			n.logger.Error("failed to unmarshal block", "daHeight", dataLayerHeight, "position", i, "error", err)
			continue
		}
		blocks[i] = new(types.Block)
		err := blocks[i].FromProto(&block)
		if err != nil {
			return da.ResultRetrieveBlocks{
				BaseResult: da.BaseResult{
					Code:    da.StatusError,
					Message: err.Error(),
				},
			}
		}
	}

	return da.ResultRetrieveBlocks{
		BaseResult: da.BaseResult{
			Code:     da.StatusSuccess,
			DAHeight: dataLayerHeight,
		},
		Blocks: blocks,
	}
}

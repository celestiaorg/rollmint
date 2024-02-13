package da

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"google.golang.org/grpc/credentials/insecure"

	"github.com/rollkit/go-da"
	"github.com/rollkit/go-da/proxy"
	goDATest "github.com/rollkit/go-da/test"
	"github.com/rollkit/rollkit/types"
)

const mockDaBlockTime = 100 * time.Millisecond

func TestMain(m *testing.M) {
	srv := startMockGRPCServ()
	if srv == nil {
		os.Exit(1)
	}
	exitCode := m.Run()

	// teardown servers
	srv.GracefulStop()

	os.Exit(exitCode)
}

// MockDA is a mock for the DA interface
type MockDA struct {
	mock.Mock
}

func (m *MockDA) MaxBlobSize(ctx context.Context) (uint64, error) {
	args := m.Called()
	return args.Get(0).(uint64), args.Error(1)
}

func (m *MockDA) Get(ctx context.Context, ids []da.ID, ns da.Namespace) ([]da.Blob, error) {
	args := m.Called(ids)
	return args.Get(0).([]da.Blob), args.Error(1)
}

func (m *MockDA) GetIDs(ctx context.Context, height uint64, ns da.Namespace) ([]da.ID, error) {
	args := m.Called(height)
	return args.Get(0).([]da.ID), args.Error(1)
}

func (m *MockDA) Commit(ctx context.Context, blobs []da.Blob, ns da.Namespace) ([]da.Commitment, error) {
	args := m.Called(blobs)
	return args.Get(0).([]da.Commitment), args.Error(1)
}

func (m *MockDA) Submit(ctx context.Context, blobs []da.Blob, gasPrice float64, ns da.Namespace) ([]da.ID, error) {
	args := m.Called(blobs, gasPrice)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return args.Get(0).([]da.ID), args.Error(1)
	}
}

func (m *MockDA) GetProofs(ctx context.Context, ids []da.ID, ns da.Namespace) ([]da.Proof, error) {
	args := m.Called(ids)
	return args.Get(0).([]da.Proof), args.Error(1)
}

func (m *MockDA) Validate(ctx context.Context, ids []da.ID, proofs []da.Proof, ns da.Namespace) ([]bool, error) {
	args := m.Called(ids, proofs)
	return args.Get(0).([]bool), args.Error(1)
}

func TestMockDAErrors(t *testing.T) {
	t.Run("submit_timeout", func(t *testing.T) {
		mockDA := &MockDA{}
		dalc := &DAClient{DA: mockDA, GasPrice: -1, Logger: log.TestingLogger()}
		blocks := []*types.Block{types.GetRandomBlock(1, 0)}
		var blobs []da.Blob
		for _, block := range blocks {
			blockBytes, err := block.MarshalBinary()
			require.NoError(t, err)
			blobs = append(blobs, blockBytes)
		}
		// Set up the mock to throw context deadline exceeded
		mockDA.On("MaxBlobSize").Return(uint64(1234), nil)
		mockDA.
			On("Submit", blobs, float64(-1)).
			After(100*time.Millisecond).
			Return([]da.ID{bytes.Repeat([]byte{0x00}, 8)}, []da.Proof{[]byte("proof")}, nil)
		doTestSubmitTimeout(t, dalc, blocks)
	})
	t.Run("max_blob_size_error", func(t *testing.T) {
		mockDA := &MockDA{}
		dalc := &DAClient{DA: mockDA, GasPrice: -1, Logger: log.TestingLogger()}
		// Set up the mock to return an error for MaxBlobSize
		mockDA.On("MaxBlobSize").Return(uint64(0), errors.New("mock error"))
		doTestMaxBlockSizeError(t, dalc)
	})
	t.Run("retrieve_no_blocks_found", func(t *testing.T) {
		mockDA := &MockDA{}
		dalc := &DAClient{DA: mockDA, GasPrice: -1, Logger: log.TestingLogger()}
		// Set Mock DA to return empty IDs
		mockDA.On("GetIDs", mock.Anything, mock.Anything).Return([]da.ID{}, nil)
		doTestRetrieveNoBlocksFound(t, dalc)
	})
}

func TestSubmitRetrieve(t *testing.T) {
	dummyClient := &DAClient{DA: goDATest.NewDummyDA(), GasPrice: -1, Logger: log.TestingLogger()}
	grpcClient, err := startMockGRPCClient()
	require.NoError(t, err)
	clients := map[string]*DAClient{
		"dummy": dummyClient,
		"grpc":  grpcClient,
	}
	tests := []struct {
		name string
		f    func(t *testing.T, dalc *DAClient)
	}{
		{"submit_retrieve", doTestSubmitRetrieve},
		{"submit_empty_blocks", doTestSubmitEmptyBlocks},
		{"submit_over_sized_block", doTestSubmitOversizedBlock},
		{"submit_small_blocks_batch", doTestSubmitSmallBlocksBatch},
		{"submit_large_blocks_overflow", doTestSubmitLargeBlocksOverflow},
	}
	for name, dalc := range clients {
		for _, tc := range tests {
			t.Run(name+"_"+tc.name, func(t *testing.T) {
				tc.f(t, dalc)
			})
		}
	}
}

func startMockGRPCServ() *grpc.Server {
	srv := proxy.NewServer(goDATest.NewDummyDA(), grpc.Creds(insecure.NewCredentials()))
	lis, err := net.Listen("tcp", "127.0.0.1"+":"+strconv.Itoa(7980))
	if err != nil {
		fmt.Println(err)
		return nil
	}
	go func() {
		_ = srv.Serve(lis)
	}()
	return srv
}

func startMockGRPCClient() (*DAClient, error) {
	client := proxy.NewClient()
	err := client.Start("127.0.0.1:7980", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &DAClient{DA: client, GasPrice: -1, Logger: log.TestingLogger()}, nil
}

func doTestSubmitTimeout(t *testing.T, dalc *DAClient, blocks []*types.Block) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	assert := assert.New(t)
	submitTimeout = 50 * time.Millisecond
	resp := dalc.SubmitBlocks(ctx, blocks)
	assert.Contains(resp.Message, "context deadline exceeded", "should return context timeout error")
}

func doTestMaxBlockSizeError(t *testing.T, dalc *DAClient) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	assert := assert.New(t)
	resp := dalc.SubmitBlocks(ctx, []*types.Block{})
	assert.Contains(resp.Message, "unable to get DA max blob size", "should return max blob size error")
}

func doTestRetrieveNoBlocksFound(t *testing.T, dalc *DAClient) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	assert := assert.New(t)
	result := dalc.RetrieveBlocks(ctx, 123)
	assert.Equal(StatusNotFound, result.Code, "should return not found")
	assert.Contains(result.Message, "blob: not found")
}

func doTestSubmitRetrieve(t *testing.T, dalc *DAClient) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require := require.New(t)
	assert := assert.New(t)

	const numBatches = 10
	const numBlocks = 10

	blockToDAHeight := make(map[*types.Block]uint64)
	countAtHeight := make(map[uint64]int)

	submitAndRecordBlocks := func(blocks []*types.Block) {
		for len(blocks) > 0 {
			resp := dalc.SubmitBlocks(ctx, blocks)
			assert.Equal(StatusSuccess, resp.Code, resp.Message)

			for _, block := range blocks[:resp.SubmittedCount] {
				blockToDAHeight[block] = resp.DAHeight
				countAtHeight[resp.DAHeight]++
			}
			blocks = blocks[resp.SubmittedCount:]
		}
	}

	for batch := uint64(0); batch < numBatches; batch++ {
		blocks := make([]*types.Block, numBlocks)
		for i := range blocks {
			blocks[i] = types.GetRandomBlock(batch*numBatches+uint64(i), rand.Int()%20) //nolint:gosec
		}
		submitAndRecordBlocks(blocks)
		time.Sleep(time.Duration(rand.Int63() % mockDaBlockTime.Milliseconds())) //nolint:gosec
	}

	validateBlockRetrieval := func(height uint64, expectedCount int) {
		t.Log("Retrieving block, DA Height", height)
		ret := dalc.RetrieveBlocks(ctx, height)
		assert.Equal(StatusSuccess, ret.Code, ret.Message)
		require.NotEmpty(ret.Blocks, height)
		assert.Len(ret.Blocks, expectedCount, height)
	}

	for height, count := range countAtHeight {
		validateBlockRetrieval(height, count)
	}

	for block, height := range blockToDAHeight {
		ret := dalc.RetrieveBlocks(ctx, height)
		assert.Equal(StatusSuccess, ret.Code, height)
		require.NotEmpty(ret.Blocks, height)
		assert.Contains(ret.Blocks, block, height)
	}
}

func doTestSubmitEmptyBlocks(t *testing.T, dalc *DAClient) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	assert := assert.New(t)

	block1 := types.GetRandomBlock(1, 0)
	block2 := types.GetRandomBlock(1, 0)
	resp := dalc.SubmitBlocks(ctx, []*types.Block{block1, block2})
	assert.Equal(StatusSuccess, resp.Code, "empty blocks should submit")
	assert.EqualValues(resp.SubmittedCount, 2, "empty blocks should batch")
}

func doTestSubmitOversizedBlock(t *testing.T, dalc *DAClient) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require := require.New(t)
	assert := assert.New(t)

	limit, err := dalc.DA.MaxBlobSize(ctx)
	require.NoError(err)
	oversizedBlock := types.GetRandomBlock(1, int(limit))
	resp := dalc.SubmitBlocks(ctx, []*types.Block{oversizedBlock})
	assert.Equal(StatusError, resp.Code, "oversized block should throw error")
	assert.Contains(resp.Message, "failed to submit blocks: oversized block: blob: over size limit")
}

func doTestSubmitSmallBlocksBatch(t *testing.T, dalc *DAClient) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	assert := assert.New(t)

	block1 := types.GetRandomBlock(1, 1)
	block2 := types.GetRandomBlock(1, 2)
	resp := dalc.SubmitBlocks(ctx, []*types.Block{block1, block2})
	assert.Equal(StatusSuccess, resp.Code, "small blocks should submit")
	assert.EqualValues(resp.SubmittedCount, 2, "small blocks should batch")
}

func doTestSubmitLargeBlocksOverflow(t *testing.T, dalc *DAClient) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require := require.New(t)
	assert := assert.New(t)

	limit, err := dalc.DA.MaxBlobSize(ctx)
	require.NoError(err)

	// two large blocks, over blob limit to force partial submit
	var block1, block2 *types.Block
	for i := 0; ; i += 10 {
		block1 = types.GetRandomBlock(1, i)
		blob1, err := block1.MarshalBinary()
		require.NoError(err)

		block2 = types.GetRandomBlock(1, i)
		blob2, err := block2.MarshalBinary()
		require.NoError(err)

		if uint64(len(blob1)+len(blob2)) > limit {
			break
		}
	}

	// overflowing blocks submit partially
	resp := dalc.SubmitBlocks(ctx, []*types.Block{block1, block2})
	assert.Equal(StatusSuccess, resp.Code, "overflowing blocks should submit partially")
	assert.EqualValues(1, resp.SubmittedCount, "submitted count should be partial")

	// retry remaining blocks
	resp = dalc.SubmitBlocks(ctx, []*types.Block{block2})
	assert.Equal(StatusSuccess, resp.Code, "remaining blocks should submit")
	assert.EqualValues(resp.SubmittedCount, 1, "submitted count should match")
}

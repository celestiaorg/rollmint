# P2P Block Sync

## Abstract

P2P Block Sync enables rollkit full nodes including aggregators to gossip blocks amongst 
themselves and sync with the rollup chain faster than they can sync using the DA layer.

```mermaid
sequenceDiagram
    title P2P Block Sync

    participant User
    participant Block Producer
    participant Full Node 1
    participant Full Node 2
    participant DA Layer

    User->>Block Producer: Send Tx
    Block Producer->>Block Producer: Generate Block
    Block Producer->>DA Layer: Publish Block

    Block Producer->>Full Node 1: Gossip Block
    Block Producer->>Full Node 2: Gossip Block
    Full Node 1->>Full Node 1: Verify Block
    Full Node 1->>Full Node 2: Gossip Block
    Full Node 1->>Full Node 1: Mark Block Soft-Confirmed

    Full Node 2->>Full Node 2: Verify Block
    Full Node 2->>Full Node 2: Mark Block Soft-Confirmed

    DA Layer->>Full Node 1: Retrieve Block
    Full Node 1->>Full Node 1: Mark Block Hard-Confirmed

    DA Layer->>Full Node 2: Retrieve Block
    Full Node 2->>Full Node 2: Mark Block Hard-Confirmed
```

## Protocol/Component Description

P2P Block Sync consists of the following components:

* block exchange service: responsible for gossiping blocks over P2P
* block publication to P2P network
* block retrieval from P2P network

### Block Exchange Service

### Block Publication to P2P network

Blocks ready to be published to the P2P network are sent to the `BlockCh` channel in Block Manager inside `publishLoop`.
The `blockPublishLoop` in the full node continuously listens for new blocks from the `BlockCh` channel and when a new block 
is received, it is written to the block store and broadcasted to the network using the block exchange service.

### Block Retrieval from P2P network

BlockStoreRetrieveLoop is a function that is responsible for retrieving blocks from the Block Store.
The function listens for two types of events:
1. When the context is done, it returns and stops the loop.
2. When a signal is received from the blockStoreCh channel, it retrieves blocks from the block store.
The function keeps track of the last retrieved block's height (lastBlockStoreHeight).
If the current block store's height (blockStoreHeight) is greater than the last retrieved block's height,
it retrieves all blocks from the block store that are between these two heights.
If there is an error while retrieving blocks, it logs the error and continues with the next iteration.
For each retrieved block, it sends a new block event to the blockInCh channel.
The new block event contains the block and the current DA (Data Availability) layer's height.
After all blocks are retrieved and sent, it updates the last retrieved block's height to the current block store's height.


Offer a comprehensive explanation of the protocol, covering aspects such as data
flow, communication mechanisms, and any other details necessary for
understanding the inner workings of this component.

## Message Structure/Communication Format

If this particular component is expected to communicate over the network,
outline the structure of the message protocol, including details such as field
interpretation, message format, and any other relevant information.

## Assumptions and Considerations

If there are any assumptions required for the component's correct operation,
performance, security, or other expected features, outline them here.
Additionally, provide any relevant considerations related to security or other
concerns.

## Implementation

The `blockStore` in `BlockExchangeService` ([node/block_exchange.go](https://github.com/rollkit/rollkit/blob/main/node/block_exchange.go)) is used when initializing a full node ([node/full.go](https://github.com/rollkit/rollkit/blob/main/node/full.go)). Blocks are written to `blockStore` in `blockPublishLoop` in full node ([node/full.go](https://github.com/rollkit/rollkit/blob/main/node/full.go)), gossiped amongst the network, and retrieved in `BlockStoreRetrieveLoop` in Block Manager ([block/manager.go](https://github.com/rollkit/rollkit/blob/main/block/manager.go)).


## References

List any references used or cited in the document.
//
// Copyright 2021, Offchain Labs, Inc. All rights reserved.
//

package main

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/offchainlabs/arbstate/arbos"
	"github.com/offchainlabs/arbstate/arbstate"
	"github.com/offchainlabs/arbstate/wavmio"
)

func getBlockHeaderByHash(hash common.Hash) *types.Header {
	enc := wavmio.ResolvePreImage(hash)
	header := &types.Header{}
	err := rlp.DecodeBytes(enc, &header)
	if err != nil {
		panic(fmt.Sprintf("Error parsing resolved block header: %v", err))
	}
	return header
}

type WavmChainContext struct{}

func (c WavmChainContext) Engine() consensus.Engine {
	return arbos.Engine{}
}

func (c WavmChainContext) GetHeader(hash common.Hash, num uint64) *types.Header {
	header := getBlockHeaderByHash(hash)
	if !header.Number.IsUint64() || header.Number.Uint64() != num {
		panic(fmt.Sprintf("Retrieved wrong block number for header hash %v -- requested %v but got %v", hash, num, header.Number.String()))
	}
	return header
}

type WavmInbox struct{}

func (i WavmInbox) PeekSequencerInbox() ([]byte, error) {
	return wavmio.ReadInboxMessage(), nil
}

func (i WavmInbox) GetSequencerInboxPosition() uint64 {
	return wavmio.GetInboxPosition()
}

func (i WavmInbox) AdvanceSequencerInbox() {
	wavmio.AdvanceInboxMessage()
}

func (i WavmInbox) GetPositionWithinMessage() uint64 {
	return wavmio.GetPositionWithinMessage()
}

func (i WavmInbox) SetPositionWithinMessage(pos uint64) {
	wavmio.SetPositionWithinMessage(pos)
}

func (i WavmInbox) ReadDelayedInbox(seqNum uint64) ([]byte, error) {
	return wavmio.ReadDelayedInboxMessage(seqNum), nil
}

func BuildBlock(statedb *state.StateDB, lastBlockHeader *types.Header, chainContext core.ChainContext, inbox arbstate.InboxBackend) (*types.Block, error) {
	var delayedMessagesRead uint64
	if lastBlockHeader != nil {
		delayedMessagesRead = lastBlockHeader.Nonce.Uint64()
	}
	inboxMultiplexer := arbstate.NewInboxMultiplexer(inbox, delayedMessagesRead)
	blockBuilder := arbos.NewBlockBuilder(lastBlockHeader, statedb, chainContext, nil, nil, nil)
	for {
		message, err := inboxMultiplexer.Peek()
		if err != nil {
			return nil, err
		}
		segment, err := arbos.IncomingMessageToSegment(message.Message, arbos.ChainConfig.ChainID)
		if err != nil {
			log.Warn("error parsing incoming message", "err", err)
			err = inboxMultiplexer.Advance()
			if err != nil {
				return nil, err
			}
			break
		}
		// Always passes if the block is empty
		if !blockBuilder.ShouldAddMessage(segment) {
			break
		}
		err = inboxMultiplexer.Advance()
		if err != nil {
			return nil, err
		}
		blockBuilder.AddMessage(segment)
		if message.MustEndBlock {
			break
		}
	}
	block, _, _ := blockBuilder.ConstructBlock(inboxMultiplexer.DelayedMessagesRead())
	return block, nil
}

func main() {
	raw := rawdb.NewDatabase(PreimageDb{})
	db := state.NewDatabase(raw)
	lastBlockHash := wavmio.GetLastBlockHash()

	fmt.Printf("Previous block hash: %v\n", lastBlockHash)
	var lastBlockHeader *types.Header
	var lastBlockStateRoot common.Hash
	if lastBlockHash != (common.Hash{}) {
		lastBlockHeader = getBlockHeaderByHash(lastBlockHash)
		lastBlockStateRoot = lastBlockHeader.Root
	}

	fmt.Printf("Previous block state root: %v\n", lastBlockStateRoot)
	statedb, err := state.New(lastBlockStateRoot, db, nil)
	if err != nil {
		panic(fmt.Sprintf("Error opening state db: %v", err.Error()))
	}

	chainContext := WavmChainContext{}
	newBlock, err := BuildBlock(statedb, lastBlockHeader, chainContext, WavmInbox{})
	if err != nil {
		panic(fmt.Sprintf("Error building block: %v", err.Error()))
	}
	if newBlock == nil {
		// failed to parse message, move on without creating block
		return
	}

	fmt.Printf("New state root: %v\n", newBlock.Root())
	newBlockHash := newBlock.Hash()
	fmt.Printf("New block hash: %v\n", newBlockHash)

	wavmio.SetLastBlockHash(newBlockHash)
}

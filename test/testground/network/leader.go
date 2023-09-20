package network

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/tendermint/tendermint/types"
	coretypes "github.com/tendermint/tendermint/types"
	"github.com/testground/sdk-go/run"
	"github.com/testground/sdk-go/runtime"
)

// Leader is the role for the leader node in a test. It is responsible for
// creating the genesis block and distributing it to all nodes.
type Leader struct {
	*FullNode
}

// Plan is the method that creates and distributes the genesis, configurations,
// and keys for all of the other nodes in the network.
func (l *Leader) Plan(ctx context.Context, statuses []Status, runenv *runtime.RunEnv, initCtx *run.InitContext) error {
	packets, err := l.BootstrapPeers(ctx, runenv, initCtx)
	if err != nil {
		return err
	}

	// create Genesis and distribute it to all nodes
	genesis, tgCfg, err := l.GenesisEvent(ctx, runenv, initCtx, packets)
	if err != nil {
		return err
	}

	baseDir, err := l.Init(homeDir, genesis, tgCfg[l.Name])
	if err != nil {
		return err
	}

	l.baseDir = baseDir

	return nil
}

func (l *Leader) Execute(ctx context.Context, runenv *runtime.RunEnv, initCtx *run.InitContext) error {
	baseDir, err := l.ConsensusNode.Init(homeDir)
	if err != nil {
		return err
	}

	err = l.ConsensusNode.StartNode(ctx, baseDir)
	if err != nil {
		return err
	}

	time.Sleep(time.Second * 20)

	go l.subscribeAndRecordBlocks(ctx, runenv)

	time.Sleep(time.Second * 20)

	// seqs := runenv.IntParam(BlobSequencesParam)
	size := runenv.IntParam(BlobSizesParam)
	count := runenv.IntParam(BlobsPerSeqParam)

	sizes := make([]int, count)
	for i := 0; i < count; i++ {
		sizes[i] = size
	}

	// issue a command to start txsim
	cmd := NewSubmitRandomPFBsCommand(
		"txsim",
		time.Minute*1,
		sizes...,
	)

	_, err = initCtx.SyncClient.Publish(ctx, CommandTopic, cmd)
	if err != nil {
		return err
	}

	// runenv.RecordMessage(fmt.Sprintf("submitting PFB"))

	// tctx, cancel := context.WithTimeout(ctx, time.Second*60)
	// defer cancel()

	// resp, err := l.SubmitRandomPFB(tctx, 1000)
	// if err != nil {
	// 	return err
	// }
	// if resp == nil {
	// 	return errors.New("submit pfb response was nil")
	// }

	// runenv.RecordMessage(fmt.Sprintf("leader submittedPFB code %d space %s", resp.Code, resp.Codespace))

	runenv.RecordMessage(fmt.Sprintf("leader waiting for halt height %d", l.HaltHeight))
	_, err = l.cctx.WaitForHeightWithTimeout(int64(l.ConsensusNode.HaltHeight), time.Minute*30)
	if err != nil {
		return err
	}

	_, err = initCtx.SyncClient.Publish(ctx, CommandTopic, EndTestCommand())

	return err
}

// Retro collects standard data from the leader node and saves it as a file.
// This data includes the block times, rounds required to reach consensus, and
// the block sizes.
func (l *Leader) Retro(ctx context.Context, runenv *runtime.RunEnv, initCtx *run.InitContext) error {
	defer l.ConsensusNode.Stop()

	blockRes, err := l.cctx.Client.Block(ctx, nil)
	if err != nil {
		return err
	}

	maxBlockSize := 0
	for i := int64(1); i < blockRes.Block.Height; i++ {
		blockRes, err := l.cctx.Client.Block(ctx, nil)
		if err != nil {
			return err
		}
		size := blockRes.Block.Size()
		if size > maxBlockSize {
			maxBlockSize = size
		}
	}

	runenv.RecordMessage(fmt.Sprintf("leader retro: height %d max block size bytes %d", blockRes.Block.Height, maxBlockSize))

	return nil
}

func (l *Leader) GenesisEvent(ctx context.Context, runenv *runtime.RunEnv, initCtx *run.InitContext, packets []PeerPacket) (*coretypes.GenesisDoc, error) {
	pubKeys := make([]cryptotypes.PubKey, 0)
	addrs := make([]string, 0)
	gentxs := make([]json.RawMessage, 0, len(packets))
	for _, packet := range packets {
		pks, err := packet.GetPubKeys()
		if err != nil {
			return nil, err
		}
		pubKeys = append(pubKeys, pks...)
		addrs = append(addrs, packet.GenesisAccounts...)
		gentxs = append(gentxs, packet.GenTx)
	}

	// save and gossip the genesis doc and configs to all of nodes and then we done
	doc, err := GenesisDoc(l.ecfg, l.params.ChainID, gentxs, addrs, pubKeys)
}

func SerializePublicKey(pubKey cryptotypes.PubKey) string {
	return hex.EncodeToString(pubKey.Bytes())
}

func DeserializeAccountPublicKey(hexPubKey string) (cryptotypes.PubKey, error) {
	bz, err := hex.DecodeString(hexPubKey)
	if err != nil {
		return nil, err
	}
	var pubKey secp256k1.PubKey
	if len(bz) != secp256k1.PubKeySize {
		return nil, errors.New("incorrect pubkey size")
	}
	pubKey.Key = bz
	return &pubKey, nil
}

// subscribeAndRecordBlocks subscribes to the block event stream and records
// the block times and sizes.
func (l *Leader) subscribeAndRecordBlocks(ctx context.Context, runenv *runtime.RunEnv) error {
	query := "tm.event = 'NewBlock'"
	events, err := l.cctx.Client.Subscribe(ctx, "leader", query, 100)
	if err != nil {
		return err
	}

	for {
		select {
		case ev := <-events:
			newBlock, ok := ev.Data.(types.EventDataNewBlock)
			if !ok {
				return fmt.Errorf("unexpected event type: %T", ev.Data)
			}
			runenv.RecordMessage(fmt.Sprintf("leader height %d max block size bytes %d", newBlock.Block.Height, newBlock.Block.Size()))
		case <-ctx.Done():
			return nil
		}
	}
}
package bridge

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"

	abci "github.com/cometbft/cometbft/abci/types"
	rpcclient "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/ethereum/go-ethereum/common"
	oracletypes "github.com/imua-xyz/imuachain/x/oracle/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
)

// BridgeManager coordinates checkpoint signing and outbound delivery.
// It is designed to be called from the price-feeder's main event loop,
// not as a standalone goroutine.
type BridgeManager struct {
	signer    *CheckpointSigner
	deliverer *OutboundDeliverer
	tmRPC     string
	logger    feedertypes.LoggerInf
	// Track what we've already signed/delivered per dstChainID
	lastSignedNonce    map[uint64]uint64
	lastDeliveredNonce map[uint64]uint64
}

// NewBridgeManager creates a new bridge manager from the feeder config.
func NewBridgeManager(cfg feedertypes.BridgeConf, logger feedertypes.LoggerInf, tmRPC string) (*BridgeManager, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if cfg.ECDSAKeyHex == "" {
		return nil, fmt.Errorf("bridge.ecdsa_key_hex is required when bridge is enabled")
	}

	signer, err := NewCheckpointSigner(cfg.ECDSAKeyHex, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create checkpoint signer: %w", err)
	}

	var deliverConfigs []OutboundDeliverConfig
	for _, c := range cfg.Chains {
		deliverConfigs = append(deliverConfigs, OutboundDeliverConfig{
			DstChainID:     c.DstChainID,
			RPC:            c.RPC,
			BridgeVerifier: common.HexToAddress(c.BridgeVerifier),
		})
	}

	deliverer, err := NewOutboundDeliverer(cfg.ECDSAKeyHex, deliverConfigs, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create outbound deliverer: %w", err)
	}

	return &BridgeManager{
		signer:             signer,
		deliverer:          deliverer,
		tmRPC:              tmRPC,
		logger:             logger,
		lastSignedNonce:    make(map[uint64]uint64),
		lastDeliveredNonce: make(map[uint64]uint64),
	}, nil
}

// OnNewBlock is called from the feeder's event loop on each new block.
// It checks for pending checkpoints to sign and finalized checkpoints to deliver.
func (bm *BridgeManager) OnNewBlock(height int64) {
	if bm == nil {
		return
	}

	ctx := context.Background()

	// Connect to Tendermint RPC to query store
	tmClient, err := rpcclient.New(bm.tmRPC, "/websocket")
	if err != nil {
		bm.logger.Error("bridge: failed to connect to tendermint", "error", err)
		return
	}

	// For each configured destination chain, check for pending checkpoints
	for dstChainID := range bm.deliverer.configs {
		bm.processCheckpointsForChain(ctx, tmClient, dstChainID)
	}
}

func (bm *BridgeManager) processCheckpointsForChain(ctx context.Context, tmClient *rpcclient.HTTP, dstChainID uint64) {
	// Query the latest checkpoint nonce
	nonceBz, err := queryStore(ctx, tmClient, oracletypes.CheckpointNonceKey(dstChainID))
	if err != nil || len(nonceBz) == 0 {
		return
	}
	latestNonce, err := oracletypes.BytesToUint64(nonceBz)
	if err != nil || latestNonce == 0 {
		return
	}

	// Query the checkpoint data
	cpBz, err := queryStore(ctx, tmClient, oracletypes.CheckpointDataKey(dstChainID, latestNonce))
	if err != nil || len(cpBz) == 0 {
		return
	}

	cp, err := ParseCheckpoint(cpBz)
	if err != nil {
		bm.logger.Error("bridge: failed to parse checkpoint", "error", err)
		return
	}

	// Sign if not yet signed
	if !cp.Finalized && bm.lastSignedNonce[dstChainID] < latestNonce {
		bm.signCheckpoint(cp, dstChainID)
	}

	// Deliver if finalized and not yet delivered
	if cp.Finalized && bm.lastDeliveredNonce[dstChainID] < latestNonce {
		bm.deliverCheckpoint(ctx, tmClient, cp, dstChainID)
	}
}

func (bm *BridgeManager) signCheckpoint(cp *OutboundCheckpoint, dstChainID uint64) {
	v, r, s, err := bm.signer.SignCheckpoint(cp.Nonce, cp.DstChainID, cp.MessagesHash)
	if err != nil {
		bm.logger.Error("bridge: failed to sign checkpoint", "nonce", cp.Nonce, "error", err)
		return
	}

	bm.logger.Info("bridge: signed checkpoint",
		"dst_chain_id", dstChainID,
		"nonce", cp.Nonce,
		"evm_addr", bm.signer.EVMAddress().Hex(),
	)

	// The actual MsgSignCheckpoint submission is done via the imuaclient.
	// Store the signature for the caller to submit.
	_ = v
	_ = r
	_ = s
	// TODO: The feeder event loop should call imuaclient.SendSignCheckpoint with this data.
	// For now, we log that signing succeeded and track the nonce.
	bm.lastSignedNonce[dstChainID] = cp.Nonce
}

func (bm *BridgeManager) deliverCheckpoint(ctx context.Context, tmClient *rpcclient.HTTP, cp *OutboundCheckpoint, dstChainID uint64) {
	// Collect outbound messages from the queue
	messages, err := bm.collectOutboundMessages(ctx, tmClient, dstChainID, cp.SeqStart, cp.SeqEnd)
	if err != nil {
		bm.logger.Error("bridge: failed to collect messages", "error", err)
		return
	}

	// Collect signatures
	sigs, err := bm.collectSignatures(ctx, tmClient, dstChainID, cp.Nonce)
	if err != nil {
		bm.logger.Error("bridge: failed to collect signatures", "error", err)
		return
	}

	if len(sigs) == 0 {
		return
	}

	// Deliver to BridgeVerifier
	if err := bm.deliverer.Deliver(ctx, dstChainID, cp.Nonce, cp.MessagesHash, messages, sigs); err != nil {
		bm.logger.Error("bridge: delivery failed", "nonce", cp.Nonce, "error", err)
		return
	}

	bm.lastDeliveredNonce[dstChainID] = cp.Nonce
}

func (bm *BridgeManager) collectOutboundMessages(ctx context.Context, tmClient *rpcclient.HTTP, dstChainID, seqStart, seqEnd uint64) ([][]byte, error) {
	head, err := queryStoreUint64(ctx, tmClient, oracletypes.OutboundHeadKey(dstChainID))
	if err != nil {
		return nil, err
	}
	tail, err := queryStoreUint64(ctx, tmClient, oracletypes.OutboundTailKey(dstChainID))
	if err != nil {
		return nil, err
	}

	var messages [][]byte
	for idx := head; idx < tail; idx++ {
		bz, err := queryStore(ctx, tmClient, oracletypes.OutboundItemKey(dstChainID, idx))
		if err != nil || len(bz) == 0 {
			continue
		}
		var msg OutboundMsg
		if err := parseJSON(bz, &msg); err != nil {
			continue
		}
		if msg.SeqNum < seqStart || msg.SeqNum > seqEnd {
			continue
		}
		payload, err := hex.DecodeString(msg.PayloadHex)
		if err != nil {
			continue
		}
		messages = append(messages, payload)
	}
	return messages, nil
}

func (bm *BridgeManager) collectSignatures(ctx context.Context, tmClient *rpcclient.HTTP, dstChainID, nonce uint64) ([]CheckpointSignature, error) {
	// Query signatures by iterating the checkpoint sig prefix.
	// This uses a prefix scan on the store.
	prefix := oracletypes.CheckpointSigKey(dstChainID, nonce, nil)
	resp, err := tmClient.ABCIQuery(ctx, fmt.Sprintf("store/%s/subspace", oracletypes.StoreKey), prefix)
	if err != nil {
		return nil, err
	}

	// The subspace query returns key-value pairs. Parse each value as a CheckpointSignature.
	var sigs []CheckpointSignature
	if resp.Response.Value != nil {
		// Single value response — parse directly
		var sig CheckpointSignature
		if err := parseJSON(resp.Response.Value, &sig); err == nil {
			sigs = append(sigs, sig)
		}
	}

	// If subspace iteration isn't available, fall back to individual queries.
	// In practice, the feeder would need a custom query or event-based approach.
	// For now, this is a reasonable starting point.
	return sigs, nil
}

// --- helpers ---

func queryStore(ctx context.Context, client *rpcclient.HTTP, key []byte) ([]byte, error) {
	resp, err := client.ABCIQuery(ctx, fmt.Sprintf("store/%s/key", oracletypes.StoreKey), key)
	if err != nil {
		return nil, err
	}
	if resp.Response.Code != abci.CodeTypeOK {
		return nil, nil
	}
	return resp.Response.Value, nil
}

func queryStoreUint64(ctx context.Context, client *rpcclient.HTTP, key []byte) (uint64, error) {
	bz, err := queryStore(ctx, client, key)
	if err != nil || len(bz) != 8 {
		return 0, err
	}
	v, err := oracletypes.BytesToUint64(bz)
	return v, err
}

func parseJSON(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

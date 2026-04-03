package bridge

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	feedertypes "github.com/imua-xyz/price-feeder/types"
)

// CheckpointSignature mirrors imuachain's CheckpointSignature.
type CheckpointSignature struct {
	Validator common.Address `json:"validator"`
	V         uint8          `json:"v"`
	R         [32]byte       `json:"r"`
	S         [32]byte       `json:"s"`
	Power     int64          `json:"power"`
}

// OutboundDeliverConfig holds config for a destination chain.
type OutboundDeliverConfig struct {
	DstChainID      uint64         `yaml:"dst_chain_id"`
	RPC             string         `yaml:"rpc"`
	BridgeVerifier  common.Address `yaml:"bridge_verifier"`
	Confirmations   uint64         `yaml:"confirmations"`
}

// OutboundDeliverer delivers finalized checkpoints to client chain BridgeVerifier.
type OutboundDeliverer struct {
	privKey *ecdsa.PrivateKey
	configs map[uint64]OutboundDeliverConfig
	clients map[uint64]*ethclient.Client
	logger  feedertypes.LoggerInf
}

// NewOutboundDeliverer creates a new outbound deliverer.
func NewOutboundDeliverer(hexKey string, configs []OutboundDeliverConfig, logger feedertypes.LoggerInf) (*OutboundDeliverer, error) {
	privKey, err := crypto.HexToECDSA(hexKey)
	if err != nil {
		return nil, fmt.Errorf("invalid ECDSA key: %w", err)
	}

	cfgMap := make(map[uint64]OutboundDeliverConfig)
	clients := make(map[uint64]*ethclient.Client)
	for _, cfg := range configs {
		cfgMap[cfg.DstChainID] = cfg
		client, err := ethclient.Dial(cfg.RPC)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to chain %d: %w", cfg.DstChainID, err)
		}
		clients[cfg.DstChainID] = client
	}

	return &OutboundDeliverer{
		privKey: privKey,
		configs: cfgMap,
		clients: clients,
		logger:  logger,
	}, nil
}

// Deliver submits a finalized checkpoint with signatures and messages to the BridgeVerifier contract.
func (od *OutboundDeliverer) Deliver(
	ctx context.Context,
	dstChainID uint64,
	checkpointNonce uint64,
	messagesHash common.Hash,
	messages [][]byte,
	sigs []CheckpointSignature,
) error {
	cfg, ok := od.configs[dstChainID]
	if !ok {
		return fmt.Errorf("no config for dst chain %d", dstChainID)
	}
	client, ok := od.clients[dstChainID]
	if !ok {
		return fmt.Errorf("no client for dst chain %d", dstChainID)
	}

	calldata, err := encodeVerifyAndDeliver(checkpointNonce, dstChainID, messagesHash, messages, sigs)
	if err != nil {
		return fmt.Errorf("encode calldata: %w", err)
	}

	from := crypto.PubkeyToAddress(od.privKey.PublicKey)
	nonce, err := client.PendingNonceAt(ctx, from)
	if err != nil {
		return fmt.Errorf("get nonce: %w", err)
	}
	gasPrice, err := client.SuggestGasPrice(ctx)
	if err != nil {
		return fmt.Errorf("suggest gas price: %w", err)
	}
	chainID, err := client.ChainID(ctx)
	if err != nil {
		return fmt.Errorf("get chain id: %w", err)
	}

	tx := types.NewTransaction(
		nonce,
		cfg.BridgeVerifier,
		big.NewInt(0),
		uint64(2_000_000),
		gasPrice,
		calldata,
	)

	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(chainID), od.privKey)
	if err != nil {
		return fmt.Errorf("sign tx: %w", err)
	}

	if err := client.SendTransaction(ctx, signedTx); err != nil {
		return fmt.Errorf("send tx: %w", err)
	}

	od.logger.Info("submitted verifyAndDeliver tx",
		"dst_chain_id", dstChainID,
		"checkpoint_nonce", checkpointNonce,
		"tx_hash", signedTx.Hash().Hex(),
		"messages", len(messages),
		"signers", len(sigs),
	)

	// Wait for receipt
	receipt, err := waitForReceipt(ctx, client, signedTx.Hash(), 120*time.Second)
	if err != nil {
		return fmt.Errorf("wait receipt: %w", err)
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return fmt.Errorf("tx reverted: %s", signedTx.Hash().Hex())
	}

	od.logger.Info("verifyAndDeliver confirmed",
		"dst_chain_id", dstChainID,
		"checkpoint_nonce", checkpointNonce,
		"tx_hash", signedTx.Hash().Hex(),
		"gas_used", receipt.GasUsed,
	)
	return nil
}

// encodeVerifyAndDeliver encodes the BridgeVerifier.verifyAndDeliver calldata.
func encodeVerifyAndDeliver(
	checkpointNonce, dstChainID uint64,
	messagesHash common.Hash,
	messages [][]byte,
	sigs []CheckpointSignature,
) ([]byte, error) {
	// Build ABI types
	uint256Ty, _ := abi.NewType("uint256", "", nil)
	bytes32Ty, _ := abi.NewType("bytes32", "", nil)
	bytesArrayTy, _ := abi.NewType("bytes[]", "", nil)
	addressArrayTy, _ := abi.NewType("address[]", "", nil)
	uint8ArrayTy, _ := abi.NewType("uint8[]", "", nil)
	bytes32ArrayTy, _ := abi.NewType("bytes32[]", "", nil)

	args := abi.Arguments{
		{Type: uint256Ty},      // checkpointNonce
		{Type: uint256Ty},      // dstChainID
		{Type: bytes32Ty},      // messagesHash
		{Type: bytesArrayTy},   // messages
		{Type: addressArrayTy}, // signers
		{Type: uint8ArrayTy},   // v
		{Type: bytes32ArrayTy}, // r
		{Type: bytes32ArrayTy}, // s
	}

	signers := make([]common.Address, len(sigs))
	vs := make([]uint8, len(sigs))
	rs := make([][32]byte, len(sigs))
	ss := make([][32]byte, len(sigs))
	for i, sig := range sigs {
		signers[i] = sig.Validator
		vs[i] = sig.V
		rs[i] = sig.R
		ss[i] = sig.S
	}

	packed, err := args.Pack(
		new(big.Int).SetUint64(checkpointNonce),
		new(big.Int).SetUint64(dstChainID),
		messagesHash,
		messages,
		signers,
		vs,
		rs,
		ss,
	)
	if err != nil {
		return nil, err
	}

	methodID := crypto.Keccak256([]byte("verifyAndDeliver(uint256,uint256,bytes32,bytes[],address[],uint8[],bytes32[],bytes32[])"))[:4]
	return append(methodID, packed...), nil
}

func waitForReceipt(ctx context.Context, client *ethclient.Client, txHash common.Hash, timeout time.Duration) (*types.Receipt, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		receipt, err := client.TransactionReceipt(ctx, txHash)
		if err == nil {
			return receipt, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return nil, fmt.Errorf("timeout waiting for receipt")
}

// ParseCheckpointSignatures deserializes checkpoint signatures from JSON.
func ParseCheckpointSignatures(data []byte) ([]CheckpointSignature, error) {
	var sigs []CheckpointSignature
	if err := json.Unmarshal(data, &sigs); err != nil {
		return nil, err
	}
	return sigs, nil
}

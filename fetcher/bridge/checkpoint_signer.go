// Package bridge implements checkpoint signing and outbound delivery for the oracle bridge.
// It integrates into the price-feeder's event loop rather than running as a standalone service.
package bridge

import (
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	feedertypes "github.com/imua-xyz/price-feeder/types"
)

// CheckpointSigner handles ECDSA signing of outbound checkpoints.
type CheckpointSigner struct {
	privKey    *ecdsa.PrivateKey
	evmAddr    common.Address
	logger     feedertypes.LoggerInf
}

// NewCheckpointSigner creates a checkpoint signer from a hex-encoded ECDSA private key.
func NewCheckpointSigner(hexKey string, logger feedertypes.LoggerInf) (*CheckpointSigner, error) {
	privKey, err := crypto.HexToECDSA(hexKey)
	if err != nil {
		return nil, fmt.Errorf("invalid ECDSA key: %w", err)
	}
	addr := crypto.PubkeyToAddress(privKey.PublicKey)
	logger.Info("checkpoint signer initialized", "evm_address", addr.Hex())
	return &CheckpointSigner{
		privKey: privKey,
		evmAddr: addr,
		logger:  logger,
	}, nil
}

// EVMAddress returns the signer's EVM address.
func (cs *CheckpointSigner) EVMAddress() common.Address {
	return cs.evmAddr
}

// BridgeID must match the BRIDGE_ID constant in BridgeVerifier.sol and imuachain.
const BridgeID = uint64(1)

// SignCheckpoint signs a checkpoint hash with the ECDSA key.
// Returns (v, r, s) where v is 27 or 28.
func (cs *CheckpointSigner) SignCheckpoint(nonce, dstChainID uint64, messagesHash common.Hash) (uint8, [32]byte, [32]byte, error) {
	checkpointHash := computeCheckpointHash(nonce, dstChainID, messagesHash)
	ethSignedHash := computeEthSignedMessageHash(checkpointHash)

	sig, err := crypto.Sign(ethSignedHash.Bytes(), cs.privKey)
	if err != nil {
		return 0, [32]byte{}, [32]byte{}, fmt.Errorf("ecdsa sign: %w", err)
	}

	var r, s [32]byte
	copy(r[:], sig[0:32])
	copy(s[:], sig[32:64])
	v := sig[64] + 27 // Ethereum convention: v is 27 or 28

	return v, r, s, nil
}

// OutboundMsg mirrors the oracle module's OutboundMsg JSON structure.
type OutboundMsg struct {
	DstChainID uint64 `json:"dst_chain_id"`
	SeqNum     uint64 `json:"seq_num"`
	Nonce      uint64 `json:"nonce"`
	PayloadHex string `json:"payload_hex"`
	Height     int64  `json:"height"`
}

// OutboundCheckpoint mirrors the oracle module's OutboundCheckpoint.
type OutboundCheckpoint struct {
	Nonce        uint64      `json:"nonce"`
	DstChainID   uint64      `json:"dst_chain_id"`
	MessagesHash common.Hash `json:"messages_hash"`
	SeqStart     uint64      `json:"seq_start"`
	SeqEnd       uint64      `json:"seq_end"`
	Height       int64       `json:"height"`
	Finalized    bool        `json:"finalized"`
}

// ParseCheckpoint deserializes a checkpoint from JSON bytes.
func ParseCheckpoint(data []byte) (*OutboundCheckpoint, error) {
	var cp OutboundCheckpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, err
	}
	return &cp, nil
}

// --- Hash computation (must match imuachain/x/oracle/types/checkpoint.go) ---

func computeCheckpointHash(nonce, dstChainID uint64, messagesHash common.Hash) common.Hash {
	data := make([]byte, 0, 128)
	data = append(data, common.BigToHash(new(big.Int).SetUint64(BridgeID)).Bytes()...)
	data = append(data, common.BigToHash(new(big.Int).SetUint64(nonce)).Bytes()...)
	data = append(data, common.BigToHash(new(big.Int).SetUint64(dstChainID)).Bytes()...)
	data = append(data, messagesHash.Bytes()...)
	return crypto.Keccak256Hash(data)
}

func computeEthSignedMessageHash(hash common.Hash) common.Hash {
	prefix := []byte("\x19Ethereum Signed Message:\n32")
	return crypto.Keccak256Hash(append(prefix, hash.Bytes()...))
}

// FormatEVMAddress returns the hex-encoded address without 0x prefix (40 chars).
func FormatEVMAddress(addr common.Address) string {
	return hex.EncodeToString(addr.Bytes())
}

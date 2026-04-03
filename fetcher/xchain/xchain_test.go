package xchain

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"
)

const testABI = `[
  {
    "anonymous": false,
    "inputs": [
      { "indexed": false, "internalType": "uint64", "name": "nonce", "type": "uint64" },
      { "indexed": false, "internalType": "bytes32", "name": "msgId", "type": "bytes32" },
      { "indexed": false, "internalType": "bytes", "name": "payload", "type": "bytes" }
    ],
    "name": "XChainMessage",
    "type": "event"
  }
]`

func newTestSource(t *testing.T) *source {
	t.Helper()
	parsedABI, err := abi.JSON(strings.NewReader(testABI))
	require.NoError(t, err)
	evt, ok := parsedABI.Events["XChainMessage"]
	require.True(t, ok)
	return &source{
		abi:   parsedABI,
		event: evt,
	}
}

func makeLog(t *testing.T, s *source, nonce uint64, msgID [32]byte, payload []byte, block uint64, index uint) types.Log {
	t.Helper()
	data, err := s.event.Inputs.Pack(nonce, msgID, payload)
	require.NoError(t, err)
	return types.Log{
		BlockNumber: block,
		Index:       index,
		Topics:      []common.Hash{s.event.ID},
		Data:        data,
	}
}

func TestParseMessagesSingle(t *testing.T) {
	s := newTestSource(t)
	var msgID [32]byte
	copy(msgID[:], []byte("test-msg-id"))
	payload := []byte{0x01, 0x02, 0x03}

	logs := []types.Log{
		makeLog(t, s, 1, msgID, payload, 10, 0),
	}
	cfg := TokenConfig{
		StartBlock:  0,
		MaxBytes:   1024,
		MaxMessages: 10,
	}

	msgs, lastBlock, lastNonce, err := s.parseMessages(cfg, logs, 1)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Equal(t, uint64(10), lastBlock)
	require.Equal(t, uint64(1), lastNonce)
	require.Equal(t, uint64(1), msgs[0].Nonce)
	require.Equal(t, "0x"+hex.EncodeToString(msgID[:]), msgs[0].ID)
	require.Equal(t, defaultEventType, msgs[0].Type)
}

func TestParseMessagesNonceGapStops(t *testing.T) {
	s := newTestSource(t)
	var msgID1, msgID2 [32]byte
	copy(msgID1[:], []byte("msg-1"))
	copy(msgID2[:], []byte("msg-3"))

	logs := []types.Log{
		makeLog(t, s, 1, msgID1, []byte{0x01}, 10, 0),
		makeLog(t, s, 3, msgID2, []byte{0x02}, 11, 0),
	}
	cfg := TokenConfig{
		StartBlock:  0,
		MaxBytes:   1024,
		MaxMessages: 10,
	}

	msgs, lastBlock, lastNonce, err := s.parseMessages(cfg, logs, 1)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Equal(t, uint64(10), lastBlock)
	require.Equal(t, uint64(1), lastNonce)
	require.Equal(t, uint64(1), msgs[0].Nonce)
}

func TestParseMessagesMaxBytes(t *testing.T) {
	s := newTestSource(t)
	var msgID1, msgID2 [32]byte
	copy(msgID1[:], []byte("msg-small"))
	copy(msgID2[:], []byte("msg-large"))

	logs := []types.Log{
		makeLog(t, s, 1, msgID1, []byte{0x01}, 10, 0),
		makeLog(t, s, 2, msgID2, make([]byte, 2048), 10, 1),
	}
	cfg := TokenConfig{
		StartBlock:  0,
		MaxBytes:   200,
		MaxMessages: 10,
	}

	msgs, _, lastNonce, err := s.parseMessages(cfg, logs, 1)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Equal(t, uint64(1), lastNonce)
}

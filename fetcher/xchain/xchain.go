package xchain

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	fetchertypes "github.com/imua-xyz/price-feeder/fetcher/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
)

const (
	defaultMaxMessages = 100
	defaultMaxBytes    = 2 * 1024 * 1024
	defaultMaxBlocks   = 2000
	defaultEventType   = "evm"
)

type xchainMessage struct {
	ID         string `json:"id"`
	Nonce      uint64 `json:"nonce"`
	Type       string `json:"type"`
	PayloadB64 string `json:"payload_b64"`
}

type xchainBatch struct {
	SrcChainID uint64          `json:"src_chain_id"`
	BatchSeq   uint64          `json:"batch_seq"`
	Messages   []xchainMessage `json:"messages"`
}

type eventData struct {
	Nonce   uint64   `abi:"nonce"`
	MsgId   [32]byte `abi:"msgId"`
	Payload []byte   `abi:"payload"`
}

func (s *source) fetch(token string) (*fetchertypes.PriceInfo, error) {
	cfg, ok := s.getTokenConfig(token)
	if !ok {
		return nil, feedertypes.ErrSourceTokenNotConfigured.Wrap(fmt.Sprintf("token %s not configured", token))
	}
	client, err := s.getClient(token, cfg)
	if err != nil {
		return nil, err
	}
	state := s.getState(token, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	latest, err := client.BlockNumber(ctx)
	if err != nil {
		return nil, err
	}
	if latest == 0 || latest <= cfg.Confirmations {
		return &fetchertypes.PriceInfo{}, nil
	}
	toBlock := latest - cfg.Confirmations
	if toBlock < state.nextBlock {
		if state.lastPrice != nil {
			return state.lastPrice, nil
		}
		return &fetchertypes.PriceInfo{}, nil
	}

	maxBlocks := cfg.MaxBlocks
	if maxBlocks == 0 {
		maxBlocks = defaultMaxBlocks
	}
	if toBlock-state.nextBlock+1 > maxBlocks {
		toBlock = state.nextBlock + maxBlocks - 1
	}

	gatewayAddr := common.HexToAddress(cfg.Gateway)
	logs, err := client.FilterLogs(ctx, ethereum.FilterQuery{
		FromBlock: big.NewInt(int64(state.nextBlock)),
		ToBlock:   big.NewInt(int64(toBlock)),
		Addresses: []common.Address{gatewayAddr},
		Topics:    [][]common.Hash{{s.event.ID}},
	})
	if err != nil {
		return nil, err
	}
	if len(logs) == 0 {
		state.nextBlock = toBlock + 1
		if state.lastPrice != nil {
			return state.lastPrice, nil
		}
		return &fetchertypes.PriceInfo{}, nil
	}

	messages, lastBlock, lastNonce, err := s.parseMessages(cfg, logs, state.nextNonce)
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		state.nextBlock = toBlock + 1
		if state.lastPrice != nil {
			return state.lastPrice, nil
		}
		return &fetchertypes.PriceInfo{}, nil
	}

	batch := xchainBatch{
		SrcChainID: cfg.SrcChainID,
		BatchSeq:   state.batchSeq,
		Messages:   messages,
	}
	rawData, err := json.Marshal(batch)
	if err != nil {
		return nil, err
	}

	state.nextNonce = lastNonce + 1
	state.batchSeq += 1
	state.nextBlock = lastBlock

	price := fetchertypes.PriceInfo{
		Price:     string(rawData),
		Decimal:   0,
		Timestamp: time.Now().UTC().Format(feedertypes.TimeLayout),
		RoundID:   fmt.Sprintf("%d", batch.BatchSeq),
	}
	state.lastPrice = &price
	return &price, nil
}

func (s *source) parseMessages(cfg TokenConfig, logs []types.Log, nextNonce uint64) ([]xchainMessage, uint64, uint64, error) {
	sort.Slice(logs, func(i, j int) bool {
		if logs[i].BlockNumber != logs[j].BlockNumber {
			return logs[i].BlockNumber < logs[j].BlockNumber
		}
		return logs[i].Index < logs[j].Index
	})

	maxMessages := cfg.MaxMessages
	if maxMessages == 0 {
		maxMessages = defaultMaxMessages
	}
	maxBytes := cfg.MaxBytes
	if maxBytes == 0 {
		maxBytes = defaultMaxBytes
	}

	totalBytes := 0
	messages := make([]xchainMessage, 0, maxMessages)
	var lastBlock uint64 = cfg.StartBlock
	var lastNonce uint64 = nextNonce - 1

	for _, lg := range logs {
		evt, err := s.decodeEvent(lg)
		if err != nil {
			s.logger.Error("failed to decode xchain event", "error", err, "tx_hash", lg.TxHash.Hex())
			continue
		}
		if evt.Nonce < nextNonce {
			lastBlock = lg.BlockNumber
			continue
		}
		if evt.Nonce > nextNonce {
			break
		}

		msg := xchainMessage{
			ID:         formatMsgID(evt.MsgId),
			Nonce:      evt.Nonce,
			Type:       defaultEventType,
			PayloadB64: base64.StdEncoding.EncodeToString(evt.Payload),
		}

		estimatedSize := len(msg.ID) + len(msg.Type) + len(msg.PayloadB64) + 64
		if len(messages) >= maxMessages || totalBytes+estimatedSize > maxBytes {
			break
		}
		messages = append(messages, msg)
		totalBytes += estimatedSize
		lastBlock = lg.BlockNumber
		lastNonce = evt.Nonce
		nextNonce = evt.Nonce + 1
	}

	return messages, lastBlock, lastNonce, nil
}

func (s *source) decodeEvent(lg types.Log) (*eventData, error) {
	out := &eventData{}

	if err := s.abi.UnpackIntoInterface(out, s.event.Name, lg.Data); err != nil {
		return nil, err
	}
	return out, nil
}

func formatMsgID(id [32]byte) string {
	return "0x" + hex.EncodeToString(id[:])
}

package imuaclient

import (
	"context"
	"errors"
	"fmt"
	"time"

	oracletypes "github.com/imua-xyz/imuachain/x/oracle/types"
	fetchertypes "github.com/imua-xyz/price-feeder/fetcher/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"

	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"

	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdktx "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
)

// SendTx signs a create-price transaction and send it to imuad
func (ec imuaClient) SendTx(feederID, baseBlock uint64, price fetchertypes.PriceInfo, nonce int32) (*sdktx.BroadcastTxResponse, error) {
	msg, txBytes, err := ec.getSignedTxBytes(feederID, baseBlock, price, nonce)
	if err != nil {
		return nil, err
	}
	// broadcast txBytes
	res, err := ec.txClient.BroadcastTx(
		context.Background(),
		&sdktx.BroadcastTxRequest{
			Mode:    sdktx.BroadcastMode_BROADCAST_MODE_SYNC,
			TxBytes: txBytes,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to braodcast transaction, msg:%v, valConsAddr:%s, error:%w", msg, sdk.ConsAddress(ec.pubKey.Address()), err)
	}
	return res, nil
}

func (ec imuaClient) SendTx2Phases(feederID, baseBlock uint64, prices []*fetchertypes.PriceInfo, phase oracletypes.AggregationPhase, nonce int32) (*sdktx.BroadcastTxResponse, error) {
	msg, txBytes, err := ec.getSignedTxBytes2Phases(feederID, baseBlock, prices, phase, nonce)
	if err != nil {
		return nil, err
	}
	res, err := ec.txClient.BroadcastTx(
		context.Background(),
		&sdktx.BroadcastTxRequest{
			Mode:    sdktx.BroadcastMode_BROADCAST_MODE_SYNC,
			TxBytes: txBytes,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to braodcast transaction, msg:%v, valConsAddr:%s, error:%w", msg, sdk.ConsAddress(ec.pubKey.Address()), err)
	}

	return res, nil
}

func (ec imuaClient) SendTxDebug(feederID, baseBlock uint64, price fetchertypes.PriceInfo, nonce int32) (*coretypes.ResultBroadcastTxCommit, error) {
	msg, txBytes, err := ec.getSignedTxBytes(feederID, baseBlock, price, nonce)
	if err != nil {
		return nil, err
	}
	// broadcast txBytes
	res, err := ec.txClientDebug.BroadcastTxCommit(context.Background(), txBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to braodcast transaction, msg:%v, valConsAddr:%s, error:%w", msg, sdk.ConsAddress(ec.pubKey.Address()), err)
	}
	return res, nil
}

// signMsg signs the message with consensusskey
func (ec imuaClient) signMsg(msgs ...sdk.Msg) (authsigning.Tx, error) {
	txBuilder := ec.txCfg.NewTxBuilder()
	_ = txBuilder.SetMsgs(msgs...)
	txBuilder.SetGasLimit(blockMaxGas)
	txBuilder.SetFeeAmount(sdk.Coins{sdk.NewInt64Coin(denom, 0)})

	if err := txBuilder.SetSignatures(ec.getSignature(nil)); err != nil {
		ec.logger.Error("failed to SetSignatures", "errro", err)
		return nil, err
	}

	bytesToSign, err := ec.getSignBytes(txBuilder.GetTx())
	if err != nil {
		return nil, fmt.Errorf("failed to getSignBytes, error:%w", err)
	}
	sigBytes, err := ec.privKey.Sign(bytesToSign)
	if err != nil {
		ec.logger.Error("failed to sign txBytes", "error", err)
		return nil, err
	}
	_ = txBuilder.SetSignatures(ec.getSignature(sigBytes))
	return txBuilder.GetTx(), nil
}

// getSignBytes reteive the bytes from tx for signing
func (ec imuaClient) getSignBytes(tx authsigning.Tx) ([]byte, error) {
	b, err := ec.txCfg.SignModeHandler().GetSignBytes(
		ec.txCfg.SignModeHandler().DefaultMode(),
		authsigning.SignerData{
			ChainID: ec.chainID,
		},
		tx,
	)
	if err != nil {
		return nil, fmt.Errorf("Get bytesToSign fail, %w", err)
	}

	return b, nil
}

// getSignature assembles a siging.SignatureV2 structure
func (ec imuaClient) getSignature(sigBytes []byte) signing.SignatureV2 {
	signature := signing.SignatureV2{
		PubKey: ec.pubKey,
		Data: &signing.SingleSignatureData{
			SignMode:  ec.txCfg.SignModeHandler().DefaultMode(),
			Signature: sigBytes,
		},
	}
	return signature
}

func (ec imuaClient) getSignedTxBytes(feederID uint64, baseBlock uint64, price fetchertypes.PriceInfo, nonce int32) (*oracletypes.MsgCreatePrice, []byte, error) {
	// build create-price message
	msg := oracletypes.NewMsgCreatePrice(
		sdk.AccAddress(ec.pubKey.Address()).String(),
		feederID,
		[]*oracletypes.PriceSource{
			{
				SourceID: Chainlink,
				Prices: []*oracletypes.PriceTimeDetID{
					{
						Price:     price.Price,
						Decimal:   price.Decimal,
						Timestamp: time.Now().UTC().Format(feedertypes.TimeLayout),
						DetID:     price.RoundID,
					},
				},
				Desc: "",
			},
		},
		baseBlock,
		nonce,
	)
	signedTxBytes, err := ec.getSignedTxBytesFromMsg(msg)
	return msg, signedTxBytes, err
}

func (ec imuaClient) getSignedTxBytes2Phases(feederID, baseBlock uint64, prices []*fetchertypes.PriceInfo, phase oracletypes.AggregationPhase, nonce int32) (*oracletypes.MsgCreatePrice, []byte, error) {
	var msg *oracletypes.MsgCreatePrice
	if phase == oracletypes.AggregationPhaseOne {
		if len(prices) != 1 {
			return nil, nil, errors.New("1st-phase message should include one and only one price info represents rootHash and leaf count")
		}
		price := prices[0]
		msg = oracletypes.NewMsgCreatePrice2Phase(
			sdk.AccAddress(ec.pubKey.Address()).String(),
			feederID,
			[]*oracletypes.PriceSource{
				{
					SourceID: Chainlink,
					Prices: []*oracletypes.PriceTimeDetID{
						{
							Price:     price.Price,
							Decimal:   price.Decimal,
							Timestamp: time.Now().UTC().Format(feedertypes.TimeLayout),
							DetID:     price.RoundID,
						},
					},
					Desc: "",
				},
			},
			baseBlock,
			nonce,
		)
		signedTxBytes, err := ec.getSignedTxBytesFromMsg(msg)
		return msg, signedTxBytes, err
	}
	if phase != oracletypes.AggregationPhaseTwo {
		return nil, nil, errors.New("invalid aggregation phase, only support 1st-phase and 2nd-phase")
	}
	if len(prices) < 1 || len(prices) > 2 {
		return nil, nil, errors.New("2nd-phase message should include one or two price infos")
	}
	pss := []*oracletypes.PriceSource{
		{
			SourceID: Chainlink,
			Prices:   []*oracletypes.PriceTimeDetID{},
			Desc:     "",
		},
	}
	for _, price := range prices {
		pss[0].Prices = append(pss[0].Prices, &oracletypes.PriceTimeDetID{
			Price:     price.Price,
			Timestamp: time.Now().UTC().Format(feedertypes.TimeLayout),
			DetID:     price.RoundID,
		})
	}
	msg = oracletypes.NewMsgCreatePrice2Phase2(
		sdk.AccAddress(ec.pubKey.Address()).String(),
		feederID,
		pss,
		baseBlock,
		nonce,
	)
	signedTxBytes, err := ec.getSignedTxBytesFromMsg(msg)
	return msg, signedTxBytes, err
}

func (ec imuaClient) getSignedTxBytesFromMsg(msg *oracletypes.MsgCreatePrice) ([]byte, error) {
	// sign the message with validator consensus-key configured
	signedTx, err := ec.signMsg(msg)
	if err != nil {
		return nil, fmt.Errorf("failed to sign message, msg:%v, valConsAddr:%s, error:%w", msg, sdk.ConsAddress(ec.pubKey.Address()), err)
	}

	// encode transaction to broadcast
	txBytes, err := ec.txCfg.TxEncoder()(signedTx)
	if err != nil {
		// this should not happen
		return nil, fmt.Errorf("failed to encode singedTx, txBytes:%b, msg:%v, valConsAddr:%s, error:%w", txBytes, msg, sdk.ConsAddress(ec.pubKey.Address()), err)
	}
	return txBytes, nil
}

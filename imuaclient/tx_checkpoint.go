package imuaclient

import (
	"context"
	"fmt"

	oracletypes "github.com/imua-xyz/imuachain/x/oracle/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdktx "github.com/cosmos/cosmos-sdk/types/tx"
)

// SendSignCheckpoint signs and broadcasts a MsgSignCheckpoint transaction.
func (ec imuaClient) SendSignCheckpoint(msg *oracletypes.MsgSignCheckpoint) (*sdktx.BroadcastTxResponse, error) {
	// Sign the message using the existing signMsg pattern (consensus key).
	signedTx, err := ec.signMsg(msg)
	if err != nil {
		return nil, fmt.Errorf("failed to sign MsgSignCheckpoint, valConsAddr:%s, error:%w", sdk.ConsAddress(ec.pubKey.Address()), err)
	}

	txBytes, err := ec.txCfg.TxEncoder()(signedTx)
	if err != nil {
		return nil, fmt.Errorf("failed to encode signed MsgSignCheckpoint: %w", err)
	}

	tc, err := ec.GetTxClient()
	if err != nil {
		return nil, fmt.Errorf("failed to get tx client: %w", err)
	}

	res, err := tc.BroadcastTx(
		context.Background(),
		&sdktx.BroadcastTxRequest{
			Mode:    sdktx.BroadcastMode_BROADCAST_MODE_SYNC,
			TxBytes: txBytes,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to broadcast MsgSignCheckpoint: %w", err)
	}
	return res, nil
}

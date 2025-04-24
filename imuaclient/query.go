package imuaclient

import (
	"context"
	"fmt"

	oracleTypes "github.com/imua-xyz/imuachain/x/oracle/types"
)

// GetParams queries oracle params
func (ec imuaClient) GetParams() (*oracleTypes.Params, error) {
	paramsRes, err := ec.oracleClient.Params(context.Background(), &oracleTypes.QueryParamsRequest{})
	if err != nil {
		return &oracleTypes.Params{}, fmt.Errorf("failed to query oracle params from oracleClient, error:%w", err)
	}
	return &paramsRes.Params, nil

}

// GetLatestPrice returns latest price of specific token
func (ec imuaClient) GetLatestPrice(tokenID uint64) (oracleTypes.PriceTimeRound, error) {
	priceRes, err := ec.oracleClient.LatestPrice(context.Background(), &oracleTypes.QueryGetLatestPriceRequest{TokenId: tokenID})
	if err != nil {
		return oracleTypes.PriceTimeRound{}, fmt.Errorf("failed to get latest price from oracleClient, error:%w", err)
	}
	return priceRes.Price, nil

}

// TODO: pagination
// GetStakerInfos get all stakerInfos for the assetID
func (ec imuaClient) GetStakerInfos(assetID string) ([]*oracleTypes.StakerInfo, uint64, error) {
	// TODO(leonz): pagination
	stakerInfoRes, err := ec.oracleClient.StakerInfos(context.Background(), &oracleTypes.QueryStakerInfosRequest{AssetId: assetID})
	if err != nil {
		return []*oracleTypes.StakerInfo{}, 0, fmt.Errorf("failed to get stakerInfos from oracleClient, error:%w", err)
	}
	return stakerInfoRes.StakerInfos, uint64(stakerInfoRes.Version), nil
}

// GetStakerInfos get the stakerInfos corresponding to stakerAddr for the assetID
func (ec imuaClient) GetStakerInfo(assetID, stakerAddr string) ([]*oracleTypes.StakerInfo, int64, error) {
	stakerInfoRes, err := ec.oracleClient.StakerInfos(context.Background(), &oracleTypes.QueryStakerInfosRequest{AssetId: assetID})
	if err != nil {
		return []*oracleTypes.StakerInfo{}, 0, fmt.Errorf("failed to get stakerInfo from oracleClient, error:%w", err)
	}
	return stakerInfoRes.StakerInfos, stakerInfoRes.Version, nil
}

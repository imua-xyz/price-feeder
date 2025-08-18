package imuaclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"cosmossdk.io/simapp/params"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/cosmos/cosmos-sdk/client"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/gorilla/websocket"
	oracletypes "github.com/imua-xyz/imuachain/x/oracle/types"
	"github.com/imua-xyz/price-feeder/internal/privval"
	feedertypes "github.com/imua-xyz/price-feeder/types"
)

var _ ImuaClientInf = &imuaClient{}

// imuaClient implements imuaClientInf interface to serve as a grpc client to interact with eoxored grpc service
type imuaClient struct {
	logger      feedertypes.LoggerInf
	grpcManager *connManager

	// params for sign/send transactions
	pv      privval.PrivValidator
	pubKey  cryptotypes.PubKey
	encCfg  params.EncodingConfig
	txCfg   client.TxConfig
	chainID string

	txClientDebug *rpchttp.HTTP

	// wsclient interact with imuad
	wsClient   *websocket.Conn
	wsEndpoint string
	wsDialer   *websocket.Dialer
	// wsStop channel used to signal ws close
	wsStop           chan struct{}
	wsLock           *sync.Mutex
	wsActiveRoutines *int
	wsActive         *bool
	wsEventsCh       chan EventInf
}

// NewImuaClient creates a imua-client used to do queries and send transactions to imuad
func NewImuaClient(logger feedertypes.LoggerInf, endpoint, wsEndpoint, endpointDebug string, pv privval.PrivValidator, encCfg params.EncodingConfig, chainID string, txOnly bool) (*imuaClient, error) {
	pubKey, err := pv.GetPubKey()
	if err != nil {
		return nil, errors.New("failed to get public key from privval")
	}
	ec := &imuaClient{
		chainID: chainID,
		logger:  logger,
		//		privKey:          privKey,
		pubKey:           pubKey,
		pv:               pv,
		encCfg:           encCfg,
		txCfg:            encCfg.TxConfig,
		wsEndpoint:       wsEndpoint,
		wsActiveRoutines: new(int),
		wsActive:         new(bool),
		wsLock:           new(sync.Mutex),
		wsStop:           make(chan struct{}),
		wsEventsCh:       make(chan EventInf),
	}

	if txOnly && len(endpointDebug) == 0 {
		return nil, errors.New("rpc endpoint is empty under debug mode")
	}
	if len(endpointDebug) > 0 {
		ec.txClientDebug, err = client.NewClientFromNode(endpointDebug)
		if err != nil {
			return nil, fmt.Errorf("failed to create new client for debug, endponit:%s, error:%v", endpointDebug, err)
		}
	}
	// grpc connection, websocket is not needed for txOnly mode when do debug
	if !txOnly {
		ec.logger.Info("establish grpc connection")
		ec.grpcManager, err = initGRPCManager(endpoint, encCfg, logger)
		if err != nil {
			return nil, feedertypes.ErrInitConnectionFail.Wrap(fmt.Sprintf("failed to init grpc conenction manager endpoint:%s, error:%v", endpoint, err))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err = ec.grpcManager.get(ctx)
		if err != nil {
			return nil, feedertypes.ErrInitConnectionFail.Wrap(fmt.Sprintf("failed to get grpc connection, endpoint:%s, error:%v", endpoint, err))
		}
		// setup wsClient
		u, err := url.Parse(wsEndpoint)
		if err != nil {
			return nil, fmt.Errorf("failed to parse wsEndpoint, wsEndpoint:%s, error:%w", wsEndpoint, err)
		}
		ec.wsDialer = &websocket.Dialer{
			NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				d := net.Dialer{
					Timeout:   5 * time.Second,
					KeepAlive: 30 * time.Second,
				}
				return d.DialContext(ctx, "tcp", u.Host)
			},
			Proxy: http.ProxyFromEnvironment,
		}
	}
	return ec, nil
}

func (ec *imuaClient) Close() {
	ec.CloseWs()
	if err := ec.CloseGRPC(); err != nil {
		ec.logger.Error("error when close GRPC connection", "error", err)
	}
}

// Close close grpc connection
func (ec *imuaClient) CloseGRPC() error {
	return ec.grpcManager.closeConn()
}

func (ec *imuaClient) CloseWs() {
	if ec.wsClient == nil {
		return
	}
	ec.StopWsRoutines()
	ec.wsClient.Close()
}

func (ec *imuaClient) GetOracleClient() (oracletypes.QueryClient, error) {
	grpcConn, err := ec.grpcManager.get(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get grpc connection: %w", err)
	}
	return oracletypes.NewQueryClient(grpcConn), nil
}

func (ec *imuaClient) GetTxClient() (tx.ServiceClient, error) {
	grpcConn, err := ec.grpcManager.get(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get grpc connection: %w", err)
	}
	return tx.NewServiceClient(grpcConn), nil
}

// GetClient returns defaultImuaClient and a bool value to tell if that defaultImuaClient has been initialized
func GetClient() (*imuaClient, bool) {
	if defaultImuaClient == nil {
		return nil, false
	}
	return defaultImuaClient, true
}

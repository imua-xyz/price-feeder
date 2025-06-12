package debugger

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	fetchertypes "github.com/imua-xyz/price-feeder/fetcher/types"
	"github.com/imua-xyz/price-feeder/imuaclient"
	"github.com/imua-xyz/price-feeder/internal/debugger/types"
	itypes "github.com/imua-xyz/price-feeder/internal/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type sendReq struct {
	// on which block committed send this transaction, required
	height int64

	feederID uint64
	price    *types.PriceMsg
	result   chan *types.SubmitPriceResponse
}
type pendingRequestManager map[int64][]*sendReq

func newPendingRequestManager() pendingRequestManager {
	return make(map[int64][]*sendReq)
}

func (p pendingRequestManager) add(height int64, req *sendReq) {
	if pendings, ok := p[height]; ok {
		p[height] = append(pendings, req)
	} else {
		p[height] = []*sendReq{req}
	}
}

func (p pendingRequestManager) process(height int64, handler func(*sendReq) error) {
	for h, pendings := range p {
		if h <= height {
			for _, req := range pendings {
				if err := handler(req); err != nil {
					req.result <- &types.SubmitPriceResponse{Err: err.Error()}
				}
			}
			delete(p, h)
		}
	}
}

type server struct {
	sendCh chan *sendReq
	types.UnimplementedPriceSubmitServiceServer
}

func (s *server) SubmitPrice(ctx context.Context, req *types.SubmitPriceRequest) (*types.SubmitPriceResponse, error) {
	result := make(chan *types.SubmitPriceResponse, 1)
	s.sendCh <- &sendReq{
		height:   req.Height,
		feederID: req.FeederId,
		price:    req.Price,
		result:   result,
	}
	r := <-result
	return r, nil
}

func newServer(sendCh chan *sendReq) *server {
	return &server{
		sendCh: sendCh,
	}
}

type PriceJSON struct {
	Price     string `json:"price"`
	DetID     string `json:"det_id"`
	Decimal   int32  `json:"decimal"`
	Timestamp string `json:"timestamp"`
	Nonce     int32  `json:"nonce"`
	BaseBlock uint64 `json:"base_block"`
}

func (p PriceJSON) Validate() error {
	if len(p.Price) == 0 {
		return errors.New("price is required")
	}
	if len(p.DetID) == 0 {
		return errors.New("det_id is required")
	}
	if p.Nonce == 0 {
		return errors.New("nonce should be greater than 0")
	}
	return nil
}

func (p PriceJSON) getPriceInfo() fetchertypes.PriceInfo {
	return fetchertypes.PriceInfo{
		Price:     p.Price,
		Decimal:   p.Decimal,
		RoundID:   p.DetID,
		Timestamp: p.Timestamp,
	}
}

var (
	DebugRetryConfig = itypes.RetryConfig{
		MaxAttempts: 43200,
		Interval:    2 * time.Second,
	}
)

func DebugPriceFeeder(conf *feedertypes.Config, logger feedertypes.LoggerInf, mnemonic, sourcesPath, privFile string) {
	// init logger
	if logger = feedertypes.SetLogger(logger); logger == nil {
		panic("logger is not initialized")
	}
	// init imuaclient
	// 1. subscribing for heights
	// 2. sending create-price tx
	count := 0
	for count < DebugRetryConfig.MaxAttempts {
		if err := imuaclient.Init(conf, mnemonic, privFile, false, true); err != nil {
			if errors.Is(err, feedertypes.ErrInitConnectionFail) {
				logger.Info("retry initComponents due to connectionfailed", "count", count, "maxRetry", DebugRetryConfig.MaxAttempts, "error", err)
				time.Sleep(DebugRetryConfig.Interval)
				continue
			}
			logger.Error("failed to init imuaclient", "error", err)
			return
		}
		break
	}
	ec, _ := imuaclient.GetClient()

	_, err := itypes.GetOracleParamsWithMaxRetry(DebugRetryConfig.MaxAttempts, ec, logger, DebugRetryConfig)
	if err != nil {
		logger.Error("failed to get oracle params", "error", err)
		return
	}
	logger.Info("subscribe block heights...")
	ecClient, _ := imuaclient.GetClient()
	defer ecClient.Close()
	ecClient.Subscribe()
	//	var pendingReq *sendReq
	//	pendingReqs := make(map[int64][]*sendReq)
	pendingReqs := newPendingRequestManager()
	sendCh := make(chan *sendReq, 10)
	go func() {
		for {
			select {
			case event := <-ecClient.EventsCh():
				if e, ok := event.(*imuaclient.EventNewBlock); ok {
					logger.Info("new block commited", "height", e.Height())

					pendingReqs.process(e.Height(), func(req *sendReq) error {
						res, err := ecClient.SendTxDebug(req.feederID, req.price.BaseBlock, req.price.GetPriceInfo(), req.price.Nonce)
						if err != nil {
							logger.Error("failed to send tx", "error", err)
							return err
						}
						req.result <- &types.SubmitPriceResponse{
							CheckTxSuccess:   res.CheckTx.Code == 0,
							CheckTxLog:       res.CheckTx.Log,
							DeliverTxSuccess: res.CheckTx.Code == 0 && res.DeliverTx.Code == 0,
							DeliverTxLog:     res.DeliverTx.Log,
							TxHash:           res.Hash.String(),
							Height:           res.Height,
						}
						return nil
					})
				}
			case req := <-sendCh:
				logger.Info("add a new send request", "height", req.height)
				pendingReqs.add(req.height, req)
			}
		}
	}()
	lis, err := net.Listen("tcp", conf.Debugger.Grpc)
	if err != nil {
		fmt.Printf("failed to listen: %v\r\n", err)
		return
	}
	grpcServer := grpc.NewServer()
	types.RegisterPriceSubmitServiceServer(grpcServer, newServer(sendCh))
	if err := grpcServer.Serve(lis); err != nil {
		fmt.Printf("failed to serve:%v\r\n", err)
		return
	}
}

func SendTx(feederID uint64, height int64, price *types.PriceMsg, port string) (*types.SubmitPriceResponse, error) {
	conn, err := grpc.Dial(
		fmt.Sprintf("localhost%s", port),
		//		"localhost:50051",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	c := types.NewPriceSubmitServiceClient(conn)
	return c.SubmitPrice(context.Background(), &types.SubmitPriceRequest{
		Height:   height,
		FeederId: feederID,
		Price:    price,
	})
}

func SendTxImmediately(feederID uint64, price *PriceJSON, mnemonic, privFile string, feederConfig *feedertypes.Config) (*types.SubmitPriceResponse, error) {
	if err := imuaclient.Init(feederConfig, mnemonic, privFile, true, true); err != nil {
		return nil, fmt.Errorf("failed to init imuaclient in txOnly mode for debug, error:%w", err)
	}
	ec, _ := imuaclient.GetClient()
	pInfo := price.getPriceInfo()

	res, err := ec.SendTxDebug(feederID, price.BaseBlock, pInfo, price.Nonce)
	if err != nil {
		return nil, err
	}
	protoRes := &types.SubmitPriceResponse{
		CheckTxSuccess:   res.CheckTx.Code == 0,
		CheckTxLog:       res.CheckTx.Log,
		DeliverTxSuccess: res.CheckTx.Code == 0 && res.DeliverTx.Code == 0,
		DeliverTxLog:     res.DeliverTx.Log,
		TxHash:           res.Hash.String(),
		Height:           res.Height,
	}
	return protoRes, nil
}

package privval

import (
	"fmt"
	"net"
	"sync"
	"time"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/imua-xyz/price-feeder/internal/privval/types"
	"github.com/imua-xyz/price-feeder/internal/utils"
)

var _ PrivValidator = (*PrivValidatorImplRemote)(nil)

type PrivValidatorImplRemote struct {
	requestID uint64
	// wrap net.Conn with loop to make sure we can read/write all messages, bytes level
	conn        net.Conn
	waiters     sync.Map
	sendReqCh   chan sendStreamObj
	wg          sync.WaitGroup
	lisAddr     string
	connTrigger chan struct{}
	// quit sension loop
	quitCh chan struct{}
	// quit write loop
	quitWCh chan struct{}
	// quit read loop
	quitRCh chan struct{}
}

func NewPrivValidatorImplRemote(lisAddr string) (*PrivValidatorImplRemote, error) {
	// try to listen on the address to make sure it's available
	lis, err := net.Listen("tcp", lisAddr)
	if err != nil {
		logger.Error().Err(err).Msgf("failed to listen on %s", lisAddr)
		return nil, err
	}
	lis.Close()
	return &PrivValidatorImplRemote{
		lisAddr:     lisAddr,
		sendReqCh:   make(chan sendStreamObj, 100),
		connTrigger: make(chan struct{}, 1),
		quitCh:      make(chan struct{}),
		quitRCh:     make(chan struct{}),
		quitWCh:     make(chan struct{}),
	}, nil
}

func (pv *PrivValidatorImplRemote) addWaiter(id uint64, w chan<- []byte) {
	if id > maxWaiters {
		pv.waiters.Delete(id - maxWaiters)
	}
	pv.waiters.Store(id, w)
}

// NextRequestID increments the request ID and returns the new value.
// this method is called synchronously in writeloop, so it is safe to use without additional locking.
func (pv *PrivValidatorImplRemote) nextRequestID() uint64 {
	pv.requestID++
	return pv.requestID
}

func (pv *PrivValidatorImplRemote) Init() {
	// sessionloop to handle the whole connection lifecycle including reconnects, read and write loops
	success := make(chan struct{})
	go pv.sessionloop(success)
	// we block until the first connection is established
	// dummy chan to trigger the first connection
	c := make(chan struct{})
	pv.reconnect(c)
	// TODO: this will wait forever, we should set a timeout in integration-mode
	<-success // wait for the first connection to be established
}

func (pv *PrivValidatorImplRemote) GetPubKey() cryptotypes.PubKey {
	return nil // we don't have a public key in this implementation, since it's not used in remote mode
}

func (pv *PrivValidatorImplRemote) SignRawDataSync(rawData []byte) ([]byte, error) {
	feedRequest := &types.OracleStreamMessage{
		Sum: &types.OracleStreamMessage_PriceFeedRequest{
			PriceFeedRequest: &types.PriceFeedRequest{
				RawData: rawData,
			},
		},
	}
	res := pv.sendMsgSync(feedRequest)
	if res.err != nil {
		return nil, res.err
	}
	timer := time.NewTimer(signatureTimeout)
	select {
	case <-timer.C:
	case r := <-res.result:
		return r, nil
	}
	return nil, fmt.Errorf("timeout waiting for signature response, request ID: %d", res.id)
}

func (pv *PrivValidatorImplRemote) sendMsgSync(msg *types.OracleStreamMessage) sendResp {
	resCh := make(chan sendResp, 1)
	timeout := time.NewTimer(signatureTimeout)
	select {
	case <-timeout.C:
	case pv.sendReqCh <- sendStreamObj{
		payload: msg,
		res:     resCh,
	}:
		return <-resCh
	}

	return sendResp{
		err: fmt.Errorf("timeout waiting for response, request ID: %d", msg.GetPriceFeedRequest().GetId()),
	}
}

func (pv *PrivValidatorImplRemote) readloop() {
	pv.wg.Add(1)
	defer pv.wg.Done()
	r := utils.NewV2DelimitedReader(pv.conn)
	active := true
	aliveTicker := time.NewTicker(3 * pingTimeout)
	for {
		select {
		case <-aliveTicker.C:
			if !active {
				// quite write loop, so we can reconnect
				pv.reconnect(pv.quitWCh)
				return
			}
			active = false
		default:
			var msg types.OracleStreamMessage
			readDeadline := time.Now().Add(defaultRWTimeout)
			pv.conn.SetReadDeadline(readDeadline)
			err := r.ReadMsg(&msg)
			if err != nil {
				logger.Error().Err(err).Msg("failed to read message")
				pv.reconnect(pv.quitWCh)
				return
			}
			active = true
			switch x := msg.Sum.(type) {
			case *types.OracleStreamMessage_PriceFeedResponse:
				res := x.PriceFeedResponse
				w, ok := pv.waiters.Load(res.RequestId)
				if !ok {
					logger.Error().Err(err).Msg(fmt.Sprintf("missing waiter for request ID:%d", res.RequestId))
					continue
				}
				waitReq, ok := w.(chan []byte)
				if !ok {
					logger.Error().Msg("invalid waiter type, expected chan []byte")
					pv.waiters.Delete(res.RequestId)
					continue
				}
				select {
				case waitReq <- res.Signature:
					pv.waiters.Delete(res.RequestId)
				default:
				}
			case *types.OracleStreamMessage_Ping:
				pv.pingpong(false) // send pong in response to ping
			default:
				// dont handle :case *types.OracleStreamMessage_Pong
				logger.Warn().Msgf("received unknown message type: %T", x)
			}
		}
	}
}

func (pv *PrivValidatorImplRemote) writeloop() {
	pv.wg.Add(1)
	defer pv.wg.Done()
	tic := time.NewTicker(10 * time.Second)
	w := utils.NewV2DelimitedWriter(pv.conn)
	for {
		var err error
		select {
		case <-tic.C:
			pv.conn.SetWriteDeadline(time.Now().Add(defaultRWTimeout))
			_, err = w.WriteMsg(&types.OracleStreamMessage{
				Sum: &types.OracleStreamMessage_Ping{
					Ping: &types.Ping{},
				},
			})
			if err != nil {
				logger.Error().Err(err).Msg("failed to write ping message")
			}
		case req := <-pv.sendReqCh:
			switch x := req.payload.Sum.(type) {
			case *types.OracleStreamMessage_PriceFeedRequest:
				id := pv.nextRequestID()
				x.PriceFeedRequest.Id = id
				result := make(chan []byte, 1)
				var resultWriteView chan<- []byte = result
				// we put the result channel into waiters map before we send the message to make sure the readloop will not miss/drop any expected response if any
				pv.addWaiter(id, resultWriteView)
				var n int
				n, err = w.WriteMsg(req.payload)
				if err != nil {
					logger.Error().Err(err).Msg("failed to write price-feed-sing-request message")
					pv.waiters.Delete(id)
					close(result)
					// TODO: reconnect ?
				} else {
					tic.Reset(pingTimeout) // reset ticker to avoid sending ping too often
				}
				select {
				case req.res <- sendResp{
					id:      id,
					written: n,
					result:  result,
					err:     err,
				}:
				// don't block, caller should make sure the response channel is ready(at least with buffer size of 1)
				default:
				}
			default:
				if _, err = w.WriteMsg(req.payload); err != nil {
					// TODO: reconnect ?
					logger.Error().Err(err).Msg("failed to write message")
				}
				// we don't return any response for other types of messages, currently it's only ping request
			}
		}
		if err != nil {
			pv.reconnect(pv.quitRCh)
			return
		}
	}
}

func (pv *PrivValidatorImplRemote) sessionloop(success chan struct{}) {
	lis, err := net.Listen("tcp", pv.lisAddr)
	if err != nil {
		time.Sleep(1 * time.Second) // wait a bit before retrying
		// try one more time, since we have do the tyring in the constructor
		lis, err = net.Listen("tcp", pv.lisAddr)
	}
	if err != nil {
		panic(fmt.Errorf("failed to listen on %s: %w", pv.lisAddr, err))
	}
	defer lis.Close()
	for {
		select {
		case <-pv.connTrigger:
			pv.wg.Wait() // wait for all goroutines to finish before accepting new connections
			conn, err := lis.Accept()
			if err == nil {
				pv.conn = conn
				pv.quitWCh = make(chan struct{})
				pv.quitRCh = make(chan struct{})
				logger.Info().Msg("start writing and reading loops")
				go pv.writeloop()
				go pv.readloop()
				logger.Info().Msgf("accepted new connection from %s", conn.RemoteAddr().String())
				select {
				case <-success:
				default:
					close(success)
				}
			} else {
				logger.Error().Err(err).Msg("failed to accept connection, waiting...")
			}
		case <-pv.quitCh:
			// we don't handle rwloop quit signal here to avoid conflict, they will quit when the connection is closed
			logger.Info().Msg("priv validator session loop quit signal received, closing connection")
		}
	}
}

func (pv *PrivValidatorImplRemote) pingpong(ping bool) {
	var payload *types.OracleStreamMessage
	if ping {
		payload = &types.OracleStreamMessage{
			Sum: &types.OracleStreamMessage_Ping{
				Ping: &types.Ping{},
			},
		}
	} else {
		payload = &types.OracleStreamMessage{
			Sum: &types.OracleStreamMessage_Pong{
				Pong: &types.Pong{},
			},
		}
	}
	select {
	case pv.sendReqCh <- sendStreamObj{
		payload: payload,
		// ignore res
	}:
	default:
	}
}

func (pv *PrivValidatorImplRemote) reconnect(quitCh chan struct{}) {
	close(quitCh)
	select {
	case pv.connTrigger <- struct{}{}:
	default:
	}
}

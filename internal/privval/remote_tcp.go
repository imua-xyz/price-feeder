package privval

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	ed25519 "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/imua-xyz/price-feeder/internal/privval/types"
	"github.com/imua-xyz/price-feeder/internal/utils"
	feedertypes "github.com/imua-xyz/price-feeder/types"
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
	l       feedertypes.LoggerInf
}

func NewPrivValidatorImplRemote(lisAddr string, l feedertypes.LoggerInf) (*PrivValidatorImplRemote, error) {
	// try to listen on the address to make sure it's available
	lis, err := net.Listen("tcp", lisAddr)
	if err != nil {
		l.Error("failed to listen on port, retrying...", "lisAddr", lisAddr)
		return nil, err
	}
	lis.Close()
	return &PrivValidatorImplRemote{
		lisAddr: lisAddr,
		// the sendReqCh has a buffer size of 100 to avoid blocking the write loop
		// when a connection is lost or slow the caller might timeout but the requests are still waiting in the channel,
		// when a new connection is established, the write loop will process them even no caller will consume the result because the caller might have timed out
		sendReqCh:   make(chan sendStreamObj, 100),
		connTrigger: make(chan struct{}, 1),
		quitCh:      make(chan struct{}),
		quitRCh:     make(chan struct{}),
		quitWCh:     make(chan struct{}),
		l:           l,
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

func (pv *PrivValidatorImplRemote) DecreaseRequestID() {
	if pv.requestID > 0 {
		pv.requestID--
	}
}

func (pv *PrivValidatorImplRemote) Init() {
	// sessionloop to handle the whole connection lifecycle including reconnects, read and write loops
	success := make(chan struct{})
	go pv.sessionloop(success)
	// we block until the first connection is established
	// dummy chan to trigger the first connection
	c := make(chan struct{})
	pv.reconnect(c)
	<-success // wait for the first connection to be established
}

func (pv *PrivValidatorImplRemote) GetPubKey() (cryptotypes.PubKey, error) {
	getPubKeyRequest := &types.OracleStreamMessage{
		Sum: &types.OracleStreamMessage_GetPubKeyRequest{
			GetPubKeyRequest: &types.GetPubKeyRequest{},
		},
	}
	res := pv.sendMsgSync(getPubKeyRequest)
	if res.err != nil {
		return nil, res.err
	}
	timer := time.NewTimer(requestTimeout)
	select {
	case <-timer.C:
	case pkBytes := <-res.result:
		if len(pkBytes) != ed25519.PubKeySize {
			return nil, fmt.Errorf("invalid ed25519 pubkey length: %d", len(pkBytes))
		}
		pk := &ed25519.PubKey{
			Key: make([]byte, ed25519.PubKeySize),
		}
		copy(pk.Key[:], pkBytes)
		return pk, nil
	}
	return nil, errors.New("timeout waiting for pubkey response")
}

func (pv *PrivValidatorImplRemote) SignRawDataSync(rawData []byte) ([]byte, error) {
	feedRequest := &types.OracleStreamMessage{
		Sum: &types.OracleStreamMessage_SignPriceFeedRequest{
			SignPriceFeedRequest: &types.SignPriceFeedRequest{
				RawData: rawData,
			},
		},
	}
	res := pv.sendMsgSync(feedRequest)
	if res.err != nil {
		return nil, res.err
	}
	timer := time.NewTimer(requestTimeout)
	select {
	case <-timer.C:
	case r := <-res.result:
		return r, nil
	}
	return nil, fmt.Errorf("timeout waiting for signature response, request ID: %d", res.id)
}

func (pv *PrivValidatorImplRemote) sendMsgSync(msg *types.OracleStreamMessage) sendResp {
	resCh := make(chan sendResp, 1)
	timeout := time.NewTimer(requestTimeout)
	select {
	case <-timeout.C:
	case pv.sendReqCh <- sendStreamObj{
		payload: msg,
		res:     resCh,
	}:
		select {
		// we don't wait forever in cases like connection lost, so we use a timeout
		case <-timeout.C:
		case r := <-resCh:
			return r
		}
	}

	return sendResp{
		err: fmt.Errorf("timeout waiting for response, request ID: %d", msg.GetSignPriceFeedRequest().GetId()),
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
		case <-pv.quitRCh:
			return
		case <-aliveTicker.C:
			if !active {
				// quite write loop, so we can reconnect
				pv.reconnect(pv.quitWCh)
				return
			}
			active = false
		default:
			var msg types.OracleStreamMessage
			err := r.ReadMsgWithTimeout(&msg, defaultRWTimeout)
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
					pv.l.Error("connection closed by remote peer, quit and try reconnect", "err", err)
					pv.reconnect(pv.quitWCh)
					return
				}
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					continue
				}
				pv.l.Error("failed to read message", "err", err)
				continue
			}
			active = true
			switch x := msg.Sum.(type) {
			case *types.OracleStreamMessage_SignPriceFeedResponse, *types.OracleStreamMessage_GetPubKeyResponse:
				// we have checked the types x before, so we can skip the error check
				reqID, resBytes, _ := msg.GetBytesResponse()
				w, ok := pv.waiters.Load(reqID)
				if !ok {
					pv.l.Error("missing waiter for request ID", "reqID", reqID)
					continue
				}
				waitReq, ok := w.(chan<- []byte)
				if !ok {
					pv.l.Error("invalid waiter type, expected chan []byte")
					pv.waiters.Delete(reqID)
					continue
				}
				select {
				case waitReq <- resBytes:
					pv.waiters.Delete(reqID)
				default:
				}
			case *types.OracleStreamMessage_Ping:
				pv.pingpong(false) // send pong in response to ping
			case *types.OracleStreamMessage_Pong:
			// dont't handle pong message here, it's just a keep-alive message
			default:
				pv.l.Error("received unknown message type", "type", x)
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
		case <-pv.quitWCh:
			return // quit write loop when quitWCh is closed
		case <-tic.C:
			_, err = w.WriteMsgWithTimeout(&types.OracleStreamMessage{
				Sum: &types.OracleStreamMessage_Ping{
					Ping: &types.Ping{},
				},
			}, defaultRWTimeout)
			if err != nil {
				pv.l.Error("failed to write ping message", "err", err)
			}
		case req := <-pv.sendReqCh:
			switch req.payload.Sum.(type) {
			case *types.OracleStreamMessage_SignPriceFeedRequest, *types.OracleStreamMessage_GetPubKeyRequest:
				id := pv.nextRequestID()
				req.payload.SetID(id)
				result := make(chan []byte, 1)
				var resultWriteView chan<- []byte = result
				// we put the result channel into waiters map before we send the message to make sure the readloop will not miss/drop any expected response if any
				pv.addWaiter(id, resultWriteView)
				var n int
				fmt.Println("writing price-feed-sign-request message with ID:", id)
				n, err = w.WriteMsgWithTimeout(req.payload, defaultRWTimeout)
				if err != nil {
					pv.l.Error("failed to write price-feed-sign-request message", "err", err)
					pv.waiters.Delete(id)
					close(result)
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
				if _, err = w.WriteMsgWithTimeout(req.payload, defaultRWTimeout); err != nil {
					pv.l.Error("failed to write message", "err", err)
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
				pv.l.Info("start writing and reading loops")
				go pv.writeloop()
				go pv.readloop()
				pv.l.Info("accepted new connection from", "addr", conn.RemoteAddr().String())
				select {
				case <-success:
				default:
					close(success)
				}
			} else {
				pv.l.Error("failed to accept connection, waiting...", "err", err)
			}
		case <-pv.quitCh:
			// we don't handle rwloop quit signal here to avoid conflict, they will quit when the connection is closed
			pv.l.Info("priv validator session loop quit signal received, closing connection")
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

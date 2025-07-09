package imuaclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

type subEvent string
type eventQuery string

const (
	subStr                       = `{"jsonrpc":"2.0","method":"subscribe","id":0,"params":{"query":"%s"}}`
	reconnectInterval            = 3
	maxRetry                     = 600
	updated                      = "true"
	eNewBlock         eventQuery = "tm.event='NewBlock'"
	eTxUpdatePrice    eventQuery = "tm.event='Tx' AND create_price.price_update='true'"
	eTxNSTBalance     eventQuery = "tm.event='Tx' AND create_price.nst_balance_update='true'"
	eTxNSTPiece       eventQuery = "tm.event='Tx' AND create_price.nst_piece_update='true'"
)

var (
	subNewBlock      subEvent = subEvent(fmt.Sprintf(subStr, eNewBlock))
	subTxUpdatePrice subEvent = subEvent(fmt.Sprintf(subStr, eTxUpdatePrice))
	subTxNSTBalance  subEvent = subEvent(fmt.Sprintf(subStr, eTxNSTBalance))
	subTxNSTPiece    subEvent = subEvent(fmt.Sprintf(subStr, eTxNSTPiece))

	events = map[subEvent]bool{
		subNewBlock:      true,
		subTxUpdatePrice: true,
		subTxNSTBalance:  true,
		subTxNSTPiece:    true,
	}
)

func (ec *imuaClient) Subscribe() {
	// set up a background routine to listen to 'stop' signal and restart all tasks
	// we expect this rountine as a forever-running process unless failed more than maxretry times
	// or failed to confirm all routines closed after timeout when reciving stop signal
	go func() {
		ec.logger.Info("start subscriber job with all tasks")
		ec.startTasks()
		defer ec.wsClient.Close()
		for {
			<-ec.wsStop
			ec.logger.Info("ws connection closed, mark connection as inactive and waiting for all ws routines to complete stopping")
			// mark ws connection as inactive to prevent further ws routine starting
			ec.markWsInactive()
			timeout := time.NewTicker(60 * time.Second)
		loop:
			for {
				select {
				case <-timeout.C:
					// this should not happen
					panic("failed to complete closing all ws routines, timeout")
				default:
					if ec.isZeroWsRoutines() {
						logger.Info("all running ws routnines stopped")
						break loop
					}
					time.Sleep(1 * time.Second)
				}
			}

			ec.startTasks()
		}
	}()
}

func (ec imuaClient) EventsCh() chan EventInf {
	return ec.wsEventsCh
}

// startTasks establishes the ws connection and
// 1. routine: send ping message
// 2. subscribe to events
// 3. routine: read events from ws connection
func (ec *imuaClient) startTasks() {
	// ws connection stopped, reset subscriber
	ec.logger.Info("establish ws connection")
	if err := ec.connectWs(maxRetry); err != nil {
		// continue
		panic(fmt.Sprintf("failed to create ws connection after maxRetry:%d, error:%s", maxRetry, err.Error()))
	}
	ec.markWsActive()
	ec.logger.Info("subscribe to ws publish", "events", events)
	if err := ec.sendAllSubscribeMsgs(maxRetry); err != nil {
		panic(fmt.Sprintf("failed to send subscribe messages after maxRetry:%d, error:%s", maxRetry, err.Error()))
	}

	// start routine to send ping messages
	ec.logger.Info("start ws ping routine")
	ec.startPingRoutine()

	// start routine to read events response
	ec.logger.Info("start ws read routine")
	ec.startReadRoutine()
	ec.logger.Info("setuped all subscriber tasks successfully")
}

func (ec *imuaClient) connectWs(maxRetry int) error {
	if ec.wsDialer == nil {
		return errors.New("wsDialer not set in imuaClient")
	}
	var err error
	count := 0
	for count < maxRetry {
		if ec.wsClient, _, err = ec.wsDialer.Dial(ec.wsEndpoint, http.Header{}); err == nil {
			ec.wsClient.SetPongHandler(func(string) error {
				return nil
			})
			ec.wsStop = make(chan struct{})
			ec.markWsActive()
			return nil
		}
		count++
		ec.logger.Info("connecting to ws endpoint", "endpoint", ec.wsEndpoint, "attempt_count", count, "error", err)
		time.Sleep(reconnectInterval * time.Second)
	}
	return fmt.Errorf("failed to dial ws endpoint, endpoint:%s, error:%w", ec.wsEndpoint, err)
}

func (ec *imuaClient) StopWsRoutines() {
	ec.wsLock.Lock()
	select {
	case _, ok := <-ec.wsStop:
		if ok {
			close(ec.wsStop)
		}
	default:
		close(ec.wsStop)
	}

	ec.wsLock.Unlock()
}

func (ec *imuaClient) increaseWsRoutines() (int, bool) {
	// only increase active rountine count when the wsConnection is active
	ec.wsLock.Lock()
	defer ec.wsLock.Unlock()
	if *ec.wsActive {
		(*ec.wsActiveRoutines)++
		return *ec.wsActiveRoutines, true
	}
	return *ec.wsActiveRoutines, false
}

func (ec *imuaClient) decreaseWsRountines() (int, bool) {
	ec.wsLock.Lock()
	defer ec.wsLock.Unlock()
	if ec.wsActiveRoutines != nil {
		if (*ec.wsActiveRoutines)--; *ec.wsActiveRoutines < 0 {
			*ec.wsActiveRoutines = 0
		}
		return *ec.wsActiveRoutines, true
	}

	return *ec.wsActiveRoutines, false
}

func (ec *imuaClient) isZeroWsRoutines() bool {
	ec.wsLock.Lock()
	isZero := *ec.wsActiveRoutines == 0
	ec.wsLock.Unlock()
	return isZero
}

func (ec *imuaClient) markWsActive() {
	ec.wsLock.Lock()
	*ec.wsActive = true
	ec.wsLock.Unlock()

}

func (ec *imuaClient) markWsInactive() {
	ec.wsLock.Lock()
	*ec.wsActive = false
	ec.wsLock.Unlock()
}

func (ec imuaClient) sendAllSubscribeMsgs(maxRetry int) error {
	// at least try for one time
	if maxRetry < 1 {
		maxRetry = 1
	}
	// reset events for re-subscribing
	resetEvents()
	allSet := false
	for maxRetry > 0 && !allSet {
		maxRetry--
		allSet = true
		for event, ok := range events {
			if ok {
				allSet = false
			}
			if err := ec.wsClient.WriteMessage(websocket.TextMessage, []byte(event)); err == nil {
				events[event] = false
				allSet = true
			} else {
				ec.logger.Error("failed to send subscribe", "message", event, "error", err)
			}
		}
		time.Sleep(2 * time.Second)
	}
	if !allSet {
		return fmt.Errorf("failed to send all subscribe messages, events:%v", events)
	}
	return nil
}

func (ec imuaClient) startPingRoutine() bool {
	if _, ok := ec.increaseWsRoutines(); !ok {
		ec.logger.Error("failed to start ping routine")
		// ws connection is not active
		return ok
	}
	go func() {
		defer func() {
			ec.decreaseWsRountines()
		}()
		ticker := time.NewTicker(1 * time.Minute)
		defer func() {
			ticker.Stop()
		}()
		for {
			select {
			case <-ticker.C:
				if err := ec.wsClient.WriteMessage(websocket.PingMessage, []byte{}); err != nil {
					logger.Error("failed to write ping message to ws connection, close ws connection, ", "error", err)
					logger.Info("send signal to stop all running ws routines")
					ec.StopWsRoutines()
					return
				}
				ec.logger.Debug("ping ws connection", "endpoint", ec.wsEndpoint)
			case <-ec.wsStop:
				logger.Info("close ws ping routine due to receiving close signal")
				if err := ec.wsClient.WriteMessage(
					websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
				); err != nil {
					logger.Error("failed to write close message to publisher", "error", err)
				}
				return
			}
		}
	}()
	return true
}

func (ec imuaClient) startReadRoutine() bool {
	if _, ok := ec.increaseWsRoutines(); !ok {
		// ws connection is not active
		return ok
	}
	go func() {
		defer func() {
			ec.decreaseWsRountines()
		}()
		for {
			select {
			case <-ec.wsStop:
				ec.logger.Info("close ws read routine due to receive close signal")
				return
			default:
				_, data, err := ec.wsClient.ReadMessage()
				if err != nil {
					ec.logger.Error("failed to read from ws publisher, send signal to stop all running ws routines", "error", err)
					// send signal to stop all running ws routines
					ec.StopWsRoutines()
					return
				}
				var response SubscribeResult
				err = json.Unmarshal(data, &response)
				if err != nil {
					ec.logger.Error("failed to parse response from publisher, skip", "error", err)
					continue
				}
				switch eventQuery(response.Result.Query) {
				case eNewBlock:
					event, err := response.GetEventNewBlock()
					if err != nil {
						ec.logger.Error("failed to get newBlock event from event-response", "response", response, "error", err)
					}
					ec.wsEventsCh <- event
				case eTxUpdatePrice:
					event, err := response.GetEventUpdatePrice()
					if err != nil {
						ec.logger.Error("failed to get updatePrice event from event-response", "response", response, "error", err)
						break
					}
					ec.wsEventsCh <- event
				case eTxNSTPiece:
					event, err := response.GetEventNSTPiece()
					if err != nil {
						ec.logger.Error("failed to get rawdataPiece event from event-response", "response", response, "error", err)
						break
					}
					ec.wsEventsCh <- event
				default:
					ec.logger.Error("failed to parse unknown event type", "response-data", string(data))
				}
			}
		}
	}()
	return true
}

func resetEvents() {
	for event := range events {
		events[event] = true
	}
}

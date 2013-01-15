package main

import (
	"ancientsolutions.com/urlconnection"
	"errors"
	"expvar"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"sync"
	"time"
)

var ReconnectsPerBackend *expvar.Map =
	expvar.NewMap("num-reconnects-per-backend")
var ReconnectFailuresByReason *expvar.Map =
	expvar.NewMap("num-reconnect-failures-by-reason")
var RequestTimeSumPerBackend *expvar.Map =
	expvar.NewMap("request-time-total-per-backend")

func AccessLogRequest(accessLog *log.Logger, req *http.Request,
	statusCode int, contentLength int64, begin time.Time) {
	accessLog.Print(req.Host, " ", req.RemoteAddr, " - - [",
		begin.UTC().Format("02/01/2006:15:04:05 -0700"), "] ",
		strconv.Quote(req.Method+" "+req.RequestURI+" "+req.Proto),
		" ", statusCode, contentLength, " ",
		strconv.Quote(req.Referer()), " ", strconv.Quote(req.UserAgent()))
}

type BackendConnection struct {
	dest                 string
	log                  *log.Logger
	clientConn           *httputil.ClientConn
	clientConnMtx        *sync.RWMutex
	tcpConn              net.Conn
	weightedResponseTime time.Duration
	connectionAttempt    uint
	ready                bool
}

func NewBackendConnection(dest string,
	logDest *log.Logger) *BackendConnection {
	return NewBackendFromURL("tcp://" + dest, logDest)
}

func NewBackendFromURL(url string,
	logDest *log.Logger) *BackendConnection {
	var be = &BackendConnection{
		dest: url,
		log: logDest,
		clientConnMtx: new(sync.RWMutex),
	}
	go be.CheckAndReconnect(nil)
	return be
}

func (be *BackendConnection) CheckAndReconnect(e error) {
	var err error
	var e2 net.Error
	var ok bool

	if be.tcpConn != nil && !be.ready {
		// Reconnection is already in progress.
		return
	}

	e2, ok = e.(net.Error)
	if ok && e2 != nil && e2.Temporary() {
		// The error is merely temporary, no need to kill our connection.
		return
	}
	be.ready = false
	if be.tcpConn != nil {
		be.tcpConn.Close()
		be.tcpConn = nil
	}
	ReconnectsPerBackend.Add(be.dest, 1)
	be.tcpConn, err = urlconnection.ConnectTimeout(be.dest,
		(2<<be.connectionAttempt)*time.Second)
	if err != nil {
		log.Print("Failed to connect to ", be.dest, ": ", err)
		ReconnectFailuresByReason.Add(err.Error(), 1)
		be.connectionAttempt = be.connectionAttempt + 1
		time.Sleep((2 << be.connectionAttempt) * 100 * time.Millisecond)
		go be.CheckAndReconnect(nil)
		return
	}
	be.clientConnMtx.Lock()
	be.clientConn = httputil.NewClientConn(be.tcpConn, nil)
	be.clientConnMtx.Unlock()
	be.connectionAttempt = 0
	be.ready = true
	log.Print("Successfully connected to " + be.dest)
	return
}

func (be *BackendConnection) Ready() bool {
	return be.ready
}

func (be *BackendConnection) String() string {
	return be.dest
}

func (be *BackendConnection) Do(req *http.Request, w http.ResponseWriter,
	closeConnection bool) error {
	var err error
	var begin time.Time
	var passed time.Duration

	be.clientConnMtx.RLock()
	if be.clientConn == nil {
		be.clientConnMtx.RUnlock()
		return errors.New("Transport endpoint not connected")
	}
	begin = time.Now()
	res, err := be.clientConn.Do(req)
	be.clientConnMtx.RUnlock()
	if err != nil {
		return err
	}
	passed = time.Since(begin)

	// Backfill URL field
	if len(req.URL.Host) == 0 {
		req.URL.Host = req.Host
	}
	if len(req.URL.Scheme) == 0 {
		if req.TLS == nil {
			req.URL.Scheme = "http"
		} else {
			req.URL.Scheme = "https"
		}
	}

	AccessLogRequest(be.log, req, res.StatusCode, res.ContentLength, begin)

	for key, values := range res.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	if closeConnection {
		w.Header().Set("Connection", "close")
	}

	w.WriteHeader(res.StatusCode)
	for {
		var data []byte = make([]byte, 65536)
		var length int
		var errb error

		length, err = res.Body.Read(data)
		if length > 0 {
			length, errb = w.Write(data[:length])
			if errb != nil {
				res.Body.Close()
				return err
			}
		}
		if err == io.EOF {
			// Reached end of input.
			break
		}
		if err != nil {
			res.Body.Close()
			return err
		}
	}
	RequestTimeSumPerBackend.AddFloat(be.dest, passed.Seconds())

	res.Body.Close()
	return nil
}

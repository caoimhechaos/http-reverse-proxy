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
	clientConnMtx        *Mutex
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
		clientConnMtx: NewMutex(),
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
	if be.clientConn != nil {
		be.clientConn.Close()
	}
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
	return
}

func (be *BackendConnection) Ready() bool {
	return be.ready
}

func (be *BackendConnection) String() string {
	return be.dest
}

func (be *BackendConnection) Do(req *http.Request, w http.ResponseWriter,
	closeConnection bool) (bool, error) {
	var err error
	var begin time.Time
	var passed time.Duration

	// Check if the connection is busy.
	if !be.clientConnMtx.TryLock() {
		return true, nil
	}
	defer be.clientConnMtx.Unlock()
	if be.clientConn == nil {
		return false, errors.New("Transport endpoint not connected")
	}
	begin = time.Now()
	res, err := be.clientConn.Do(req)
	if err == httputil.ErrPersistEOF {
		defer be.CheckAndReconnect(nil)
		err = nil
	}
	if err != nil {
		return false, err
	}
	passed = time.Since(begin)
	defer res.Body.Close()

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
	} else {
		w.Header().Del("Connection")
	}

	w.WriteHeader(res.StatusCode)
	_, err := io.Copy(w, res.Body)
	if err != nil && err != io.EOF {
		log.Print("Error copying bytes: ", err.Error(),
			" at ", req.URL.String())

		// It is too late to return an error now (we already
		// started sending the response), so we don't return
		// an error here (it would cause another attempt to
		// fetch the data).
		return false, err
	}
	RequestTimeSumPerBackend.AddFloat(be.dest, passed.Seconds())

	return false, nil
}

package main

import (
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
	log					 *log.Logger
	clientConn           *httputil.ClientConn
	tcpConn              net.Conn
	weightedResponseTime time.Duration
	connectionAttempt    uint
	ready                bool
}

func NewBackendConnection(dest string,
	logDest *log.Logger) *BackendConnection {
	var be = &BackendConnection{
		dest: dest,
		log: logDest,
	}
	go be.CheckAndReconnect(nil)
	return be
}

func (be *BackendConnection) CheckAndReconnect(e error) {
	var err error
	var e2 net.Error
	var ok bool

	e2, ok = e.(net.Error)
	if ok && e2 != nil && e2.Temporary() {
		// The error is merely temporary, no need to kill our connection.
		return
	}
	be.ready = false
	ReconnectsPerBackend.Add(be.dest, 1)
	be.tcpConn, err = net.DialTimeout("tcp", be.dest,
		(2<<be.connectionAttempt)*time.Second)
	if err != nil {
		log.Print("Failed to connect to ", be.dest, ": ", err)
		ReconnectFailuresByReason.Add(err.Error(), 1)
		be.connectionAttempt = be.connectionAttempt + 1
		time.Sleep((2 << be.connectionAttempt) * 100 * time.Millisecond)
		go be.CheckAndReconnect(nil)
		return
	}
	be.clientConn = httputil.NewClientConn(be.tcpConn, nil)
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

func (be *BackendConnection) Do(req *http.Request, w http.ResponseWriter) error {
	var err error
	var begin time.Time = time.Now()
	var passed time.Duration
	res, err := be.clientConn.Do(req)
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

	w.WriteHeader(res.StatusCode)
	_, err = io.Copy(w, res.Body)
	if err != nil {
		return err
	}
	RequestTimeSumPerBackend.AddFloat(be.dest, passed.Seconds())

	res.Body.Close()
	return nil
}

package main

import (
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"time"
)

type BackendConnection struct {
	dest                 string
	clientConn           *httputil.ClientConn
	tcpConn              net.Conn
	weightedResponseTime time.Duration
	connectionAttempt    uint
	ready                bool
}

func NewBackendConnection(dest string) *BackendConnection {
	var be = &BackendConnection{
		dest: dest,
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
	be.tcpConn, err = net.DialTimeout("tcp", be.dest,
		(2<<be.connectionAttempt)*time.Second)
	if err != nil {
		log.Print("Failed to connect to ", be.dest, ": ", err)
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

	log.Print("Request to ", req.URL.String(), " took ", passed)
	log.Print(req.Host, " ", req.RemoteAddr, " - - [",
		begin.UTC().Format("02/01/2006:15:04:05 -0700"), "] ",
		strconv.Quote(req.Method+" "+req.RequestURI+" "+req.Proto),
		" ", res.StatusCode, res.ContentLength, " ",
		strconv.Quote(req.Referer()), " ", strconv.Quote(req.UserAgent()))

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

	res.Body.Close()
	return nil
}

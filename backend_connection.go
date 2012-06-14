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
	weightedResponseTime time.Duration
}

func NewBackendConnection(dest string) (be *BackendConnection, err error) {
	conn, err := net.Dial("tcp", dest)
	if err != nil {
		return
	}
	be = &BackendConnection{
		dest:       dest,
		clientConn: httputil.NewClientConn(conn, nil),
	}
	return
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
	log.Print("Request took ", passed)
	log.Print(req.Host, " ", req.RemoteAddr, " - - [",
		begin.UTC().Format("02/01/2006:15:04:05"), "] ",
		strconv.Quote(req.Method + " " + req.RequestURI + " " + req.Proto),
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

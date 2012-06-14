package main

import (
	"code.google.com/p/goprotobuf/proto"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
)

type ReqHandler struct {
	PortConfig *PortConfig
	VHostConfig map[string]*TargetConfig
}

// Default Request Handler
func (this *ReqHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var target *TargetConfig = this.VHostConfig[r.Host]
	
	if target == nil {
		host, _, err := net.SplitHostPort(r.Host)
		if err == nil {
			target = this.VHostConfig[host]
		}
	}

	if target == nil {
		http.Error(w, "Host not configured", http.StatusServiceUnavailable)
		log.Print("Received request for unknown host " + r.Host)
		return
	}

	for _, be := range(target.Be) {
		var dest string = net.JoinHostPort(*be.Host,
			fmt.Sprintf("%d", *be.Port))
		conn, err := NewBackendConnection(dest)

		if err == nil {
			err := conn.Do(r, w)

			if err != nil {
				log.Print("Error sending request to " + dest + ": " +
					err.Error())
			}
		} else {
			log.Print("Error connecting to " + dest + ": " +
				err.Error())
		}
	}
}

func main() {
	var filename = flag.String("config", "", "Path to configuration file")
	var config = new(ReverseProxyConfig)
	var vhostconfig = make(map[string]*TargetConfig)
	var conffile io.Reader
	var wg sync.WaitGroup
	var data []byte
	var err error
	
	flag.Parse()

	if len(*filename) == 0 {
		log.Fatal("No configuration file given")
	}

	if conffile, err = os.Open(*filename); err != nil {
		log.Fatal(err)
	}
	if data, err = ioutil.ReadAll(conffile); err != nil {
		log.Fatal(err)
	}

	if err = proto.UnmarshalText(string(data), config); err != nil {
		log.Fatal(err)
	}
	
	for _, target := range(config.TargetConfig) {
		for _, host := range(target.HttpHost) {
			vhostconfig[host] = target
		}
	}

	wg.Add(len(config.PortConfig))

	for _, port := range(config.PortConfig) {
		go func(p *PortConfig) {
			srv := new(http.Server)
			handler := new(ReqHandler)
			handler.PortConfig = p
			handler.VHostConfig = vhostconfig

			srv.Addr = ":" + fmt.Sprint(*p.Port)
			srv.Handler = handler

			if p.SslCertPath != nil && p.SslKeyPath != nil {
				err = srv.ListenAndServeTLS(*p.SslCertPath, *p.SslKeyPath)
			} else {
				err = srv.ListenAndServe()
			}

			if err != nil {
				log.Print(err)
			}
			
			wg.Done()
		}(port)
	}

	wg.Wait()
}

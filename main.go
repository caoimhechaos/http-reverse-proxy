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
	"net/http/httputil"
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
	var client *httputil.ClientConn
	
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
		conn, err := net.Dial("tcp", dest)

		if err == nil {
			client = httputil.NewClientConn(conn, nil)
			res, err := client.Do(r)
			
			if err == nil {
				for key, values := range(res.Header) {
					for _, value := range(values) {
						w.Header().Add(key, value)
					}
				}
				w.WriteHeader(res.StatusCode)
				_, err = io.Copy(w, res.Body)
				if err != nil {
					log.Print("Unable to send response to client: " +
						err.Error())
				}
				res.Body.Close()
				return
			} else {
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

			if err = srv.ListenAndServe(); err != nil {
				log.Print(err)
			}
			
			wg.Done()
		}(port)
	}

	wg.Wait()
}

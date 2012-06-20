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
	BackendMap map[string]*TargetsSpec
}

type TargetsSpec struct {
	Backends []*BackendConnection
	lru      int
}

func (this *TargetsSpec) GetNextConnection() *BackendConnection {
	if this.lru >= len(this.Backends)-1 {
		this.lru = 0
		return this.Backends[this.lru]
	}
	this.lru = this.lru + 1
	return this.Backends[this.lru]
}

// Default Request Handler
func (this *ReqHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var targets *TargetsSpec = this.BackendMap[r.Host]
	var be, initbe *BackendConnection

	if targets == nil {
		host, _, err := net.SplitHostPort(r.Host)
		if err == nil {
			targets = this.BackendMap[host]
		}
	}

	if targets == nil {
		http.Error(w, "Host not configured", http.StatusServiceUnavailable)
		log.Print("Received request for unknown host " + r.Host)
		return
	}

	// We don't want to terminate the connection to our backend, so let's
	// leave it up to our HTTP client.
	r.Header.Del("Connection")

	// We need to add an XFF header however.
	r.Header.Add('X-Forwarded-For', r.RemoteAddr)

	initbe = targets.GetNextConnection()
	be = initbe
	for {
		err := be.Do(r, w)

		if err == nil {
			return
		} else {
			log.Print("Error sending request to backend",
				be.String(), ": ", err.Error())
			go be.CheckAndReconnect(err)
		}

		for {
			be = targets.GetNextConnection()
			if be == initbe {
				break
			}
			if be.Ready() {
				break
			}
		}
		if be == initbe {
			break
		}
	}
}

func main() {
	var filename = flag.String("config", "", "Path to configuration file")
	var config = new(ReverseProxyConfig)
	var backendmap = make(map[string]*TargetsSpec)
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

	for _, target := range config.TargetConfig {
		var spec *TargetsSpec = new(TargetsSpec)
		var be_list []*BackendConnection

		for _, backend := range target.Be {
			var dest string = net.JoinHostPort(*backend.Host,
				fmt.Sprintf("%d", *backend.Port))
			var conn *BackendConnection = NewBackendConnection(dest)

			if err != nil {
				log.Printf("Unable to connect to %s: %s",
					dest, err)
			} else {
				log.Print("Established backend connection to ",
					dest)
				be_list = append(be_list, conn)
			}
		}

		for _, host := range target.HttpHost {
			spec.Backends = be_list
			backendmap[host] = spec
		}
	}

	wg.Add(len(config.PortConfig))

	for _, port := range config.PortConfig {
		go func(p *PortConfig) {
			srv := new(http.Server)
			handler := new(ReqHandler)
			handler.PortConfig = p
			handler.BackendMap = backendmap

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

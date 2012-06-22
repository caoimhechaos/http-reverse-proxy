package main

import (
	"code.google.com/p/goprotobuf/proto"
	"expvar"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
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

var requestsTotal *expvar.Int
var requestsPerHost *expvar.Map
var requestsPerBackend *expvar.Map
var requestErrorsPerHost *expvar.Map
var requestErrorsPerBackend *expvar.Map
var requestErrorsPerError *expvar.Map

var accessLog *log.Logger

// Default Request Handler
func (this *ReqHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var targets *TargetsSpec = this.BackendMap[r.Host]
	var host string;
	var be, initbe *BackendConnection
	var err error

	host, _, err = net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}

	if targets == nil {
		targets = this.BackendMap[host]
	}

	requestsTotal.Add(1)
	requestsPerHost.Add(host, 1)

	if targets == nil {
		http.Error(w, "Host not configured",
			http.StatusServiceUnavailable)
		log.Print("Received request for unknown host " + r.Host)
		requestErrorsPerError.Add("unknown-host", 1)
		AccessLogRequest(accessLog, r, http.StatusServiceUnavailable, -1,
			time.Now())
		return
	}

	// We don't want to terminate the connection to our backend, so let's
	// leave it up to our HTTP client.
	r.Header.Del("Connection")

	// We need to add an XFF header however.
	r.Header.Add("X-Forwarded-For", r.RemoteAddr)

	initbe = targets.GetNextConnection()
	if initbe == nil {
		http.Error(w, "Backends not available",
			http.StatusServiceUnavailable)
		log.Print("Received request for " + r.Host +
			" but all backends are down")
		requestErrorsPerHost.Add(host, 1)
		requestErrorsPerError.Add("no-backends", 1)
		AccessLogRequest(accessLog, r, http.StatusServiceUnavailable, -1,
			time.Now())
		return
	}
	be = initbe
	for {
		requestsPerBackend.Add(be.String(), 1)
		err = be.Do(r, w)

		if err == nil {
			return
		} else {
			log.Print("Error sending request to backend",
				be.String(), ": ", err.Error())
			requestErrorsPerHost.Add(host, 1)
			requestErrorsPerError.Add(err.Error(), 1)
			requestErrorsPerBackend.Add(be.String(), 1)
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
	var accessLogFile *os.File
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

	if config.InfoServer != nil {
		go func(addr string) {
			err = http.ListenAndServe(addr, nil)
			if err != nil {
				log.Fatal("Unable to start info server on ", addr,
					": ", err)
			}
		}(*config.InfoServer)
	}
	
	requestsTotal = expvar.NewInt("requests-total")
	requestsPerHost = expvar.NewMap("requests-per-host")
	requestsPerBackend = expvar.NewMap("requests-per-backend")
	requestErrorsPerHost = expvar.NewMap("request-errors-per-host")
	requestErrorsPerBackend = expvar.NewMap("request-errors-per-backend")
	requestErrorsPerError = expvar.NewMap("request-errors-per-error-type")

	accessLogFile, err = os.OpenFile(*config.AccessLogPath,
		os.O_WRONLY | os.O_APPEND | os.O_SYNC | os.O_CREATE, 0600)
	if err != nil {
		log.Fatal("Unable to open ", *config.AccessLogPath, " for writing: ",
			err)
	}

	accessLog = log.New(accessLogFile, "", 0)

	for _, target := range config.TargetConfig {
		var spec *TargetsSpec = new(TargetsSpec)
		var be_list []*BackendConnection

		for _, backend := range target.Be {
			var dest string = net.JoinHostPort(*backend.Host,
				fmt.Sprintf("%d", *backend.Port))
			var conn *BackendConnection =
				NewBackendConnection(dest, accessLog)

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
				err = srv.ListenAndServeTLS(*p.SslCertPath,
					*p.SslKeyPath)
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

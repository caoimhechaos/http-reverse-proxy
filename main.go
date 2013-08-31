package main

import (
	"ancientsolutions.com/urlconnection"
	"bytes"
	"code.google.com/p/goprotobuf/proto"
	"expvar"
	"flag"
	"fmt"
	"github.com/nranchev/libgeo"
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

	// A map from source countries to their blacklist record.
	Blacklist  map[string]*CountryBlacklistConfig
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

// Reader which supports close.
type byteReadCloser struct {
	bytes.Reader
	r      *bytes.Reader
	closed bool
}

func (b *byteReadCloser) Read(p []byte) (n int, err error) {
	return b.r.Read(p)
}

func (b *byteReadCloser) Close() error {
	if b.closed {
		return http.ErrBodyReadAfterClose
	}
	b.closed = true
	return nil
}

var requestsTotal *expvar.Int
var requestsPerHost *expvar.Map
var requestsPerBackend *expvar.Map
var requestErrorsPerHost *expvar.Map
var requestErrorsPerBackend *expvar.Map
var requestErrorsPerError *expvar.Map
var geoipLookupErrors *expvar.Int
var geoipRejectedRequests *expvar.Int
var geoipRejectedRequestsByCC *expvar.Map

var accessLog *log.Logger

var geoip *libgeo.GeoIP = nil

// Default Request Handler
func (this *ReqHandler) ServeHTTP(w http.ResponseWriter,
	r *http.Request) {
	var targets *TargetsSpec = this.BackendMap[r.Host]
	var body []byte
	var workr http.Request
	var host string
	var be, initbe *BackendConnection
	var numAttempts uint32 = 0
	var closeConnection bool = false
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

	// Determine if we should close the connection.
	if r.Header.Get("Connection") == "close" ||
		r.Header.Get("Connection") == "closed" || r.Close {
		closeConnection = true
		r.Close = false
		w.Header().Add("Connection", "close")
	}

	if targets == nil {
		http.Error(w, "Host not configured",
			http.StatusServiceUnavailable)
		log.Print("Received request for unknown host ", r.Host)
		requestErrorsPerError.Add("unknown-host", 1)
		AccessLogRequest(accessLog, r, http.StatusServiceUnavailable,
			-1, time.Now())
		return
	}

	// We don't want to terminate the connection to our backend, so let's
	// leave it up to our HTTP client.
	r.Header.Del("Connection")

	// We need to add an XFF header however.
	r.Header.Add("X-Forwarded-For", r.RemoteAddr)

	// Determine if there's a geoip block for the requestor.
	if geoip != nil {
		// First we need to extract the host part from the remote addr.
		var rhost string
		var rec *CountryBlacklistConfig
		var ok bool

		rhost, _, err = net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			rhost = r.RemoteAddr
		}

		var loc = geoip.GetLocationByIP(rhost)
		if loc == nil {
			geoipLookupErrors.Add(1)
		} else if rec, ok = this.Blacklist[loc.CountryCode]; ok {
			var whitelisted bool = false

			for _, srvname := range rec.HostWhitelist {
				rhost, _, err =
					net.SplitHostPort(srvname)
				if err != nil {
					rhost = srvname
				}
				if rhost == host {
					whitelisted = true
				}
			}

			if !whitelisted {
				r.Body.Close()

				if rec.ErrorHtml != nil &&
					rec.RedirectUrl != nil {
					w.Header().Add("Refresh",
						fmt.Sprintf("%d; %s",
							*rec.Timeout,
							*rec.RedirectUrl))
				} else if rec.ErrorHtml == nil &&
					rec.RedirectUrl != nil {
					http.Redirect(w, r,
						*rec.RedirectUrl,
						http.StatusFound)
				}

				if rec.ErrorHtml != nil {
					w.Write([]byte(*rec.ErrorHtml))
				}

				geoipRejectedRequests.Add(1)
				geoipRejectedRequestsByCC.Add(
					loc.CountryCode, 1)

				return
			}
		}
	}

	body, err = ioutil.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		http.Error(w, "Error reading request body",
			http.StatusBadRequest)
		log.Print("Received unreadable request body for ", r.Host,
			": ", err.Error())
		requestErrorsPerError.Add(err.Error(), 1)
		AccessLogRequest(accessLog, r, http.StatusBadRequest, -1,
			time.Now())
		return
	}

	initbe = targets.GetNextConnection()
	if initbe == nil {
		http.Error(w, "Backends not available",
			http.StatusServiceUnavailable)
		log.Print("Received request for " + r.Host +
			" but all backends are down")
		requestErrorsPerHost.Add(host, 1)
		requestErrorsPerError.Add("no-backends", 1)
		AccessLogRequest(accessLog, r,
			http.StatusServiceUnavailable, -1, time.Now())
		return
	}
	be = initbe
	for {
		workr = *r
		workr.Body = &byteReadCloser{
			r: bytes.NewReader(body),
		}

		requestsPerBackend.Add(be.String(), 1)
		err = be.Do(&workr, w, closeConnection)

		if err == nil {
			return
		}

		// Error sending request to client, we'll have to
		// log it and try the next backend.
		log.Print("Error sending request to backend",
			be.String(), ": ", err.Error())
		requestErrorsPerHost.Add(host, 1)
		requestErrorsPerError.Add(err.Error(), 1)
		requestErrorsPerBackend.Add(be.String(), 1)
		go be.CheckAndReconnect(err)

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
			if numAttempts >= 4 {
				http.Error(w, "Backends not available",
					http.StatusServiceUnavailable)
				log.Print("Received request for " + r.Host +
					" but all backends have errors")
				requestErrorsPerHost.Add(host, 1)
				requestErrorsPerError.Add("backend-errors", 1)
				AccessLogRequest(accessLog, r,
					http.StatusServiceUnavailable, -1,
					time.Now())
				break
			} else {
				// We should wait for a bit here because
				// we may be waiting for some reconnects.
				// TODO(tonnerre): This could be more
				// intelligent.
				time.Sleep(50 * (2 << numAttempts) *
					time.Millisecond)
				numAttempts = numAttempts + 1
			}
		}
	}
}

func main() {
	var filename = flag.String("config", "", "Path to configuration file")
	var timeout = flag.Int64("timeout", 120,
		"Client connection timeout in seconds")
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
				log.Fatal("Unable to start info server on ",
					addr, ": ", err)
			}
		}(*config.InfoServer)
	}

	if config.DoozerUri != nil {
		var buri string

		if config.DoozerBootUri != nil {
			buri = *config.DoozerBootUri
		}

		err = urlconnection.SetupDoozer(buri, *config.DoozerUri)
		if err != nil {
			log.Fatal("Unable to setup Doozer connection ",
			*config.DoozerUri, ": ", err)
		}
	}

	if config.GeoipPath != nil {
		geoip, err = libgeo.Load(*config.GeoipPath)
		if err != nil {
			log.Fatal("Unable to open geoip database: ",
				err)
		}
	} else if len(config.Blacklist) > 0 {
		log.Fatal("Blacklist given without geoip path")
	}

	requestsTotal = expvar.NewInt("requests-total")
	requestsPerHost = expvar.NewMap("requests-per-host")
	requestsPerBackend = expvar.NewMap("requests-per-backend")
	requestErrorsPerHost = expvar.NewMap("request-errors-per-host")
	requestErrorsPerBackend = expvar.NewMap("request-errors-per-backend")
	requestErrorsPerError = expvar.NewMap("request-errors-per-error-type")
	geoipLookupErrors = expvar.NewInt("geoip-lookup-errors")
	geoipRejectedRequests = expvar.NewInt("geoip-rejected-requests")
	geoipRejectedRequestsByCC = expvar.NewMap(
		"geoip-rejected-requests-by-cc")

	accessLogFile, err = os.OpenFile(*config.AccessLogPath,
		os.O_WRONLY|os.O_APPEND|os.O_SYNC|os.O_CREATE, 0600)
	if err != nil {
		log.Fatal("Unable to open ", *config.AccessLogPath,
			" for writing: ", err)
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

		for _, backend := range target.BackendUri {
			var conn *BackendConnection

			conn = NewBackendFromURL(backend, accessLog)
			be_list = append(be_list, conn)
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
			handler.Blacklist =
				make(map[string]*CountryBlacklistConfig)

			for _, rec := range config.Blacklist {
				for _, name := range rec.Country {
					handler.Blacklist[name] =
						rec
				}
			}

			srv.Addr = ":" + fmt.Sprint(*p.Port)
			srv.Handler = handler

			// Set some sensible timeouts so we don't run out
			// of connections.
			srv.ReadTimeout = time.Minute
			srv.WriteTimeout = time.Duration(*timeout) *
				time.Second

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

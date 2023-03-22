//go:build linux

package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Get the RTT from the OS itself rather than timing it ourselves
// NB: Only works on linux
func tcpOsRtt(conn *net.TCPConn) (int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}

	var info *unix.TCPInfo
	ctrlErr := raw.Control(func(fd uintptr) {
		info, err = unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO)
	})
	switch {
	case ctrlErr != nil:
		return 0, ctrlErr
	case err != nil:
		return 0, err
	}
	return int(info.Rtt), nil
}

func recordLatencies(ticker *time.Ticker) {
	for range ticker.C {
		for _, r := range regionLatencies {
			// connect over TCP to all the servers
			conn, err := net.Dial("tcp", r.host)
			if err != nil {
				log.Printf("Unable to connect to %s: %v", r.region, err)
				continue
			}

			// tell the server your source region
			fmt.Fprintf(conn, currRegion+"\n")

			// read the server's region
			scanner := bufio.NewScanner(conn)
			scanner.Scan()
			serverRegion := scanner.Text()

			// get the RTT
			latency, err := tcpOsRtt(conn.(*net.TCPConn))
			conn.Close()
			if err != nil {
				log.Printf("Unable to extract rtt from tcp conn on client to %s: %v", r.region, err)
				continue
			}

			// update the prometheus metrics
			r.hist.Observe(float64(latency))
			r.last = latency

			log.Printf("C:\t%s\t%s\t%d", currRegion, serverRegion, latency)
		}
	}
}

type regionData struct {
	hist   prometheus.Histogram
	last   int    // the last latency reading
	region string // the shortened region name to which this a client connected
	host   string // hostname for connecting to region
}

func NewRegion(r string) *regionData {
	return &regionData{
		hist: promauto.NewHistogram(
			prometheus.HistogramOpts{
				Name: fmt.Sprintf("latency_%s_to_%s_microsecond", currRegion, r),
			}),
		region: r,
		host:   fmt.Sprintf("%s.%s.internal:%s", r, appName, tcpPort),
	}
}

var regionLatencies = make(map[string]*regionData)

// TXT records contain all the deployed regions
// At some interval, refresh the information and create new regions if they don't exist
func updateRegions(ticker *time.Ticker) {
	for range ticker.C {
		entries, err := net.LookupTXT(fmt.Sprintf("regions.%s.internal", appName))
		if err != nil {
			log.Printf("TXT lookup for all deployed regions failed: %v", err)
		}
		if len(entries) == 0 {
			log.Printf("No TXT records, skipping update")
			continue
		}
		if len(entries) > 1 {
			log.Printf("Multiple TXT records, using first")
		}
		entries = strings.Split(entries[0], ",")
		// TODO: Drop old regions from the map?
		for _, r := range entries {
			if _, ok := regionLatencies[r]; !ok {
				regionLatencies[r] = NewRegion(r)
			}
		}
	}
}

// simple HTTP method to get all the latencies to all other regions in the given region
func getLatencies(w http.ResponseWriter, r *http.Request) {
	for _, r := range regionLatencies {
		io.WriteString(w, fmt.Sprintf("%s\t%s\t%d\n", currRegion, r.region, r.last))
	}
}

// listen for clients (peers) on TCP so they can measure latency to you
func runTcpPingServer() {
	listener, err := net.Listen("tcp", ":"+tcpPort)
	if err != nil {
		log.Fatal(err)
	}

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Failed to accept a client connection: %v", err)
		}
		go func(c *net.TCPConn) {
			defer c.Close()
			// send your region to the client
			fmt.Fprintf(c, currRegion+"\n")

			// read the client's region
			scanner := bufio.NewScanner(c)
			scanner.Scan()
			clientRegion := scanner.Text()

			// record what the server's perceived latency is
			latency, err := tcpOsRtt(c)
			if err != nil {
				log.Printf("Unable to extract rtt from tcp conn on server: %v", err)
				return
			}

			log.Printf("S:\t%s\t%s\t%d", currRegion, clientRegion, latency)
			//hold the conn open for the client so everything can close cleanly
			time.Sleep(250 * time.Millisecond)

		}(conn.(*net.TCPConn))
	}
}

var appNameEnvVar = "FLY_APP_NAME"
var appName = ""
var currRegionEnvVar = "FLY_REGION"
var currRegion = ""
var regionRefreshRate = 10 * time.Second
var latencyRefreshRate = 1 * time.Second
var tcpPort = "10000"
var httpPort = "9091"

func main() {

	var ok bool

	currRegion, ok = os.LookupEnv(currRegionEnvVar)
	if !ok || len(currRegion) == 0 {
		log.Fatal(fmt.Sprintf("%s is unset", currRegionEnvVar))
	}
	appName, ok = os.LookupEnv(appNameEnvVar)
	if !ok || len(currRegion) == 0 {
		log.Fatal(fmt.Sprintf("%s is unset", appNameEnvVar))
	}

	regionRefreshTicker := time.NewTicker(regionRefreshRate)
	defer regionRefreshTicker.Stop()
	go updateRegions(regionRefreshTicker)

	updateLatencyTicker := time.NewTicker(latencyRefreshRate)
	defer updateLatencyTicker.Stop()
	go recordLatencies(updateLatencyTicker)

	go runTcpPingServer()

	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/", getLatencies)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(currRegion))
	})
	log.Fatal(http.ListenAndServe(":"+httpPort, nil))

}

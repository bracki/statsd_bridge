// Copyright (c) 2013, Prometheus Team
// All rights reserved.
//
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"strconv"

	"github.com/howeyc/fsnotify"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	listenAddress       = flag.String("web.listen-address", ":9102", "The address on which to expose the web interface and generated Prometheus metrics.")
	metricsEndpoint     = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
	statsdListenAddress = flag.String("statsd.listen-address", ":9125", "The UDP address on which to receive statsd metric lines.")
	mappingConfig       = flag.String("statsd.mapping-config", "", "Metric mapping configuration file name.")
)

func serveHTTP() {
	http.Handle(*metricsEndpoint, prometheus.Handler())
	http.ListenAndServe(*listenAddress, nil)
}

func udpAddrFromString(addr string) *net.UDPAddr {
	host, portStr, err := net.SplitHostPort(*statsdListenAddress)
	if err != nil {
		log.Fatal("Bad StatsD listening address", *statsdListenAddress)
	}

	if host == "" {
		host = "0.0.0.0"
	}
	ip, err := net.ResolveIPAddr("ip", host)
	if err != nil {
		log.Fatalf("Unable to resolve %s: %s", host, err)
	}

	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		log.Fatal("Bad port %s: %s", portStr, err)
	}

	return &net.UDPAddr{
		IP:   ip.IP,
		Port: port,
		Zone: ip.Zone,
	}
}

func watchConfig(fileName string, mapper *metricMapper) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}

	err = watcher.WatchFlags(fileName, fsnotify.FSN_MODIFY)
	if err != nil {
		log.Fatal(err)
	}

	for {
		select {
		case ev := <-watcher.Event:
			log.Printf("Config file changed (%s), attempting reload", ev)
			err = mapper.initFromFile(fileName)
			if err != nil {
				log.Println("Error reloading config:", err)
				configLoads.WithLabelValues("failure").Inc()
			} else {
				log.Println("Config reloaded successfully")
				configLoads.WithLabelValues("success").Inc()
			}
			// Re-add the file watcher since it can get lost on some changes. E.g.
			// saving a file with vim results in a RENAME-MODIFY-DELETE event
			// sequence, after which the newly written file is no longer watched.
			err = watcher.WatchFlags(fileName, fsnotify.FSN_MODIFY)
		case err := <-watcher.Error:
			log.Println("Error watching config:", err)
		}
	}
}

func main() {
	flag.Parse()

	log.Println("Starting StatsD -> Prometheus Bridge...")
	log.Println("Accepting StatsD Traffic on", *statsdListenAddress)
	log.Println("Accepting Prometheus Requests on", *listenAddress)

	go serveHTTP()

	events := make(chan Events, 1024)
	defer close(events)

	listenAddr := udpAddrFromString(*statsdListenAddress)
	conn, err := net.ListenUDP("udp", listenAddr)
	if err != nil {
		log.Fatal(err)
	}
	l := &StatsDListener{conn: conn}
	go l.Listen(events)

	mapper := &metricMapper{}
	if *mappingConfig != "" {
		err := mapper.initFromFile(*mappingConfig)
		if err != nil {
			log.Fatal("Error loading config:", err)
		}
		go watchConfig(*mappingConfig, mapper)
	}
	bridge := NewBridge(mapper)
	bridge.Listen(events)
}

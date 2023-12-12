// Copyright (C) 2018 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package serve

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/lib/pq" // PostgreSQL driver
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/syncthing/syncthing/cmd/ursrv/blob"
	"github.com/syncthing/syncthing/cmd/ursrv/report"
	"github.com/syncthing/syncthing/lib/upgrade"
	"github.com/syncthing/syncthing/lib/ur/contract"
)

type CLI struct {
	Debug     bool   `env:"UR_DEBUG"`
	Listen    string `env:"UR_LISTEN" default:"0.0.0.0:8080"`
	GeoIPPath string `env:"UR_GEOIP" default:"GeoLite2-City.mmdb"`
}

//go:embed static
var statics embed.FS

var (
	tpl              *template.Template
	progressBarClass = []string{"", "progress-bar-success", "progress-bar-info", "progress-bar-warning", "progress-bar-danger"}
	blocksToGb       = float64(8 * 1024)
)

// Functions used in the GUI (index.html)
var funcs = map[string]interface{}{
	"commatize":  commatize,
	"number":     number,
	"proportion": proportion,
	"counter": func() *counter {
		return &counter{}
	},
	"progressBarClassByIndex": func(a int) string {
		return progressBarClass[a%len(progressBarClass)]
	},
	"slice": func(numParts, whichPart int, input []report.Feature) []report.Feature {
		var part []report.Feature
		perPart := (len(input) / numParts) + len(input)%2

		parts := make([][]report.Feature, 0, numParts)
		for len(input) >= perPart {
			part, input = input[:perPart], input[perPart:]
			parts = append(parts, part)
		}
		if len(input) > 0 {
			parts = append(parts, input)
		}
		return parts[whichPart-1]
	},
}

func (cli *CLI) Run(store *blob.UrsrvStore) error {
	// Template
	fd, err := statics.Open("static/index.html")
	if err != nil {
		log.Fatalln("template:", err)
	}
	bs, err := io.ReadAll(fd)
	if err != nil {
		log.Fatalln("template:", err)
	}
	fd.Close()
	tpl = template.Must(template.New("index.html").Funcs(funcs).Parse(string(bs)))

	// Listening
	listener, err := net.Listen("tcp", cli.Listen)
	if err != nil {
		log.Fatalln("listen:", err)
	}

	srv := &server{
		store:             store,
		debug:             cli.Debug,
		geoIPPath:         cli.GeoIPPath,
		cachedBlockstats:  newBlockStats(),
		cachedPerformance: newPerformance(),
		cachedSummary:     newSummary(),
	}
	http.HandleFunc("/", srv.rootHandler)
	http.HandleFunc("/newdata", srv.newDataHandler)
	http.HandleFunc("/summary.json", srv.summaryHandler)
	http.HandleFunc("/performance.json", srv.performanceHandler)
	http.HandleFunc("/blockstats.json", srv.blockStatsHandler)
	http.HandleFunc("/locations.json", srv.locationsHandler)
	http.Handle("/metrics", promhttp.Handler())
	http.Handle("/static/", http.FileServer(http.FS(statics)))

	go srv.cacheRefresher()

	httpSrv := http.Server{
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 15 * time.Second,
	}
	return httpSrv.Serve(listener)
}

type server struct {
	debug     bool
	geoIPPath string
	store     *blob.UrsrvStore

	cacheMut           sync.Mutex
	cachedLatestReport report.AggregatedReport
	cachedSummary      summary
	cachedPerformance  [][]interface{}
	cachedBlockstats   [][]interface{}
	cacheTime          time.Time
}

// TESTING VALUE
const maxCacheTime = 2 * time.Minute

func (s *server) cacheRefresher() {
	ticker := time.NewTicker(maxCacheTime - time.Minute)
	defer ticker.Stop()
	for ; true; <-ticker.C {
		s.cacheMut.Lock()
		if err := s.refreshCacheLocked(); err != nil {
			log.Println(err)
		}
		s.cacheMut.Unlock()
	}
}

func (s *server) refreshCacheLocked() error {
	rep, err := s.store.LastAggregatedReport()
	if err != nil {
		return err
	}

	s.cachedLatestReport = rep
	var reportsToCache []report.AggregatedReport
	if s.cachedLatestReport.Date.IsZero() {
		reportsToCache, err = s.store.ListAggregatedReports()
		if err != nil {
			return err
		}
	} else if rep.Date.After(s.cachedLatestReport.Date) {
		reportsToCache = append(reportsToCache, rep)
	}

	if len(reportsToCache) > 0 {
		s.cacheGraphData(reportsToCache)
	}

	s.cacheTime = time.Now()

	return nil
}

func (s *server) rootHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" || r.URL.Path == "/index.html" {
		s.cacheMut.Lock()
		defer s.cacheMut.Unlock()

		if time.Since(s.cacheTime) > maxCacheTime {
			if err := s.refreshCacheLocked(); err != nil {
				log.Println(err)
				http.Error(w, "Template Error", http.StatusInternalServerError)
				return
			}
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		buf := new(bytes.Buffer)
		err := tpl.Execute(buf, s.cachedLatestReport)
		if err != nil {
			return
		}
		w.Write(buf.Bytes())
	} else {
		http.Error(w, "Not found", 404)
		return
	}
}

func (s *server) locationsHandler(w http.ResponseWriter, _ *http.Request) {
	s.cacheMut.Lock()
	defer s.cacheMut.Unlock()

	if time.Since(s.cacheTime) > maxCacheTime {
		if err := s.refreshCacheLocked(); err != nil {
			log.Println(err)
			http.Error(w, "Template Error", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	locations, _ := json.Marshal(s.cachedLatestReport.Locations)
	w.Write(locations)
}

func (s *server) newDataHandler(w http.ResponseWriter, r *http.Request) {
	version := "fail"
	defer func() {
		// Version is "fail", "duplicate", "v2", "v3", ...
		metricReportsTotal.WithLabelValues(version).Inc()
	}()

	defer r.Body.Close()

	addr := r.Header.Get("X-Forwarded-For")
	if addr != "" {
		addr = strings.Split(addr, ", ")[0]
	} else {
		addr = r.RemoteAddr
	}

	if host, _, err := net.SplitHostPort(addr); err == nil {
		addr = host
	}

	if net.ParseIP(addr) == nil {
		addr = ""
	}

	var rep contract.Report
	received := time.Now().UTC()
	rep.Date = received.Format(time.DateOnly)
	rep.Address = addr

	lr := &io.LimitedReader{R: r.Body, N: 40 * 1024}
	bs, _ := io.ReadAll(lr)
	if err := json.Unmarshal(bs, &rep); err != nil {
		log.Println("decode:", err)
		if s.debug {
			log.Printf("%s", bs)
		}
		http.Error(w, "JSON Decode Error", http.StatusInternalServerError)
		return
	}

	if err := rep.Validate(); err != nil {
		log.Println("validate:", err)
		if s.debug {
			log.Printf("%#v", rep)
		}
		http.Error(w, "Validation Error", http.StatusInternalServerError)
		return
	}

	if err := s.store.PutUsageReport(rep, received); err != nil {
		if err.Error() == "already exists" {
			// We already have a report today for the same unique ID; drop
			// this one without complaining.
			version = "duplicate"
			return
		}

		if s.debug {
			log.Printf("%#v", rep)
		}
		http.Error(w, "Store Error", http.StatusInternalServerError)
		return
	}

	version = fmt.Sprintf("v%d", rep.URVersion)
}

func (s *server) summaryHandler(w http.ResponseWriter, r *http.Request) {
	min, _ := strconv.Atoi(r.URL.Query().Get("min"))
	s.cacheMut.Lock()
	defer s.cacheMut.Unlock()

	if time.Since(s.cacheTime) > maxCacheTime {
		if err := s.refreshCacheLocked(); err != nil {
			log.Println(err)
			http.Error(w, "Template Error", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	s.cachedSummary.filter(min)
	summary, _ := s.cachedSummary.MarshalJSON()
	w.Write(summary)
}

func (s *server) performanceHandler(w http.ResponseWriter, _ *http.Request) {
	s.cacheMut.Lock()
	defer s.cacheMut.Unlock()

	if time.Since(s.cacheTime) > maxCacheTime {
		if err := s.refreshCacheLocked(); err != nil {
			log.Println(err)
			http.Error(w, "Template Error", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	performance, _ := json.Marshal(s.cachedPerformance)
	w.Write(performance)
}

func (s *server) blockStatsHandler(w http.ResponseWriter, _ *http.Request) {
	s.cacheMut.Lock()
	defer s.cacheMut.Unlock()

	if time.Since(s.cacheTime) > maxCacheTime {
		if err := s.refreshCacheLocked(); err != nil {
			log.Println(err)
			http.Error(w, "Template Error", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	blockstats, _ := json.Marshal(s.cachedBlockstats)
	w.Write(blockstats)
}

func (s *server) cacheGraphData(reports []report.AggregatedReport) {
	for _, rep := range reports {
		date := rep.Date.UTC().Format(time.DateOnly)

		s.cachedSummary.setCount(date, rep.VersionCount)
		if blockStats := parseBlockStats(date, rep.Nodes, rep.BlockStats); blockStats != nil {
			s.cachedBlockstats = append(s.cachedBlockstats, blockStats)
		}
		s.cachedPerformance = append(s.cachedPerformance, []interface{}{
			date, rep.Performance.TotFiles, rep.Performance.TotMib, float64(int(rep.Performance.Sha256Perf*10)) / 10, rep.Performance.MemorySize, rep.Performance.MemoryUsageMib,
		})
	}
}

func newBlockStats() [][]interface{} {
	return [][]interface{}{
		{"Day", "Number of Reports", "Transferred (GiB)", "Saved by renaming files (GiB)", "Saved by resuming transfer (GiB)", "Saved by reusing data from old file (GiB)", "Saved by reusing shifted data from old file (GiB)", "Saved by reusing data from other files (GiB)"},
	}
}

func parseBlockStats(date string, reports int, blockStats report.BlockStats) []interface{} {
	// Legacy bad data on certain days
	if reports <= 0 || !blockStats.Valid() {
		return nil
	}

	return []interface{}{
		date,
		reports,
		blockStats.Pulled / blocksToGb,
		blockStats.Renamed / blocksToGb,
		blockStats.Reused / blocksToGb,
		blockStats.CopyOrigin / blocksToGb,
		blockStats.CopyOriginShifted / blocksToGb,
		blockStats.CopyElsewhere / blocksToGb,
	}
}

func newPerformance() [][]interface{} {
	return [][]interface{}{
		{"Day", "TotFiles", "TotMiB", "SHA256Perf", "MemorySize", "MemoryUsageMiB"},
	}
}

type summary struct {
	versions map[string]int   // version string to count index
	max      map[string]int   // version string to max users per day
	rows     map[string][]int // date to list of counts
}

func newSummary() summary {
	return summary{
		versions: make(map[string]int),
		max:      make(map[string]int),
		rows:     make(map[string][]int),
	}
}

func (s *summary) setCount(date string, versions map[string]int) {
	for version, count := range versions {
		idx, ok := s.versions[version]
		if !ok {
			idx = len(s.versions)
			s.versions[version] = idx
		}

		if s.max[version] < count {
			s.max[version] = count
		}

		row := s.rows[date]
		if len(row) <= idx {
			old := row
			row = make([]int, idx+1)
			copy(row, old)
			s.rows[date] = row
		}

		row[idx] = count
	}
}

func (s *summary) MarshalJSON() ([]byte, error) {
	var versions []string
	for v := range s.versions {
		versions = append(versions, v)
		println(v)
	}
	sort.Slice(versions, func(a, b int) bool {
		return upgrade.CompareVersions(versions[a], versions[b]) < 0
	})

	var filtered []string
	for _, v := range versions {
		if s.max[v] > 50 {
			filtered = append(filtered, v)
		}
	}
	versions = filtered

	headerRow := []interface{}{"Day"}
	for _, v := range versions {
		headerRow = append(headerRow, v)
	}

	var table [][]interface{}
	table = append(table, headerRow)

	var dates []string
	for k := range s.rows {
		dates = append(dates, k)
	}
	sort.Strings(dates)

	for _, date := range dates {
		row := []interface{}{date}
		for _, ver := range versions {
			idx := s.versions[ver]
			if len(s.rows[date]) > idx && s.rows[date][idx] > 0 {
				row = append(row, s.rows[date][idx])
			} else {
				row = append(row, nil)
			}
		}
		table = append(table, row)
	}

	return json.Marshal(table)
}

// filter removes versions that never reach the specified min count.
func (s *summary) filter(min int) {
	// We cheat and just remove the versions from the "index" and leave the
	// data points alone. The version index is used to build the table when
	// we do the serialization, so at that point the data points are
	// filtered out as well.
	for ver := range s.versions {
		if s.max[ver] < min {
			delete(s.versions, ver)
			delete(s.max, ver)
		}
	}
}

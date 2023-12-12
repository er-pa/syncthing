package blob

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/syncthing/syncthing/cmd/ursrv/report"
	"github.com/syncthing/syncthing/lib/ur/contract"
)

const (
	USAGE_PREFIX      = "UR" // contract.Report
	AGGREGATED_PREFIX = "AR" // report.AggregatedReport
)

func NewBlobStorage() Store {
	// Some blob storage.
	// return blob.NewAzure()/NewS3()/...

	// Fall back on local storage.
	dir, err := os.UserHomeDir()
	if err != nil {
		log.Println("Could not get user home directory", "error", err)
		dir = os.TempDir()
	}

	dir = filepath.Join(dir, ".ursrv", "blob")
	log.Println("Using local blob storage", "dir", dir)

	return NewDisk(dir)
}

type Store interface {
	Put(key string, data []byte) error
	Get(key string) ([]byte, error)
	Delete(key string) error
	Iterate(key string, fn func([]byte) bool) error
}

type UrsrvStore struct {
	Store
}

func NewUrsrvStore(s Store) *UrsrvStore {
	return &UrsrvStore{s}
}

func usageReportKey(when time.Time, uniqueId string) string {
	return fmt.Sprintf("%s~%s-%s", USAGE_PREFIX, when.Format(time.DateOnly), uniqueId)
}

func aggregatedReportKey(when time.Time) string {
	return fmt.Sprintf("%s~%s", AGGREGATED_PREFIX, when.Format(time.DateOnly))
}

func (m *UrsrvStore) PutUsageReport(rep contract.Report, received time.Time) error {
	key := usageReportKey(received, rep.UniqueID)

	// Check if we already have a report for this instance from today.
	if data, err := m.Get(key); err == nil && len(data) != 0 {
		return errors.New("already exists")
	}

	bs, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	return m.Put(key, bs)
}

func (m *UrsrvStore) PutAggregatedReport(rep *report.AggregatedReport) error {
	key := aggregatedReportKey(rep.Date)
	bs, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	return m.Put(key, bs)
}

func (m *UrsrvStore) ListUsageReportsForDate(when time.Time) ([]contract.Report, error) {
	key := usageReportKey(when, "")

	var res []contract.Report
	var rep contract.Report

	err := m.Store.Iterate(key, func(b []byte) bool {
		err := json.Unmarshal(b, &rep)
		if err != nil {
			return true
		}
		res = append(res, rep)
		return true
	})

	return res, err
}

func (m *UrsrvStore) ListAggregatedReports() ([]report.AggregatedReport, error) {
	key := AGGREGATED_PREFIX

	var res []report.AggregatedReport
	var rep report.AggregatedReport
	err := m.Store.Iterate(key, func(b []byte) bool {
		err := json.Unmarshal(b, &rep)
		if err != nil {
			return true
		}
		res = append(res, rep)
		return true
	})

	return res, err
}

func (m *UrsrvStore) LastAggregatedReport() (report.AggregatedReport, error) {
	var rep report.AggregatedReport

	date := time.Now().UTC().AddDate(0, 0, -1)
	key := aggregatedReportKey(date)
	data, err := m.Store.Get(key)
	if err != nil {
		return rep, errors.New("no aggregated report found")
	}

	err = json.Unmarshal(data, &rep)

	return rep, err
}

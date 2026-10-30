package geoip

import (
	"fmt"
	"net/netip"
	"strings"

	maxminddb "github.com/oschwald/maxminddb-golang/v2"
)

type mmdbReader struct {
	reader *maxminddb.Reader
}

func openMMDB(path string) (Reader, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("geoip: empty MMDB path")
	}
	reader, err := maxminddb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("geoip: open MMDB: %w", err)
	}
	if err := reader.Verify(); err != nil {
		_ = reader.Close()
		return nil, fmt.Errorf("geoip: verify MMDB: %w", err)
	}
	databaseType := strings.ToLower(reader.Metadata.DatabaseType)
	if !strings.Contains(databaseType, "country") && !strings.Contains(databaseType, "city") {
		_ = reader.Close()
		return nil, fmt.Errorf("geoip: MMDB is not a Country-compatible database (type %q)", reader.Metadata.DatabaseType)
	}
	return &mmdbReader{reader: reader}, nil
}

func (r *mmdbReader) LookupCountry(addr netip.Addr) (string, bool, error) {
	if r == nil || r.reader == nil {
		return "", false, fmt.Errorf("geoip: nil MMDB reader")
	}
	var record struct {
		Country struct {
			ISOCode string `maxminddb:"iso_code"`
		} `maxminddb:"country"`
	}
	if err := r.reader.Lookup(addr.Unmap()).Decode(&record); err != nil {
		return "", false, err
	}
	country := strings.ToUpper(strings.TrimSpace(record.Country.ISOCode))
	return country, country != "", nil
}

func (r *mmdbReader) Close() error {
	if r == nil || r.reader == nil {
		return nil
	}
	return r.reader.Close()
}

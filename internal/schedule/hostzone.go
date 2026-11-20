package schedule

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type hostZoneResolver func() (*time.Location, string, error)

func systemHostZone() (*time.Location, string, error) {
	return readHostZone("/etc/localtime")
}

// Read the active OS zone, including copied TZif files. time.Local caches its
// initial rules for the lifetime of a process, which can include host travel.
func readHostZone(path string) (*time.Location, string, error) {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, "", fmt.Errorf("resolve host time zone: %w", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return nil, "", fmt.Errorf("read host time zone: %w", err)
	}
	name := "Host local time"
	id := ""
	if _, zone, ok := strings.Cut(target, "/zoneinfo/"); ok && zone != "" {
		name, id = zone, zone
	}
	loc, err := time.LoadLocationFromTZData(name, data)
	if err != nil {
		return nil, "", fmt.Errorf("load host time zone: %w", err)
	}
	if id == "" {
		// A copied zone file has no IANA name; its rules identify a change.
		id = fmt.Sprintf("%x", sha256.Sum256(data))
	}
	return loc, id, nil
}

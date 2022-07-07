package main

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pyke369/golang-support/acl"
	"github.com/pyke369/golang-support/jsonrpc"
	"github.com/pyke369/golang-support/rcache"
	"github.com/pyke369/golang-support/uconfig"
	"github.com/pyke369/golang-support/uuid"
)

var probeId = uuid.UUID()

func Probe(remote string, ctx context.Context) (result [][]any) {
	cache := Config.GetString(PROGNAME+".probe.cache", filepath.Join("/tmp", PROGNAME))
	if data, err := ioutil.ReadFile(filepath.Join(cache, remote)); err == nil {
		if json.Unmarshal(data, &result) == nil {
			return
		}
	}

	result = [][]any{}
	matcher, blacklist := rcache.Get(`^(?:(\S+)\s+\()?([^)]+)\)?$`), Config.GetStrings(PROGNAME+".probe.blacklist")
	if len(blacklist) == 0 {
		blacklist = []string{"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10"}
	}
	remove, anonymize := Config.GetStrings(PROGNAME+".probe.remove"), Config.GetStrings(PROGNAME+".probe.anonymize")
	if len(anonymize) == 0 {
		anonymize = []string{"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10"}
	}
	if !acl.CIDR(remote, blacklist) {
		parts := strings.Split(strings.ReplaceAll(Config.GetString(PROGNAME+".probe.command", "mtr -j -b -i 0.5 -G 1 -c 7 {{remote}}"), "{{remote}}", remote), " ")
		command, start := exec.CommandContext(ctx, parts[0], parts[1:]...), time.Now()
		if output, err := command.Output(); err == nil {
			var probe map[string]any

			if json.Unmarshal(output, &probe) == nil {
				for _, entry := range jsonrpc.Slice(jsonrpc.Map(probe["report"])["hubs"]) {
					country, asnumber, asname, values := "", "", "", jsonrpc.Map(entry)
					if captures := matcher.FindStringSubmatch(jsonrpc.String(values["host"])); captures != nil {
						if captures[2] != "???" {
							if len(remove) > 0 && acl.CIDR(captures[2], remove) {
								continue
							}
							if acl.CIDR(captures[2], anonymize) {
								captures[1] = fmt.Sprintf("%x", md5.Sum([]byte(captures[2]+probeId)))
							}
							if captures[1] == "" {
								captures[1] = captures[2]
							}
							geo := GeoLookup(captures[2])
							country, asnumber, asname = strings.ToLower(jsonrpc.String(geo["country_code"])), jsonrpc.String(geo["as_number"]), jsonrpc.String(geo["as_name"])
						} else {
							captures[1] = ""
						}
						result = append(result, []any{
							strings.ToLower(captures[1]),
							country, asnumber, asname,
							jsonrpc.Number(values["Last"]),
							jsonrpc.Number(values["Wrst"]),
							jsonrpc.Number(values["Avg"]),
							jsonrpc.Number(values["Best"]),
							jsonrpc.Number(values["StDev"]),
							jsonrpc.Number(values["Loss%"]),
						})
					}
				}
			}
		}
		geo := GeoLookup(remote)
		Logger.Info(map[string]any{
			"scope":    "probe",
			"event":    "probe",
			"remote":   remote,
			"country":  strings.ToLower(jsonrpc.String(geo["country_code"])),
			"asnum":    jsonrpc.String(geo["as_number"]),
			"asname":   jsonrpc.String(geo["as_name"]),
			"hops":     len(result),
			"duration": fmt.Sprintf("%.3fs", float64(time.Now().Sub(start))/float64(time.Second)),
		})
		if data, err := json.Marshal(result); err == nil {
			os.MkdirAll(cache, 0755)
			ioutil.WriteFile(filepath.Join(cache, remote), data, 0644)
		}
	}

	return
}

func ProbeCleanup() {
	for range time.Tick(time.Minute) {
		cache, expire := Config.GetString(PROGNAME+".probe.cache", filepath.Join("/tmp", PROGNAME)), uconfig.Duration(Config.GetDurationBounds(PROGNAME+".probe.expire", 600, 60, 3600))
		if entries, err := ioutil.ReadDir(cache); err == nil {
			for _, entry := range entries {
				if time.Now().Sub(entry.ModTime()) >= expire {
					os.Remove(filepath.Join(cache, entry.Name()))
					Logger.Info(map[string]any{"scope": "probe", "event": "cleanup", "remote": entry.Name()})
				}
			}
		}
	}
}

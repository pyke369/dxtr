package main

import (
	"net"

	"github.com/pyke369/golang-support/prefixdb"
)

var geoBases = []*prefixdb.PrefixDB{}

func GeoLoad() {
	bases := []*prefixdb.PrefixDB{}
	for _, path := range Config.GetPaths(PROGNAME + ".geo") {
		base := prefixdb.New()
		if err := base.Load(Config.GetString(path, "")); err == nil {
			bases = append(bases, base)
			Logger.Info(map[string]any{"scope": "geo", "event": "load", "source": base.Path, "description": base.Description})
		}
	}
	geoBases = bases
}

func GeoLookup(input string) (output map[string]any) {
	output = map[string]any{}
	remote, _, err := net.SplitHostPort(input)
	if err != nil {
		remote = input
	}
	if remote := net.ParseIP(remote); remote != nil {
		for _, base := range geoBases {
			output, _ = base.Lookup(remote, output)
		}
	}
	return
}

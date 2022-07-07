package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"html/template"
	"io/ioutil"
	_ "log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pyke369/golang-support/acl"
	"github.com/pyke369/golang-support/dynacert"
	"github.com/pyke369/golang-support/jsonrpc"
	"github.com/pyke369/golang-support/listener"
	"github.com/pyke369/golang-support/uconfig"
)

var htmlTemplate = template.New("index")

func httpHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		remote, _, err := net.SplitHostPort(request.RemoteAddr)
		if err != nil {
			remote = request.RemoteAddr
		}
		forward := Config.GetStrings(PROGNAME + ".http.forward")
		if len(forward) == 0 {
			forward = []string{"127.0.0.0/8"}
		}
		if acl.CIDR(request.RemoteAddr, forward) {
			if value := request.Header.Get("X-Forwarded-For"); value != "" {
				remote = value
			}
		}
		if overload := Config.GetString(PROGNAME+".http.overload", ""); overload != "" {
			if value := request.Header.Get("X-" + overload); value != "" {
				remote = value
			}
			parameters := request.URL.Query()
			if value := parameters.Get(overload); value != "" {
				remote = value
			}
		}

		response.Header().Set("Server", PROGNAME+"/"+VERSION)
		switch request.URL.Path {
		case Config.GetString(PROGNAME+".http.probe", "/probe"):
			if request.Method != http.MethodGet {
				response.WriteHeader(http.StatusMethodNotAllowed)
				return
			}

			workers, result := Config.GetPaths(PROGNAME+".probe.workers"), []any{}
			if len(workers) > 0 {
				client := &http.Client{Timeout: uconfig.Duration(Config.GetDurationBounds(PROGNAME+".http.write_timeout", 30, 5, 30) - 2)}
				queue, waiter, ctx := make(chan map[string]any, len(workers)), sync.WaitGroup{}, request.Context()
				for index, path := range workers {
					if worker := Config.GetString(path, ""); worker != "" {
						waiter.Add(1)
						go func(index int, worker string) {
							result, start := map[string]any{"index": index, "id": "-", "duration": "-", "data": map[string]any{}}, time.Now()
							worker = strings.ReplaceAll(worker, "{{remote}}", remote)
							if request, err := http.NewRequestWithContext(ctx, http.MethodGet, worker, nil); err == nil {
								request.Header.Set("X-Forwarded-For", remote)
								if response, err := client.Do(request); err == nil {
									if response.StatusCode == http.StatusOK {
										if body, err := ioutil.ReadAll(response.Body); err == nil {
											if len(body) > 0 {
												var data []any

												if json.Unmarshal(body, &data) == nil {
													result["data"] = data[0]
													if value, ok := result["data"].(map[string]any)["id"].(string); ok {
														result["id"] = value
													}
												}
											}
										}
									}
									response.Body.Close()
								}
							}
							result["duration"] = fmt.Sprintf("%.3fs", float64(time.Now().Sub(start))/float64(time.Second))
							queue <- result
							waiter.Done()
						}(index, worker)
					}
				}
				waiter.Wait()

				result = make([]any, len(workers))
				durations := map[string]string{}
				for index := 0; index < len(workers); index++ {
					item := <-queue
					result[item["index"].(int)] = item["data"]
					durations[item["id"].(string)] = item["duration"].(string)
				}
				close(queue)

				geo := GeoLookup(remote)
				Logger.Info(map[string]any{
					"scope":     "http",
					"event":     "probe",
					"remote":    remote,
					"country":   strings.ToLower(jsonrpc.String(geo["country_code"])),
					"asnum":     jsonrpc.String(geo["as_number"]),
					"asname":    jsonrpc.String(geo["as_name"]),
					"durations": durations,
				})

			} else {
				result = append(result, map[string]any{
					"id":          Config.GetString(PROGNAME+".info.0", ""),
					"country":     Config.GetString(PROGNAME+".info.1", ""),
					"description": Config.GetString(PROGNAME+".info.2", ""),
					"probe":       Probe(remote, request.Context()),
				})
			}

			response.Header().Set("Content-Type", "application/json")
			if content, err := json.Marshal(result); err == nil {
				response.Write(content)
			}

		case "/":
			geo := GeoLookup(remote)
			geo["remote"] = remote
			htmlTemplate.Execute(response, map[string]any{
				"remote":   geo,
				"progname": PROGNAME,
				"version":  VERSION,
			})

		default:
			h.ServeHTTP(response, request)
		}
	})
}

func HTTPInit() {
	if data, err := ResourcesGet("index.tmpl"); err == nil {
		htmlTemplate.Funcs(template.FuncMap{
			"default": func(fallback any, input any) any {
				if input != nil && input != "" {
					return input
				}
				return fallback
			},
			"lower": func(input any) any {
				return strings.ToLower(fmt.Sprintf("%v", input))
			},
			"upper": func(input any) any {
				return strings.ToLower(fmt.Sprintf("%v", input))
			},
		}).Parse(string(data))
	}

	mux := http.NewServeMux()
	mux.Handle("/", httpHandler(http.StripPrefix("/", ResourcesHandler(6*time.Hour))))

	for _, path := range Config.GetPaths(PROGNAME + ".http.listen") {
		if key := Config.GetStringMatch(path, "_", `^\s*(\S+)?:\d+\s*((,[^,]+){2})?$`); key != "_" {
			parts := []string{}
			for _, value := range strings.Split(key, ",") {
				if value = strings.TrimSpace(value); value != "" {
					parts = append(parts, value)
				}
			}
			parts[0] = strings.TrimLeft(parts[0], "*")

			server := &http.Server{
				Addr:    parts[0],
				Handler: mux,
				// ErrorLog:     log.New(ioutil.Discard, "", 0),
				ReadTimeout:  uconfig.Duration(Config.GetDurationBounds(PROGNAME+".http.read_timeout", 10, 5, 30)),
				WriteTimeout: uconfig.Duration(Config.GetDurationBounds(PROGNAME+".http.write_timeout", 30, 5, 30)),
				IdleTimeout:  uconfig.Duration(Config.GetDurationBounds(PROGNAME+".http.idle_timeout", 30, 5, 30)),
			}
			if len(parts) == 3 {
				certificates := &dynacert.DYNACERT{}
				certificates.Add("*", parts[1], parts[2])
				server.TLSConfig = dynacert.IntermediateTLSConfig(certificates.GetCertificate)
				server.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){}
				go func(server *http.Server, parts []string) {
					Logger.Info(map[string]any{"scope": "http", "event": "listen", "listen": parts[0], "mode": "https", "certificates": parts[1:]})
					for {
						if listener, err := listener.NewTCPListener("tcp", parts[0], true, int(Config.GetSizeBounds(PROGNAME+".http.read_size", 0, 4<<10, 8<<20)),
							int(Config.GetSizeBounds(PROGNAME+".http.write_size", 0, 4<<10, 8<<20)), nil); err == nil {
							server.ServeTLS(listener, "", "")
							break
						}

						time.Sleep(time.Second)
					}
				}(server, parts)
			} else {
				go func(server *http.Server, parts []string) {
					Logger.Info(map[string]any{"scope": "http", "event": "listen", "listen": parts[0], "mode": "http"})
					for {
						if listener, err := listener.NewTCPListener("tcp", parts[0], true, int(Config.GetSizeBounds(PROGNAME+".http.read_size", 0, 4<<10, 4<<20)),
							int(Config.GetSizeBounds(PROGNAME+".http.write_size", 0, 4<<10, 4<<20)), nil); err == nil {
							server.Serve(listener)
							break
						}
						time.Sleep(time.Second)
					}
				}(server, parts)
			}
		}
	}
}

// Command goplay is a small control client for a running goplayd.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 10 * time.Second}

func main() {
	addr := flag.String("addr", envOr("GOPLAY_ADDR", "127.0.0.1:8420"), "daemon address")
	flag.Usage = usage
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	base := "http://" + strings.TrimRight(*addr, "/")
	if strings.HasPrefix(*addr, "http://") || strings.HasPrefix(*addr, "https://") {
		base = strings.TrimRight(*addr, "/")
	}
	if err := run(base, args); err != nil {
		fmt.Fprintln(os.Stderr, "goplay:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `GoPlay control client

usage: goplay [-addr host:port] <command> [args]

commands:
  health                       daemon health
  profiles                     list embedded game profiles
  list                         list instances
  create <id> [--name N] [--profile P] [--seed S] [--episodes N] [--no-start]
  start|pause|resume|stop <id>
  delete <id>
  show <id>                    one snapshot
  watch <id>                   poll snapshots every second
  say <id> <text...>           queue a message in the instruction buffer
  events <id> [n]              recent events
  metrics                      metrics snapshot
  pref <text...>               add a global soft preference
  prefs                        list global preferences
  recall <id> <query>          recall instance memory
`)
}

func run(base string, args []string) error {
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "health":
		return get(base+"/api/health", true)
	case "profiles":
		return get(base+"/api/profiles", true)
	case "list":
		return get(base+"/api/instances", true)
	case "metrics":
		return get(base+"/api/metrics", true)
	case "prefs":
		return get(base+"/api/memory/global", true)
	case "create":
		return create(base, rest)
	case "start", "pause", "resume", "stop":
		if len(rest) != 1 {
			return fmt.Errorf("%s needs an instance id", cmd)
		}
		return post(base+"/api/instances/"+rest[0]+"/"+cmd, nil, true)
	case "delete":
		if len(rest) != 1 {
			return fmt.Errorf("delete needs an instance id")
		}
		return del(base + "/api/instances/" + rest[0])
	case "show":
		if len(rest) != 1 {
			return fmt.Errorf("show needs an instance id")
		}
		return get(base+"/api/instances/"+rest[0], true)
	case "watch":
		if len(rest) != 1 {
			return fmt.Errorf("watch needs an instance id")
		}
		return watch(base, rest[0])
	case "say":
		if len(rest) < 2 {
			return fmt.Errorf("say needs an instance id and text")
		}
		body := map[string]string{"text": strings.Join(rest[1:], " ")}
		return post(base+"/api/instances/"+rest[0]+"/message", body, true)
	case "events":
		if len(rest) < 1 {
			return fmt.Errorf("events needs an instance id")
		}
		n := "40"
		if len(rest) > 1 {
			n = rest[1]
		}
		return get(base+"/api/instances/"+rest[0]+"/events?limit="+n, true)
	case "pref":
		if len(rest) == 0 {
			return fmt.Errorf("pref needs text")
		}
		body := map[string]any{"text": strings.Join(rest, " "), "source": "cli"}
		return post(base+"/api/memory/global/preferences", body, true)
	case "recall":
		if len(rest) < 2 {
			return fmt.Errorf("recall needs an instance id and query")
		}
		return get(base+"/api/instances/"+rest[0]+"/memory?q="+rest[1], true)
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func create(base string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("create needs an instance id")
	}
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "display name")
	profile := fs.String("profile", "mock_dungeon", "Lua profile id")
	seed := fs.Int64("seed", 7, "random seed")
	episodes := fs.Int("episodes", 10, "episode limit")
	noStart := fs.Bool("no-start", false, "do not autostart")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	body := map[string]any{
		"id": args[0], "name": *name, "profile_id": *profile,
		"seed": *seed, "max_episodes": *episodes, "auto_start": !*noStart,
	}
	return post(base+"/api/instances", body, true)
}

func get(url string, pretty bool) error {
	res, err := httpClient.Get(url)
	if err != nil {
		return err
	}
	return output(res, pretty)
}

func post(url string, body any, pretty bool) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	res, err := httpClient.Post(url, "application/json", r)
	if err != nil {
		return err
	}
	return output(res, pretty)
}

func del(url string) error {
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	return output(res, true)
}

func output(res *http.Response, pretty bool) error {
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(b)))
	}
	if !pretty {
		_, err = os.Stdout.Write(b)
		return err
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		_, err = os.Stdout.Write(b)
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func watch(base, id string) error {
	last := ""
	for {
		res, err := httpClient.Get(base + "/api/instances/" + id)
		if err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if err != nil {
			return err
		}
		var v struct {
			Status string `json:"status"`
			Stats  struct {
				Decisions  int64   `json:"decisions"`
				Actions    int64   `json:"actions"`
				Reward     float64 `json:"reward"`
				Progress   float64 `json:"progress"`
				LastAction string  `json:"last_action"`
			} `json:"stats"`
		}
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		line := fmt.Sprintf("%-14s decisions=%-5d actions=%-5d progress=%3.0f%% reward=%7.1f  %s",
			v.Status, v.Stats.Decisions, v.Stats.Actions, v.Stats.Progress*100, v.Stats.Reward, v.Stats.LastAction)
		if line != last {
			fmt.Println(line)
			last = line
		}
		if v.Status == "stopped" || v.Status == "error" {
			return nil
		}
		time.Sleep(time.Second)
	}
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

// Command socketbench measures decision latency over RateGuard's Unix socket
// and over loopback TCP, with the same Go client, at the same fixed request
// rate. k6 cannot dial a Unix socket, so this is how the sidecar's socket is
// measured (ADR 0020). Run it inside RateGuard's network namespace, with the
// socket's directory mounted, so both targets are local:
//
//	go run ./test/load/socketbench -socket /run/rateguard/rateguard.sock -tcp http://127.0.0.1:8080
//
// RG_ADMIN_TOKEN must be set: it creates a policy and clients through the
// admin API before measuring.
//
// Like k6's constant-arrival-rate executor, requests start on a fixed
// schedule whatever the latency, so a slow response cannot hide the ones
// queued behind it. A request is timed from when it starts to when its body
// has been read. It uses only the standard library.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

type config struct {
	socket   string
	tcp      string
	rate     int
	duration time.Duration
	warmup   time.Duration
	clients  int
	workers  int
	rounds   int
}

func main() {
	var c config
	flag.StringVar(&c.socket, "socket", "/run/rateguard/rateguard.sock", "RateGuard's Unix socket")
	flag.StringVar(&c.tcp, "tcp", "http://127.0.0.1:8080", "RateGuard over loopback TCP")
	flag.IntVar(&c.rate, "rate", 1000, "requests per second")
	flag.DurationVar(&c.duration, "duration", 60*time.Second, "measured time per target")
	flag.DurationVar(&c.warmup, "warmup", 10*time.Second, "unmeasured time per target before the rounds")
	flag.IntVar(&c.clients, "clients", 100, "API keys to spread requests over")
	flag.IntVar(&c.workers, "workers", 200, "requests that may be in flight at once")
	flag.IntVar(&c.rounds, "rounds", 4, "rounds the measured time is split into, alternating which target goes first")
	flag.Parse()

	if err := run(c); err != nil {
		fmt.Fprintln(os.Stderr, "socketbench:", err)
		os.Exit(1)
	}
}

func run(c config) error {
	token := os.Getenv("RG_ADMIN_TOKEN")
	if token == "" {
		return errors.New("RG_ADMIN_TOKEN must be set")
	}
	if c.rate < 1 || c.workers < 1 || c.clients < 1 || c.rounds < 1 || c.duration <= 0 {
		return errors.New("rate, workers, clients, rounds and duration must be positive")
	}
	ctx := context.Background()

	tcp := newClient(c.workers, nil)
	unix := newClient(c.workers, func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", c.socket)
	})

	keys, err := setUp(ctx, tcp, c.tcp, token, c.clients)
	if err != nil {
		return err
	}
	fmt.Printf("socketbench: %d clients, %d requests/s, %s warm-up and %s measured per target, in %d alternating rounds\n\n",
		c.clients, c.rate, c.warmup, c.duration, c.rounds)

	targets := []target{
		// The host part of a socket URL is ignored by the dialer.
		{name: "unix socket", client: unix, base: "http://rateguard"},
		{name: "loopback tcp", client: tcp, base: c.tcp},
	}
	for _, t := range targets {
		if c.warmup > 0 {
			measure(ctx, t.client, t.base, keys, c.rate, c.warmup, c.workers)
		}
	}
	// Whichever target goes second runs on a warmer machine, so the order
	// flips every round and each target's rounds are pooled.
	results := make([]result, len(targets))
	for round := range c.rounds {
		for i := range targets {
			if round%2 == 1 {
				i = len(targets) - 1 - i
			}
			t := targets[i]
			results[i].add(measure(ctx, t.client, t.base, keys, c.rate, c.duration/time.Duration(c.rounds), c.workers))
		}
	}
	for i, t := range targets {
		results[i].print(t.name)
	}
	return nil
}

type target struct {
	name   string
	client *http.Client
	base   string
}

// newClient returns a keep-alive client with room for workers idle
// connections, dialling with dial when it is set.
func newClient(workers int, dial func(ctx context.Context, network, addr string) (net.Conn, error)) *http.Client {
	tr := &http.Transport{
		MaxIdleConns:        workers,
		MaxIdleConnsPerHost: workers,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
	}
	if dial != nil {
		tr.DialContext = dial
	}
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}
}

// setUp creates a policy that never runs dry and n clients on it, and
// returns their API keys.
func setUp(ctx context.Context, c *http.Client, base, token string, n int) ([]string, error) {
	stamp := time.Now().UnixNano()
	var policy struct {
		ID string `json:"id"`
	}
	body := fmt.Sprintf(`{"name":"Socketbench %d","capacity":1000000000,"window_seconds":60}`, stamp)
	if err := admin(ctx, c, http.MethodPost, base+"/v1/policies", token, body, http.StatusCreated, &policy); err != nil {
		return nil, fmt.Errorf("creating the policy: %w", err)
	}
	keys := make([]string, 0, n)
	for i := range n {
		var reg struct {
			Client struct {
				ID string `json:"id"`
			} `json:"client"`
			APIKey struct {
				Key string `json:"key"`
			} `json:"api_key"`
		}
		body := fmt.Sprintf(`{"name":"Socketbench %d %d"}`, stamp, i)
		if err := admin(ctx, c, http.MethodPost, base+"/v1/clients", token, body, http.StatusCreated, &reg); err != nil {
			return nil, fmt.Errorf("registering client %d: %w", i, err)
		}
		assign := fmt.Sprintf(`{"policy_id":%q}`, policy.ID)
		if err := admin(ctx, c, http.MethodPut, base+"/v1/clients/"+reg.Client.ID+"/policy", token, assign, http.StatusOK, nil); err != nil {
			return nil, fmt.Errorf("assigning the policy to client %d: %w", i, err)
		}
		keys = append(keys, reg.APIKey.Key)
	}
	return keys, nil
}

func admin(ctx context.Context, c *http.Client, method, url, token, body string, want int, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != want {
		return fmt.Errorf("%s %s: %d %s", method, url, resp.StatusCode, b)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

const checkBody = `{"method":"GET","path":"/api/products"}`

// result is what one measurement saw.
type result struct {
	latencies []time.Duration
	failures  int
	dropped   int
	elapsed   time.Duration
}

// measure sends rate requests a second for duration, taking the keys in turn
// and starting each request on schedule. A request that finds every worker
// busy is dropped and counted, as k6 counts a dropped iteration.
func measure(ctx context.Context, c *http.Client, base string, keys []string, rate int, duration time.Duration, workers int) result {
	var (
		mu  sync.Mutex
		res result
		wg  sync.WaitGroup
	)
	jobs := make(chan string)
	for range workers {
		wg.Go(func() {
			for key := range jobs {
				d, err := decide(ctx, c, base, key)
				mu.Lock()
				if err != nil {
					res.failures++
				} else {
					res.latencies = append(res.latencies, d)
				}
				mu.Unlock()
			}
		})
	}

	interval := time.Second / time.Duration(rate)
	total := int(duration / interval)
	start := time.Now()
	for i := range total {
		if wait := time.Until(start.Add(time.Duration(i) * interval)); wait > 0 {
			time.Sleep(wait)
		}
		select {
		case jobs <- keys[i%len(keys)]:
		default:
			mu.Lock()
			res.dropped++
			mu.Unlock()
		}
	}
	close(jobs)
	wg.Wait()
	res.elapsed = time.Since(start)
	return res
}

// decide sends one check and returns how long it took to the end of the
// body. Anything but a 200 is a failure: the policy never refuses.
func decide(ctx context.Context, c *http.Client, base, key string) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/check", strings.NewReader(checkBody))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	_, err = io.Copy(io.Discard, resp.Body)
	d := time.Since(start)
	if cerr := resp.Body.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("status %d", resp.StatusCode)
	}
	return d, nil
}

// add pools another measurement of the same target into r.
func (r *result) add(o result) {
	r.latencies = append(r.latencies, o.latencies...)
	r.failures += o.failures
	r.dropped += o.dropped
	r.elapsed += o.elapsed
}

func (r result) print(name string) {
	slices.Sort(r.latencies)
	n := len(r.latencies)
	fmt.Printf("== %s\n", name)
	fmt.Printf("requests        %d (%.1f/s)\n", n, float64(n)/r.elapsed.Seconds())
	fmt.Printf("failed          %d\n", r.failures)
	fmt.Printf("dropped         %d\n", r.dropped)
	if n == 0 {
		fmt.Println()
		return
	}
	var sum time.Duration
	for _, d := range r.latencies {
		sum += d
	}
	fmt.Printf("latency ms      p50 %s  p90 %s  p95 %s  p99 %s  max %s  avg %s\n\n",
		ms(quantile(r.latencies, 0.50)), ms(quantile(r.latencies, 0.90)), ms(quantile(r.latencies, 0.95)),
		ms(quantile(r.latencies, 0.99)), ms(r.latencies[n-1]), ms(sum/time.Duration(n)))
}

// quantile returns the q-quantile of sorted by the nearest-rank method.
func quantile(sorted []time.Duration, q float64) time.Duration {
	i := int(math.Ceil(q*float64(len(sorted)))) - 1
	return sorted[max(0, min(i, len(sorted)-1))]
}

func ms(d time.Duration) string {
	return fmt.Sprintf("%.3f", float64(d)/float64(time.Millisecond))
}

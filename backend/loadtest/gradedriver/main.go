// Command gradedriver load-tests the submission-grading path (POST
// /api/v1/quiz/submit) against a running backend seeded by gradeseed. It runs a
// CLOSED-LOOP concurrency sweep: for each concurrency level it keeps N workers
// busy submitting for a fixed duration, then reports throughput and latency
// percentiles. Because exam DML is serialised by the backend's single-worker
// queue, DML p95 climbs with concurrency while SELECT (lock-free) stays flat —
// the "knee" is where DML latency takes off. That is the number that sizes the
// server.
//
// Usage:
//
//	go run ./loadtest/gradedriver -manifest loadtest/grade_manifest.json \
//	    -c 1,2,4,8,16,32,64 -d 8s -mix both
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Manifest mirrors gradeseed's output.
type Manifest struct {
	Base         string `json:"base"`
	ExamID       int    `json:"exam_id"`
	SelectQID    int    `json:"select_qid"`
	DMLQID       int    `json:"dml_qid"`
	StudentCount int    `json:"student_count"`
	StudentPre   string `json:"student_prefix"`
	Password     string `json:"password"`
	SelectSQL    string `json:"select_sql"`
	DMLSQL       string `json:"dml_sql"`
}

func main() {
	manifestPath := flag.String("manifest", "loadtest/grade_manifest.json", "path to gradeseed manifest")
	cList := flag.String("c", "1,2,4,8,16,32,64", "comma-separated concurrency levels to sweep")
	dur := flag.Duration("d", 8*time.Second, "duration to hold each concurrency level")
	mix := flag.String("mix", "both", "which workload to drive: select | dml | both")
	warmup := flag.Duration("warmup", 1*time.Second, "warmup discarded before each level's measurement")
	flag.Parse()

	raw, err := os.ReadFile(*manifestPath)
	if err != nil {
		log.Fatalf("read manifest %s: %v (run gradeseed first)", *manifestPath, err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		log.Fatalf("parse manifest: %v", err)
	}

	levels, err := parseLevels(*cList)
	if err != nil {
		log.Fatalf("bad -c: %v", err)
	}
	maxC := levels[len(levels)-1]

	hc := &http.Client{Timeout: 60 * time.Second}
	d := &driver{base: m.Base, hc: hc}

	// Log in enough students to cover the highest concurrency (one token per
	// worker avoids a single account's rows dominating; reuse is fine when
	// c > students).
	nTokens := m.StudentCount
	if nTokens > maxC {
		nTokens = maxC
	}
	if nTokens < 1 {
		nTokens = 1
	}
	log.Printf("logging in %d students...", nTokens)
	tokens := d.loginStudents(m, nTokens)
	if len(tokens) == 0 {
		log.Fatal("no students logged in; check the manifest/backend")
	}
	log.Printf("logged in %d/%d students", len(tokens), nTokens)
	d.tokens = tokens

	workloads := map[string]struct {
		qid int
		sql string
	}{}
	if *mix == "select" || *mix == "both" {
		workloads["SELECT"] = struct {
			qid int
			sql string
		}{m.SelectQID, m.SelectSQL}
	}
	if *mix == "dml" || *mix == "both" {
		workloads["DML"] = struct {
			qid int
			sql string
		}{m.DMLQID, m.DMLSQL}
	}

	for _, name := range []string{"SELECT", "DML"} {
		w, ok := workloads[name]
		if !ok {
			continue
		}
		fmt.Printf("\n===== %s workload (question %d) =====\n", name, w.qid)
		fmt.Printf("%-6s %10s %8s %8s %8s %8s %8s %8s\n",
			"conc", "req/s", "p50", "p95", "p99", "max", "ok", "err")
		for _, c := range levels {
			r := d.runLevel(c, *dur, *warmup, w.qid, w.sql)
			fmt.Printf("%-6d %10.1f %8s %8s %8s %8s %8d %8d\n",
				c, r.throughput, ms(r.p50), ms(r.p95), ms(r.p99), ms(r.max), r.ok, r.err)
		}
	}
	fmt.Printf("\nRead the knee: the concurrency where p95 stops tracking p50 and climbs.\n")
}

type driver struct {
	base   string
	hc     *http.Client
	tokens []string
}

type levelResult struct {
	throughput         float64
	p50, p95, p99, max time.Duration
	ok, err            int
}

// runLevel holds `c` workers busy submitting for `dur` (after a discarded
// warmup) and returns aggregate stats for the measured window.
func (d *driver) runLevel(c int, dur, warmup time.Duration, qid int, sql string) levelResult {
	var okCount, errCount int64
	var mu sync.Mutex
	lat := make([]time.Duration, 0, 4096)

	stop := make(chan struct{}) // closed when the measured window ends
	var recording atomic.Bool

	var wg sync.WaitGroup
	for i := 0; i < c; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			token := d.tokens[worker%len(d.tokens)]
			for {
				select {
				case <-stop:
					return
				default:
				}
				start := time.Now()
				err := d.submit(token, qid, sql)
				elapsed := time.Since(start)
				if !recording.Load() {
					continue // warmup — discard
				}
				if err != nil {
					atomic.AddInt64(&errCount, 1)
				} else {
					atomic.AddInt64(&okCount, 1)
					mu.Lock()
					lat = append(lat, elapsed)
					mu.Unlock()
				}
			}
		}(i)
	}

	time.Sleep(warmup)
	recording.Store(true)
	measureStart := time.Now()
	time.Sleep(dur)
	window := time.Since(measureStart)
	close(stop)
	wg.Wait()

	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	res := levelResult{
		ok:  int(okCount),
		err: int(errCount),
	}
	if window > 0 {
		res.throughput = float64(okCount) / window.Seconds()
	}
	if len(lat) > 0 {
		res.p50 = pct(lat, 0.50)
		res.p95 = pct(lat, 0.95)
		res.p99 = pct(lat, 0.99)
		res.max = lat[len(lat)-1]
	}
	return res
}

func (d *driver) submit(token string, qid int, sql string) error {
	body, _ := json.Marshal(map[string]any{"question_number": qid, "sql_text": sql})
	req, err := http.NewRequest(http.MethodPost, d.base+"/api/v1/quiz/submit", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := d.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("submit -> %d: %s", resp.StatusCode, bytes.TrimSpace(data))
	}
	return nil
}

func (d *driver) loginStudents(m Manifest, n int) []string {
	tokens := make([]string, n)
	sem := make(chan struct{}, 16)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			user := fmt.Sprintf("%s%04d", m.StudentPre, idx+1)
			var out struct {
				Token string `json:"token"`
			}
			body, _ := json.Marshal(map[string]string{"username": user, "password": m.Password})
			resp, err := d.hc.Post(d.base+"/api/v1/login", "application/json", bytes.NewReader(body))
			if err != nil {
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return
			}
			_ = json.NewDecoder(resp.Body).Decode(&out)
			tokens[idx] = out.Token
		}(i)
	}
	wg.Wait()
	// Compact out any failed logins.
	compact := tokens[:0]
	for _, t := range tokens {
		if t != "" {
			compact = append(compact, t)
		}
	}
	return compact
}

func parseLevels(s string) ([]int, error) {
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := strconv.Atoi(p)
		if err != nil || v < 1 {
			return nil, fmt.Errorf("invalid level %q", p)
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no levels")
	}
	sort.Ints(out)
	return out, nil
}

// pct returns the p-quantile (0..1) of a sorted slice via nearest-rank.
func pct(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	return sorted[idx]
}

func ms(d time.Duration) string { return fmt.Sprintf("%dms", d.Milliseconds()) }

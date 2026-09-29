// Command queuedriver fires a FIXED number of exam DML submissions (POST
// /api/v1/quiz/submit) at a running backend seeded by gradeseed, and checks
// every grade. Where gradedriver sweeps concurrency for a fixed duration to find
// the latency knee, this answers "does the single-worker SubmissionQueue get N
// submissions through correctly?".
//
// Every k-th submission is a deliberately WRONG answer. The seeded DML answer is
// state-dependent (hours = hours + 10), so if the savepoint rollback ever leaked
// one submission's writes into the next, later correct answers would grade
// wrong — the mismatch counter doubles as an isolation check.
//
// With -retry, a 503 (queue full / schema busy) is retried with jittered backoff
// the way a real client would, so every submission is eventually graded; without
// it, 503s are counted as shed load.
//
// Usage:
//
//	go run ./loadtest/queuedriver -manifest loadtest/grade_manifest.json -n 6000 -c 150
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Manifest mirrors gradeseed's output (only the fields used here).
type Manifest struct {
	Base         string `json:"base"`
	ExamID       string `json:"exam_id"`
	DMLQID       int    `json:"dml_qid"`
	StudentCount int    `json:"student_count"`
	StudentPre   string `json:"student_prefix"`
	Password     string `json:"password"`
	DMLSQL       string `json:"dml_sql"`
}

type outcome struct {
	status   int // 0 = transport error
	latency  time.Duration
	attempts int
	correct  bool // grade returned by the backend (200 only)
	wantOK   bool // what the grade should have been
	errText  string
}

func main() {
	manifestPath := flag.String("manifest", "loadtest/grade_manifest.json", "path to gradeseed manifest")
	n := flag.Int("n", 6000, "total DML submissions")
	c := flag.Int("c", 150, "concurrent in-flight submissions (one student token per worker)")
	wrongEvery := flag.Int("wrong-every", 4, "every k-th submission is a wrong answer (0 = all correct)")
	wrongSQL := flag.String("wrong-sql", "UPDATE works_on SET hours = hours + 5", "the deliberately wrong answer")
	retry := flag.Bool("retry", false, "retry 503s with jittered backoff until graded")
	maxAttempts := flag.Int("max-attempts", 50, "per-submission attempt cap when -retry is on")
	flag.Parse()

	raw, err := os.ReadFile(*manifestPath)
	if err != nil {
		log.Fatalf("read manifest %s: %v (run gradeseed first)", *manifestPath, err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		log.Fatalf("parse manifest: %v", err)
	}

	hc := &http.Client{
		Timeout: 90 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        *c + 16,
			MaxIdleConnsPerHost: *c + 16,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	nTokens := min(m.StudentCount, *c)
	tokens := login(hc, m, nTokens)
	if len(tokens) == 0 {
		log.Fatal("no students logged in; check the manifest/backend")
	}
	log.Printf("logged in %d/%d students; firing %d DML submissions at c=%d (wrong every %d, retry=%v)",
		len(tokens), nTokens, *n, *c, *wrongEvery, *retry)

	results := make([]outcome, *n)
	var next atomic.Int64
	var done atomic.Int64

	stopProgress := make(chan struct{})
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stopProgress:
				return
			case <-t.C:
				log.Printf("progress: %d/%d", done.Load(), *n)
			}
		}
	}()

	start := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < *c; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			token := tokens[worker%len(tokens)]
			for {
				i := int(next.Add(1) - 1)
				if i >= *n {
					return
				}
				wrong := *wrongEvery > 0 && i%*wrongEvery == *wrongEvery-1
				sql := m.DMLSQL
				if wrong {
					sql = *wrongSQL
				}
				o := outcome{wantOK: !wrong}
				t0 := time.Now()
				for {
					o.attempts++
					o.status, o.correct, o.errText = submit(hc, m.Base, token, m.DMLQID, sql)
					if !*retry || o.status != http.StatusServiceUnavailable || o.attempts >= *maxAttempts {
						break
					}
					backoff := time.Duration(50*o.attempts) * time.Millisecond
					time.Sleep(min(backoff, 2*time.Second) + rand.N(100*time.Millisecond))
				}
				o.latency = time.Since(t0)
				results[i] = o
				done.Add(1)
			}
		}(w)
	}
	wg.Wait()
	wall := time.Since(start)
	close(stopProgress)

	report(results, wall)
}

func report(results []outcome, wall time.Duration) {
	statuses := map[int]int{}
	errSamples := map[int]string{}
	var lat []time.Duration
	var graded, mismatch, wrongGraded, retried, totalAttempts int
	for _, o := range results {
		statuses[o.status]++
		totalAttempts += o.attempts
		if o.attempts > 1 {
			retried++
		}
		if o.status != http.StatusOK {
			if _, ok := errSamples[o.status]; !ok {
				errSamples[o.status] = o.errText
			}
			continue
		}
		graded++
		lat = append(lat, o.latency)
		if o.correct != o.wantOK {
			mismatch++
		}
		if !o.wantOK {
			wrongGraded++
		}
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })

	fmt.Printf("\n===== DML queue load: %d submissions =====\n", len(results))
	fmt.Printf("wall time        %s\n", wall.Round(time.Millisecond))
	fmt.Printf("graded (200)     %d  (%.1f graded/s)\n", graded, float64(graded)/wall.Seconds())
	fmt.Printf("HTTP attempts    %d  (%d submissions needed >1 attempt)\n", totalAttempts, retried)
	fmt.Printf("status mix      ")
	codes := make([]int, 0, len(statuses))
	for k := range statuses {
		codes = append(codes, k)
	}
	sort.Ints(codes)
	for _, k := range codes {
		fmt.Printf(" %d×%d", k, statuses[k])
	}
	fmt.Println()
	if len(lat) > 0 {
		fmt.Printf("latency (200s)   p50=%s p90=%s p95=%s p99=%s max=%s\n",
			ms(pct(lat, .50)), ms(pct(lat, .90)), ms(pct(lat, .95)), ms(pct(lat, .99)), ms(lat[len(lat)-1]))
	}
	fmt.Printf("grade check      %d mismatches out of %d graded (%d wrong answers correctly caught: %d)\n",
		mismatch, graded, wrongGraded, wrongGraded-countWrongMismatch(results))
	for _, k := range codes {
		if s, ok := errSamples[k]; ok && k != http.StatusOK {
			fmt.Printf("sample %d body   %s\n", k, truncate(s, 200))
		}
	}
	if mismatch > 0 {
		fmt.Println("FAIL: grades disagreed with the expected outcome — possible state leak between submissions")
		os.Exit(1)
	}
}

func countWrongMismatch(results []outcome) int {
	n := 0
	for _, o := range results {
		if o.status == http.StatusOK && !o.wantOK && o.correct {
			n++
		}
	}
	return n
}

func submit(hc *http.Client, base, token string, qid int, sql string) (status int, correct bool, errText string) {
	body, _ := json.Marshal(map[string]any{"question_number": qid, "sql_text": sql})
	req, err := http.NewRequest(http.MethodPost, base+"/api/v1/quiz/submit", bytes.NewReader(body))
	if err != nil {
		return 0, false, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := hc.Do(req)
	if err != nil {
		return 0, false, err.Error()
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, false, string(bytes.TrimSpace(data))
	}
	var out struct {
		IsCorrect bool `json:"is_correct"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return resp.StatusCode, false, "bad json: " + err.Error()
	}
	return resp.StatusCode, out.IsCorrect, ""
}

func login(hc *http.Client, m Manifest, n int) []string {
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
			body, _ := json.Marshal(map[string]string{"username": user, "password": m.Password})
			resp, err := hc.Post(m.Base+"/api/v1/login", "application/json", bytes.NewReader(body))
			if err != nil {
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return
			}
			var out struct {
				Token string `json:"token"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&out)
			tokens[idx] = out.Token
		}(i)
	}
	wg.Wait()
	compact := tokens[:0]
	for _, t := range tokens {
		if t != "" {
			compact = append(compact, t)
		}
	}
	return compact
}

func pct(sorted []time.Duration, p float64) time.Duration {
	return sorted[int(p*float64(len(sorted)-1))]
}

func ms(d time.Duration) string { return fmt.Sprintf("%dms", d.Milliseconds()) }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

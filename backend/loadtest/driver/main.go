// Command driver load-tests the email-authentication flow end to end against a
// running backend, using Mailpit as the SMTP sink to recover the OTP that the
// API never returns over HTTP.
//
// Per sample (account loadtestNNNN), one worker runs:
//
//	1. POST /api/v1/forgot-password            -> generic 200 (no code in body)
//	2. poll Mailpit for the "reset code" email -> extract the 6-digit OTP
//	3. POST /api/v1/forgot-password/verify     -> 200, regenerates + emails new pw
//	4. poll Mailpit for the "credentials" email-> extract the new password
//	5. POST /api/v1/login with the new password-> 200 + token
//
// Concurrency is a bounded worker pool (-c). Each account is hit exactly once,
// so the 5-minute per-account throttle never fires within a run; RE-running
// within 5 min against the same seed WILL be throttled (no new emails) — reseed a
// fresh range, wait, or clear last_forgot_reset_at first.
//
// Usage:
//
//	go run ./loadtest/driver -base http://localhost:8080 -mailpit http://localhost:8025 -n 150 -c 20
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"sync"
	"time"
)

// Kept in sync with loadtest/seed: names and emails are derived the same way.
const (
	usernamePrefix = "loadtest"
	emailDomain    = "example.com"
)

// Subjects set by the backend's SMTPEmailSender (email_smtp.go).
const (
	subjectResetCode   = "Your password reset code"
	subjectCredentials = "Your SQL Quiz System credentials"
)

var (
	otpRe      = regexp.MustCompile(`\b(\d{6})\b`)
	passwordRe = regexp.MustCompile(`(?m)^Password:\s*(\S+)`)
)

// stepTimings holds one sample's per-step wall-clock durations. A zero duration
// means the step was never reached (an earlier step failed).
type stepTimings struct {
	forgot     time.Duration // HTTP time of step 1
	otpArrival time.Duration // forgot-200 -> reset email visible in Mailpit
	verify     time.Duration // HTTP time of step 3
	pwArrival  time.Duration // verify-200 -> credentials email visible
	login      time.Duration // HTTP time of step 5
	total      time.Duration // whole chain
	failedAt   string        // "" on full success, else the failing step name
}

func main() {
	base := flag.String("base", "http://localhost:8080", "backend base URL")
	mailpit := flag.String("mailpit", "http://localhost:8025", "Mailpit base URL (web/API)")
	n := flag.Int("n", 150, "number of samples")
	concurrency := flag.Int("c", 20, "max concurrent workers")
	mailTimeout := flag.Duration("mail-timeout", 20*time.Second, "per-email polling timeout")
	pollEvery := flag.Duration("poll", 200*time.Millisecond, "Mailpit poll interval")
	purge := flag.Bool("purge", true, "delete all Mailpit messages before the run")
	flag.Parse()

	client := &http.Client{Timeout: 30 * time.Second}
	d := &driver{
		client:      client,
		base:        *base,
		mailpit:     *mailpit,
		mailTimeout: *mailTimeout,
		pollEvery:   *pollEvery,
	}

	if *purge {
		if err := d.purgeMailpit(); err != nil {
			log.Fatalf("purge mailpit: %v (is Mailpit up at %s?)", err, *mailpit)
		}
		log.Printf("purged Mailpit mailbox")
	}

	log.Printf("driving %d samples at concurrency %d against %s", *n, *concurrency, *base)
	results := make([]stepTimings, *n)
	sem := make(chan struct{}, *concurrency)
	var wg sync.WaitGroup

	runStart := time.Now()
	for i := 1; i <= *n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			results[idx-1] = d.runOne(idx)
		}(i)
	}
	wg.Wait()
	wall := time.Since(runStart)

	report(results, wall)
}

type driver struct {
	client      *http.Client
	base        string
	mailpit     string
	mailTimeout time.Duration
	pollEvery   time.Duration
}

// runOne executes the full five-step chain for account loadtestNNNN and returns
// its timings. It stops at the first failing step, recording failedAt.
func (d *driver) runOne(idx int) stepTimings {
	var t stepTimings
	username := fmt.Sprintf("%s%04d", usernamePrefix, idx)
	email := fmt.Sprintf("%s%04d@%s", usernamePrefix, idx, emailDomain)
	chainStart := time.Now()

	// Step 1: forgot-password (always 200).
	start := time.Now()
	if err := d.postJSON("/api/v1/forgot-password", map[string]string{"username": username}, nil); err != nil {
		t.failedAt = "forgot"
		return t
	}
	t.forgot = time.Since(start)

	// Step 2: recover OTP from the reset-code email.
	otpStart := time.Now()
	body, err := d.waitForEmail(email, subjectResetCode)
	if err != nil {
		t.failedAt = "otp_email"
		return t
	}
	t.otpArrival = time.Since(otpStart)
	m := otpRe.FindStringSubmatch(body)
	if m == nil {
		t.failedAt = "otp_parse"
		return t
	}
	otp := m[1]

	// Step 3: verify — regenerates the password and emails new credentials.
	start = time.Now()
	if err := d.postJSON("/api/v1/forgot-password/verify",
		map[string]string{"username": username, "otp": otp}, nil); err != nil {
		t.failedAt = "verify"
		return t
	}
	t.verify = time.Since(start)

	// Step 4: recover the new password from the credentials email.
	pwStart := time.Now()
	credBody, err := d.waitForEmail(email, subjectCredentials)
	if err != nil {
		t.failedAt = "cred_email"
		return t
	}
	t.pwArrival = time.Since(pwStart)
	pm := passwordRe.FindStringSubmatch(credBody)
	if pm == nil {
		t.failedAt = "pw_parse"
		return t
	}
	newPassword := pm[1]

	// Step 5: log in with the regenerated password.
	start = time.Now()
	if err := d.postJSON("/api/v1/login",
		map[string]string{"username": username, "password": newPassword}, nil); err != nil {
		t.failedAt = "login"
		return t
	}
	t.login = time.Since(start)

	t.total = time.Since(chainStart)
	return t
}

// postJSON POSTs body as JSON to base+path and requires a 2xx. If out is
// non-nil the response body is decoded into it.
func (d *driver) postJSON(path string, body any, out any) error {
	buf, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, d.base+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s -> %d: %s", path, resp.StatusCode, bytes.TrimSpace(data))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// mailpitMessage is the subset of a Mailpit message summary we read.
type mailpitMessage struct {
	ID      string `json:"ID"`
	Subject string `json:"Subject"`
}

type mailpitSearch struct {
	Messages []mailpitMessage `json:"messages"`
}

type mailpitMessageFull struct {
	Text string `json:"Text"`
}

// waitForEmail polls Mailpit until a message addressed to `to` with the given
// subject appears (or the timeout elapses), then returns its plain-text body.
func (d *driver) waitForEmail(to, subject string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d.mailTimeout)
	defer cancel()
	for {
		id, err := d.findMessage(to, subject)
		if err == nil && id != "" {
			return d.fetchMessageText(id)
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("timeout waiting for %q to %s", subject, to)
		case <-time.After(d.pollEvery):
		}
	}
}

// findMessage returns the ID of the newest message to `to` whose subject matches,
// or "" if none yet. Mailpit's search matches the address; we filter subject
// client-side so the reset-code and credentials mails to the same recipient are
// disambiguated.
func (d *driver) findMessage(to, subject string) (string, error) {
	u := fmt.Sprintf("%s/api/v1/search?query=to:%s", d.mailpit, to)
	resp, err := d.client.Get(u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mailpit search -> %d", resp.StatusCode)
	}
	var s mailpitSearch
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return "", err
	}
	for _, m := range s.Messages { // newest first per Mailpit
		if m.Subject == subject {
			return m.ID, nil
		}
	}
	return "", nil
}

func (d *driver) fetchMessageText(id string) (string, error) {
	resp, err := d.client.Get(fmt.Sprintf("%s/api/v1/message/%s", d.mailpit, id))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var full mailpitMessageFull
	if err := json.NewDecoder(resp.Body).Decode(&full); err != nil {
		return "", err
	}
	return full.Text, nil
}

func (d *driver) purgeMailpit() error {
	req, _ := http.NewRequest(http.MethodDelete, d.mailpit+"/api/v1/messages", nil)
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("delete messages -> %d", resp.StatusCode)
	}
	return nil
}

// report prints success/failure counts and latency percentiles per step.
func report(results []stepTimings, wall time.Duration) {
	total := len(results)
	success := 0
	failByStep := map[string]int{}
	var forgot, otp, verify, pw, login, chain []time.Duration

	for _, r := range results {
		if r.failedAt == "" {
			success++
			forgot = append(forgot, r.forgot)
			otp = append(otp, r.otpArrival)
			verify = append(verify, r.verify)
			pw = append(pw, r.pwArrival)
			login = append(login, r.login)
			chain = append(chain, r.total)
		} else {
			failByStep[r.failedAt]++
		}
	}

	fmt.Printf("\n===== email-auth load test =====\n")
	fmt.Printf("samples:      %d\n", total)
	fmt.Printf("succeeded:    %d\n", success)
	fmt.Printf("failed:       %d\n", total-success)
	if len(failByStep) > 0 {
		fmt.Printf("failures by step:\n")
		for step, c := range failByStep {
			fmt.Printf("  %-12s %d\n", step, c)
		}
	}
	fmt.Printf("wall clock:   %s\n", wall.Round(time.Millisecond))
	if wall > 0 {
		fmt.Printf("throughput:   %.1f completed samples/sec\n", float64(success)/wall.Seconds())
	}

	fmt.Printf("\nlatency (successful samples only)\n")
	fmt.Printf("%-14s %8s %8s %8s %8s %8s\n", "step", "p50", "p95", "p99", "min", "max")
	printPct("forgot(HTTP)", forgot)
	printPct("otp-arrival", otp) // the true async-SMTP signal for the reset mail
	printPct("verify(HTTP)", verify)
	printPct("pw-arrival", pw) // async-SMTP signal for the credentials mail
	printPct("login(HTTP)", login)
	printPct("chain(total)", chain)
}

func printPct(name string, ds []time.Duration) {
	if len(ds) == 0 {
		fmt.Printf("%-14s %8s\n", name, "-")
		return
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	fmt.Printf("%-14s %8s %8s %8s %8s %8s\n", name,
		ms(pct(ds, 0.50)), ms(pct(ds, 0.95)), ms(pct(ds, 0.99)),
		ms(ds[0]), ms(ds[len(ds)-1]))
}

// pct returns the p-quantile (0..1) of a sorted slice via nearest-rank.
func pct(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	return sorted[idx]
}

func ms(d time.Duration) string {
	return fmt.Sprintf("%dms", d.Milliseconds())
}

// Command newapi-stress runs a bounded, closed-loop streaming capacity test
// against an OpenAI-compatible Chat Completions endpoint.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	defaultTarget          = "https://newapi.withcortex.ai/v1"
	defaultModel           = "fake-agent-medium-v1"
	defaultAPIKeyEnv       = "NEW_API_KEY"
	defaultHTTPVersion     = "2"
	defaultConcurrency     = 300
	defaultMaxConcurrency  = 4800
	defaultStageDuration   = 3 * time.Minute
	fakeInputTokens        = int64(20_000)
	fakeOutputTokens       = int64(2_000)
	maxSSEEventBytes       = 4 << 20
	maxLatencySamples      = 100_000
	fakeContentMarker      = "cortex-fake-llm"
	paidSonnetModel        = "claude-sonnet-5"
	paidSonnetPrompt       = "Respond with exactly: hi"
	paidSonnetConcurrency  = 300
	paidSonnetMaxTokens    = 2
	maxPaidStageDuration   = 2 * time.Minute
	maxPaidCostUSD         = 2.0
	maxPaidRequests        = 50_000
	sonnetInputPerMillion  = 2.0
	sonnetCachedPerMillion = 0.20
	sonnetOutputPerMillion = 10.0
)

type config struct {
	Target           string
	Model            string
	APIKey           string
	StartConcurrency int
	MaxConcurrency   int
	StageDuration    time.Duration
	SuccessThreshold float64
	PreflightOnly    bool
	OutputDir        string
	RunID            string
	ConnectTimeout   time.Duration
	HeaderTimeout    time.Duration
	IdleTimeout      time.Duration
	RequestTimeout   time.Duration
	HTTPVersion      string
	AllowPaidSonnet  bool
	MaxCostUSD       float64
	MaxRequests      int
}

func (c config) endpoint() (string, error) {
	u, err := url.Parse(c.Target)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid --target %q", c.Target)
	}
	const endpointPath = "/v1/chat/completions"
	if u.Path == "" || u.Path == "/" {
		u.Path = endpointPath
	} else if !strings.HasSuffix(strings.TrimSuffix(u.Path, "/"), endpointPath) {
		u.Path = path.Join(u.Path, "chat/completions")
	}
	return u.String(), nil
}

func (c config) validate() error {
	if c.APIKey == "" {
		return fmt.Errorf("%s must be set", defaultAPIKeyEnv)
	}
	if c.Model == "" {
		return errors.New("--model must not be empty")
	}
	if c.Model != defaultModel && c.Model != paidSonnetModel {
		return fmt.Errorf("--model must be %q or %q", defaultModel, paidSonnetModel)
	}
	if c.Model == defaultModel && (c.AllowPaidSonnet || c.MaxCostUSD != 0 || c.MaxRequests != 0) {
		return errors.New("--allow-paid-sonnet, --max-cost-usd, and --max-requests are only valid with --model claude-sonnet-5")
	}
	if c.Model == paidSonnetModel {
		if !c.AllowPaidSonnet {
			return errors.New("--allow-paid-sonnet is required for claude-sonnet-5")
		}
		if c.StartConcurrency != paidSonnetConcurrency || c.MaxConcurrency != paidSonnetConcurrency {
			return errors.New("paid Sonnet mode requires --start-concurrency and --max-concurrency exactly 300")
		}
		if c.StageDuration <= 0 || c.StageDuration > maxPaidStageDuration {
			return errors.New("paid Sonnet --stage-duration must be positive and at most 2m")
		}
		if c.MaxCostUSD <= 0 || c.MaxCostUSD > maxPaidCostUSD || math.IsNaN(c.MaxCostUSD) {
			return errors.New("paid Sonnet --max-cost-usd must be in (0, 2]")
		}
		if c.MaxRequests <= 0 || c.MaxRequests > maxPaidRequests {
			return errors.New("paid Sonnet --max-requests must be in [1, 50000]")
		}
	}
	if c.StartConcurrency < 1 || c.MaxConcurrency < c.StartConcurrency {
		return errors.New("concurrency must be positive and --max-concurrency must be at least --start-concurrency")
	}
	if c.StageDuration <= 0 || c.ConnectTimeout <= 0 || c.HeaderTimeout <= 0 || c.IdleTimeout <= 0 || c.RequestTimeout <= 0 {
		return errors.New("durations and timeouts must be positive")
	}
	if c.SuccessThreshold <= 0 || c.SuccessThreshold > 1 || math.IsNaN(c.SuccessThreshold) {
		return errors.New("--success-threshold must be in (0, 1]")
	}
	if c.HTTPVersion != "1.1" && c.HTTPVersion != "2" {
		return errors.New("--http-version must be either 1.1 or 2")
	}
	_, err := c.endpoint()
	return err
}

type chatRequest struct {
	Model         string         `json:"model"`
	Messages      []chatMessage  `json:"messages"`
	Stream        bool           `json:"stream"`
	StreamOptions chatStreamOpts `json:"stream_options"`
	MaxTokens     int            `json:"max_tokens,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatStreamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type sample struct {
	StageConcurrency     int       `json:"stage_concurrency"`
	Sequence             int64     `json:"sequence"`
	StartedAt            time.Time `json:"started_at"`
	HTTPStatus           int       `json:"http_status,omitempty"`
	HTTPProtocol         string    `json:"http_protocol,omitempty"`
	Success              bool      `json:"success"`
	FailureKind          string    `json:"failure_kind,omitempty"`
	Error                string    `json:"error,omitempty"`
	TTFTMS               float64   `json:"ttft_ms,omitempty"`
	CompletionMS         float64   `json:"completion_ms,omitempty"`
	RequestDurationMS    float64   `json:"request_duration_ms"`
	InputTokens          int64     `json:"input_tokens,omitempty"`
	CachedInputTokens    int64     `json:"cached_input_tokens,omitempty"`
	OutputTokens         int64     `json:"output_tokens,omitempty"`
	UsageKnown           bool      `json:"usage_known"`
	EstimatedListCostUSD float64   `json:"estimated_list_cost_usd,omitempty"`
	ContentBytes         int       `json:"content_bytes,omitempty"`
	SSEEvents            int       `json:"sse_events,omitempty"`
}

type tokenUsage struct {
	RequestsWithUsage    int     `json:"requests_with_usage"`
	UnknownUsageRequests int     `json:"unknown_usage_requests"`
	InputTokens          int64   `json:"input_tokens"`
	CachedInputTokens    int64   `json:"cached_input_tokens"`
	UncachedInputTokens  int64   `json:"uncached_input_tokens"`
	OutputTokens         int64   `json:"output_tokens"`
	EstimatedListCostUSD float64 `json:"estimated_list_cost_usd"`
}

func (u tokenUsage) estimatedSonnetCostUSD() float64 {
	uncached := u.InputTokens - u.CachedInputTokens
	if uncached < 0 {
		uncached = 0
	}
	return (float64(uncached)*sonnetInputPerMillion +
		float64(u.CachedInputTokens)*sonnetCachedPerMillion +
		float64(u.OutputTokens)*sonnetOutputPerMillion) / 1_000_000
}

type latencyStats struct {
	Count int     `json:"count"`
	Mean  float64 `json:"mean_ms"`
	P50   float64 `json:"p50_ms"`
	P90   float64 `json:"p90_ms"`
	P95   float64 `json:"p95_ms"`
	P99   float64 `json:"p99_ms"`
	Max   float64 `json:"max_ms"`
}

type stageSummary struct {
	Concurrency           int            `json:"concurrency"`
	SchedulingStart       time.Time      `json:"scheduling_started_at"`
	SchedulingMS          float64        `json:"scheduling_window_ms"`
	ElapsedMS             float64        `json:"elapsed_ms_including_drain"`
	Requests              int            `json:"requests"`
	Successes             int            `json:"successes"`
	Failures              int            `json:"failures"`
	SuccessRate           float64        `json:"success_rate"`
	RequestsPerSec        float64        `json:"requests_per_second"`
	PeakActive            int64          `json:"peak_active"`
	TTFT                  latencyStats   `json:"time_to_first_token"`
	Completion            latencyStats   `json:"time_to_complete"`
	AllRequests           latencyStats   `json:"all_request_duration"`
	LatencySamplesDropped int            `json:"latency_samples_dropped,omitempty"`
	HTTPStatuses          map[string]int `json:"http_statuses"`
	FailureKinds          map[string]int `json:"failure_kinds"`
	InputTokens           int64          `json:"input_tokens"`
	OutputTokens          int64          `json:"output_tokens"`
	Usage                 tokenUsage     `json:"usage"`
	StoppedByCap          string         `json:"stopped_by_cap,omitempty"`
}

type runSummary struct {
	RunID             string         `json:"run_id"`
	Target            string         `json:"target"`
	Model             string         `json:"model"`
	HTTPVersion       string         `json:"http_version"`
	StartedAt         time.Time      `json:"started_at"`
	FinishedAt        time.Time      `json:"finished_at"`
	SuccessThreshold  float64        `json:"success_threshold"`
	StoppedBelowLimit bool           `json:"stopped_below_success_threshold"`
	Preflight         sample         `json:"preflight"`
	Stages            []stageSummary `json:"stages"`
	TotalUsage        tokenUsage     `json:"total_usage"`
	StopReason        string         `json:"stop_reason,omitempty"`
}

type runner struct {
	cfg          config
	endpoint     string
	client       *http.Client
	sequence     atomic.Int64
	active       atomic.Int64
	peak         atomic.Int64
	after        func(time.Duration) <-chan time.Time
	stageElapsed func(time.Time) time.Duration
	now          func() time.Time
	limiter      *paidLimiter
}

type paidLimiter struct {
	mu          sync.Mutex
	maxRequests int
	maxCostUSD  float64
	started     int
	costUSD     float64
	stopReason  string
	stopped     chan struct{}
	stopOnce    sync.Once
}

func newPaidLimiter(maxRequests int, maxCostUSD float64) *paidLimiter {
	return &paidLimiter{maxRequests: maxRequests, maxCostUSD: maxCostUSD, stopped: make(chan struct{})}
}

func (l *paidLimiter) tryStart() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopReason != "" {
		return false
	}
	if l.started >= l.maxRequests {
		l.stopLocked("max_requests")
		return false
	}
	if l.costUSD >= l.maxCostUSD {
		l.stopLocked("max_cost_usd")
		return false
	}
	l.started++
	return true
}

func (l *paidLimiter) observe(result sample) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !result.UsageKnown {
		l.stopLocked("unknown_usage")
		return
	}
	l.costUSD += result.EstimatedListCostUSD
	if l.costUSD >= l.maxCostUSD {
		l.stopLocked("max_cost_usd")
	} else if l.started >= l.maxRequests {
		l.stopLocked("max_requests")
	}
}

func (l *paidLimiter) stopLocked(reason string) {
	if l.stopReason != "" {
		return
	}
	l.stopReason = reason
	l.stopOnce.Do(func() { close(l.stopped) })
}

func (l *paidLimiter) reason() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stopReason
}

func newRunner(cfg config) (*runner, error) {
	endpoint, err := cfg.endpoint()
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: cfg.ConnectTimeout, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     cfg.HTTPVersion == "2",
		MaxIdleConns:          cfg.MaxConcurrency,
		MaxIdleConnsPerHost:   cfg.MaxConcurrency,
		MaxConnsPerHost:       cfg.MaxConcurrency,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: cfg.HeaderTimeout,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	if cfg.HTTPVersion == "1.1" {
		// An empty TLSNextProto map disables automatic HTTP/2 negotiation. This is
		// required for a trustworthy HTTP/1.1 control run; ForceAttemptHTTP2=false
		// alone does not express that guarantee for every Transport configuration.
		transport.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
	}
	runner := &runner{
		cfg: cfg, endpoint: endpoint, after: time.After, stageElapsed: time.Since, now: time.Now,
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("redirects are disabled")
			},
		},
	}
	if cfg.Model == paidSonnetModel {
		runner.limiter = newPaidLimiter(cfg.MaxRequests, cfg.MaxCostUSD)
	}
	return runner, nil
}

func (r *runner) runStage(parent context.Context, concurrency int, consume func(sample) error) (stageSummary, error) {
	started := r.schedulingNow()
	schedulingDeadline := started.Add(r.cfg.StageDuration)
	r.peak.Store(0)
	stageCtx, cancel := context.WithCancel(parent)
	defer cancel()
	stopScheduling := make(chan struct{})
	var schedulingWindowNS atomic.Int64
	go func() {
		if r.limiter == nil {
			select {
			case <-r.after(r.cfg.StageDuration):
			case <-stageCtx.Done():
			}
		} else {
			select {
			case <-r.after(r.cfg.StageDuration):
			case <-r.limiter.stopped:
			case <-stageCtx.Done():
			}
		}
		schedulingWindow := r.elapsedStage(started)
		if schedulingWindow <= 0 {
			schedulingWindow = time.Nanosecond
		}
		schedulingWindowNS.Store(int64(schedulingWindow))
		close(stopScheduling)
	}()
	results := make(chan sample, concurrency)
	var workers sync.WaitGroup
	for range concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-stopScheduling:
					return
				case <-stageCtx.Done():
					return
				default:
				}
				if !r.schedulingNow().Before(schedulingDeadline) {
					return
				}
				if r.limiter != nil && !r.limiter.tryStart() {
					return
				}
				result := r.execute(stageCtx, concurrency, r.sequence.Add(1))
				if r.limiter != nil {
					r.limiter.observe(result)
				}
				select {
				case results <- result:
				case <-stageCtx.Done():
					return
				}
			}
		}()
	}
	go func() {
		workers.Wait()
		close(results)
	}()

	accumulator := newStageAccumulator(concurrency, started)
	var consumeErr error
	for result := range results {
		if consumeErr == nil {
			consumeErr = consume(result)
			if consumeErr != nil {
				cancel()
			}
		}
		accumulator.observe(result)
	}
	<-stopScheduling
	if transport, ok := r.client.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
	report := accumulator.finish(time.Duration(schedulingWindowNS.Load()), r.elapsedStage(started), r.peak.Load())
	if r.limiter != nil {
		report.StoppedByCap = r.limiter.reason()
	}
	return report, consumeErr
}

func (r *runner) elapsedStage(started time.Time) time.Duration {
	if r.stageElapsed != nil {
		return r.stageElapsed(started)
	}
	return time.Since(started)
}

func (r *runner) schedulingNow() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *runner) execute(parent context.Context, concurrency int, sequence int64) (result sample) {
	started := time.Now()
	result = sample{StageConcurrency: concurrency, Sequence: sequence, StartedAt: started}
	defer func() { result.RequestDurationMS = milliseconds(time.Since(started)) }()
	active := r.active.Add(1)
	defer r.active.Add(-1)
	for peak := r.peak.Load(); active > peak && !r.peak.CompareAndSwap(peak, active); peak = r.peak.Load() {
	}

	maxTokens := 0
	prompt := "echo HI"
	if r.cfg.Model == defaultModel {
		maxTokens = int(fakeOutputTokens)
	} else {
		maxTokens = paidSonnetMaxTokens
		prompt = paidSonnetPrompt
	}
	body, err := common.Marshal(chatRequest{
		Model:         r.cfg.Model,
		Messages:      []chatMessage{{Role: "user", Content: prompt}},
		Stream:        true,
		StreamOptions: chatStreamOpts{IncludeUsage: true},
		MaxTokens:     maxTokens,
	})
	if err != nil {
		fail(&result, "request", err)
		return result
	}
	ctx, cancel := context.WithTimeout(parent, r.cfg.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(body))
	if err != nil {
		fail(&result, "request", err)
		return result
	}
	req.Header.Set("Authorization", "Bearer "+r.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := r.client.Do(req)
	if err != nil {
		fail(&result, classifyError(err), err)
		return result
	}
	defer resp.Body.Close()
	result.HTTPStatus = resp.StatusCode
	result.HTTPProtocol = resp.Proto
	if (r.cfg.HTTPVersion == "1.1" && (resp.ProtoMajor != 1 || resp.ProtoMinor != 1)) ||
		(r.cfg.HTTPVersion == "2" && resp.ProtoMajor != 2) {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		fail(&result, "protocol", fmt.Errorf("negotiated %s, required HTTP/%s", resp.Proto, r.cfg.HTTPVersion))
		return result
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		fail(&result, "http_status", fmt.Errorf("HTTP %d", resp.StatusCode))
		return result
	}
	if err := r.validateStream(resp.Body, started, &result); err != nil {
		fail(&result, classifyError(err), err)
		return result
	}
	result.Success = true
	return result
}

var (
	errMalformedSSE   = errors.New("malformed SSE")
	errMissingContent = errors.New("missing content")
	errMissingFinish  = errors.New("missing finish event")
	errMissingUsage   = errors.New("missing usage")
	errInvalidUsage   = errors.New("unexpected fake-model usage")
	errInvalidDone    = errors.New("expected exactly one [DONE]")
	errEventAfterDone = errors.New("event received after [DONE]")
	errIdleTimeout    = errors.New("idle timeout")
	errInvalidContent = errors.New("unexpected response content")
)

type sseEvent struct{ Data string }

func (r *runner) validateStream(body io.ReadCloser, started time.Time, result *sample) error {
	reader := bufio.NewReaderSize(body, 32*1024)
	var content strings.Builder
	finishSeen := false
	usageSeen := false
	doneCount := 0
	for {
		event, err := nextSSEEvent(reader, body, r.cfg.IdleTimeout)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		result.SSEEvents++
		if event.Data == "[DONE]" {
			if !finishSeen || !usageSeen {
				return fmt.Errorf("%w: [DONE] arrived before finish and usage", errMalformedSSE)
			}
			doneCount++
			if doneCount == 1 {
				result.CompletionMS = milliseconds(time.Since(started))
			}
			continue
		}
		if doneCount > 0 {
			return errEventAfterDone
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens        int64 `json:"prompt_tokens"`
				CompletionTokens    int64 `json:"completion_tokens"`
				PromptTokensDetails struct {
					CachedTokens int64 `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if err := common.UnmarshalJsonStr(event.Data, &chunk); err != nil {
			return fmt.Errorf("%w: %v", errMalformedSSE, err)
		}
		if chunk.Usage != nil {
			if usageSeen {
				return fmt.Errorf("%w: duplicate usage frame", errMalformedSSE)
			}
			if !finishSeen || len(chunk.Choices) != 0 {
				return fmt.Errorf("%w: usage must follow finish in a choices-free frame", errMalformedSSE)
			}
			usageSeen = true
			result.InputTokens = chunk.Usage.PromptTokens
			result.CachedInputTokens = chunk.Usage.PromptTokensDetails.CachedTokens
			result.OutputTokens = chunk.Usage.CompletionTokens
			if result.InputTokens < 0 || result.CachedInputTokens < 0 || result.OutputTokens < 0 || result.CachedInputTokens > result.InputTokens {
				return fmt.Errorf("%w: invalid token counts", errInvalidUsage)
			}
			result.UsageKnown = true
			if r.cfg.Model == paidSonnetModel {
				if result.InputTokens == 0 || result.OutputTokens == 0 {
					return fmt.Errorf("%w: paid response reported zero prompt or completion tokens", errInvalidUsage)
				}
				result.EstimatedListCostUSD = (tokenUsage{
					InputTokens: result.InputTokens, CachedInputTokens: result.CachedInputTokens, OutputTokens: result.OutputTokens,
				}).estimatedSonnetCostUSD()
			}
		}
		for _, choice := range chunk.Choices {
			if finishSeen {
				return fmt.Errorf("%w: choice received after finish", errMalformedSSE)
			}
			if choice.Delta.Content != "" {
				if result.TTFTMS == 0 {
					result.TTFTMS = milliseconds(time.Since(started))
				}
				content.WriteString(choice.Delta.Content)
			}
			if choice.FinishReason != nil {
				validFinish := *choice.FinishReason == "stop"
				if r.cfg.Model == paidSonnetModel {
					validFinish = validFinish || *choice.FinishReason == "length"
				}
				if finishSeen || !validFinish {
					return fmt.Errorf("%w: invalid or duplicate finish_reason", errMissingFinish)
				}
				finishSeen = true
			}
		}
	}
	result.ContentBytes = content.Len()
	if content.Len() == 0 {
		return errMissingContent
	}
	if r.cfg.Model == defaultModel && !strings.Contains(content.String(), fakeContentMarker) {
		return fmt.Errorf("%w: required marker %q not found", errMissingContent, fakeContentMarker)
	}
	if r.cfg.Model == paidSonnetModel && content.String() != "hi" {
		return fmt.Errorf("%w: got %q", errInvalidContent, content.String())
	}
	if !finishSeen {
		return errMissingFinish
	}
	if !usageSeen {
		return errMissingUsage
	}
	if doneCount != 1 {
		return fmt.Errorf("%w: got %d", errInvalidDone, doneCount)
	}
	if r.cfg.Model == defaultModel && (result.InputTokens != fakeInputTokens || result.OutputTokens != fakeOutputTokens) {
		return fmt.Errorf("%w: got %d/%d, want %d/%d", errInvalidUsage, result.InputTokens, result.OutputTokens, fakeInputTokens, fakeOutputTokens)
	}
	if r.cfg.Model == paidSonnetModel && result.OutputTokens > paidSonnetMaxTokens {
		return fmt.Errorf("%w: paid response reported %d completion tokens, want at most %d", errInvalidUsage, result.OutputTokens, paidSonnetMaxTokens)
	}
	return nil
}

func nextSSEEvent(reader *bufio.Reader, body io.Closer, idleTimeout time.Duration) (sseEvent, error) {
	type readResult struct {
		event sseEvent
		err   error
	}
	result := make(chan readResult, 1)
	go func() {
		event, err := readSSEEvent(reader)
		result <- readResult{event: event, err: err}
	}()
	timer := time.NewTimer(idleTimeout)
	defer timer.Stop()
	select {
	case read := <-result:
		return read.event, read.err
	case <-timer.C:
		_ = body.Close()
		<-result
		return sseEvent{}, fmt.Errorf("%w after %s", errIdleTimeout, idleTimeout)
	}
}

func readSSEEvent(reader *bufio.Reader) (sseEvent, error) {
	var data []string
	bytesRead := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			if len(data) > 0 {
				return sseEvent{}, fmt.Errorf("%w: unterminated event", errMalformedSSE)
			}
			return sseEvent{}, err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		bytesRead += len(line)
		if bytesRead > maxSSEEventBytes {
			return sseEvent{}, fmt.Errorf("%w: event exceeds %d bytes", errMalformedSSE, maxSSEEventBytes)
		}
		if line == "" {
			if len(data) > 0 {
				return sseEvent{Data: strings.Join(data, "\n")}, nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data:")
			data = append(data, strings.TrimPrefix(value, " "))
		}
		if err != nil {
			return sseEvent{}, fmt.Errorf("%w: unterminated event", errMalformedSSE)
		}
	}
}

type stageAccumulator struct {
	report     stageSummary
	ttft       []float64
	completion []float64
	allRequest []float64
}

func newStageAccumulator(concurrency int, started time.Time) *stageAccumulator {
	expectedSamples := concurrency * 4
	if expectedSamples > maxLatencySamples {
		expectedSamples = maxLatencySamples
	}
	return &stageAccumulator{
		report: stageSummary{
			Concurrency: concurrency, SchedulingStart: started,
			HTTPStatuses: make(map[string]int), FailureKinds: make(map[string]int),
		},
		ttft: make([]float64, 0, expectedSamples), completion: make([]float64, 0, expectedSamples),
		allRequest: make([]float64, 0, expectedSamples),
	}
}

func (a *stageAccumulator) observe(result sample) {
	a.report.Requests++
	a.report.Usage.observe(result)
	if result.HTTPStatus != 0 {
		a.report.HTTPStatuses[fmt.Sprint(result.HTTPStatus)]++
	}
	if len(a.allRequest) < maxLatencySamples {
		a.allRequest = append(a.allRequest, result.RequestDurationMS)
	} else {
		a.report.LatencySamplesDropped++
	}
	if result.Success {
		a.report.Successes++
		if len(a.ttft) < maxLatencySamples {
			a.ttft = append(a.ttft, result.TTFTMS)
		} else {
			a.report.LatencySamplesDropped++
		}
		if len(a.completion) < maxLatencySamples {
			a.completion = append(a.completion, result.CompletionMS)
		} else {
			a.report.LatencySamplesDropped++
		}
	} else {
		a.report.Failures++
		a.report.FailureKinds[result.FailureKind]++
	}
	a.report.InputTokens = a.report.Usage.InputTokens
	a.report.OutputTokens = a.report.Usage.OutputTokens
}

func (u *tokenUsage) observe(result sample) {
	if !result.UsageKnown {
		u.UnknownUsageRequests++
		return
	}
	u.RequestsWithUsage++
	u.InputTokens += result.InputTokens
	u.CachedInputTokens += result.CachedInputTokens
	u.UncachedInputTokens += result.InputTokens - result.CachedInputTokens
	u.OutputTokens += result.OutputTokens
	u.EstimatedListCostUSD += result.EstimatedListCostUSD
}

func (u *tokenUsage) add(other tokenUsage) {
	u.RequestsWithUsage += other.RequestsWithUsage
	u.UnknownUsageRequests += other.UnknownUsageRequests
	u.InputTokens += other.InputTokens
	u.CachedInputTokens += other.CachedInputTokens
	u.UncachedInputTokens += other.UncachedInputTokens
	u.OutputTokens += other.OutputTokens
	u.EstimatedListCostUSD += other.EstimatedListCostUSD
}

func (a *stageAccumulator) finish(scheduling, elapsed time.Duration, peak int64) stageSummary {
	a.report.SchedulingMS = milliseconds(scheduling)
	a.report.ElapsedMS = milliseconds(elapsed)
	a.report.PeakActive = peak
	if a.report.Requests > 0 {
		a.report.SuccessRate = float64(a.report.Successes) / float64(a.report.Requests)
	}
	if scheduling > 0 {
		a.report.RequestsPerSec = float64(a.report.Requests) / scheduling.Seconds()
	}
	a.report.TTFT = summarizeLatencies(a.ttft)
	a.report.Completion = summarizeLatencies(a.completion)
	a.report.AllRequests = summarizeLatencies(a.allRequest)
	return a.report
}

func summarizeStageForTest(concurrency int, started time.Time, scheduling, elapsed time.Duration, peak int64, samples []sample) stageSummary {
	accumulator := newStageAccumulator(concurrency, started)
	for _, result := range samples {
		accumulator.observe(result)
	}
	return accumulator.finish(scheduling, elapsed, peak)
}

func summarizeLatencies(values []float64) latencyStats {
	if len(values) == 0 {
		return latencyStats{}
	}
	sort.Float64s(values)
	var sum float64
	for _, value := range values {
		sum += value
	}
	return latencyStats{
		Count: len(values), Mean: sum / float64(len(values)),
		P50: percentile(values, .50), P90: percentile(values, .90),
		P95: percentile(values, .95), P99: percentile(values, .99),
		Max: values[len(values)-1],
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(math.Ceil(p*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	return sorted[index]
}

func fail(result *sample, kind string, err error) {
	result.FailureKind = kind
	result.Error = err.Error()
}

func classifyError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, errIdleTimeout):
		return "timeout"
	case errors.Is(err, errMalformedSSE):
		return "malformed_sse"
	case errors.Is(err, errMissingContent):
		return "missing_content"
	case errors.Is(err, errInvalidContent):
		return "invalid_content"
	case errors.Is(err, errMissingFinish):
		return "missing_finish"
	case errors.Is(err, errMissingUsage), errors.Is(err, errInvalidUsage):
		return "usage"
	case errors.Is(err, errInvalidDone), errors.Is(err, errEventAfterDone):
		return "terminal"
	default:
		return "transport"
	}
}

func milliseconds(duration time.Duration) float64 {
	return float64(duration) / float64(time.Millisecond)
}

type artifacts struct {
	runDir      string
	samplesPath string
	summaryPath string
	samples     *os.File
}

func createArtifacts(cfg config) (*artifacts, error) {
	runDir := filepath.Join(cfg.OutputDir, cfg.RunID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, err
	}
	samplesPath := filepath.Join(runDir, "samples.jsonl")
	samples, err := os.OpenFile(samplesPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	return &artifacts{runDir: runDir, samplesPath: samplesPath, summaryPath: filepath.Join(runDir, "summary.json"), samples: samples}, nil
}

func (a *artifacts) writeSample(result sample) error {
	encoded, err := common.Marshal(result)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	_, err = a.samples.Write(encoded)
	return err
}

func (a *artifacts) finish(report runSummary) error {
	var resultErr error
	if a.samples != nil {
		resultErr = errors.Join(resultErr, a.samples.Sync(), a.samples.Close())
		a.samples = nil
	}
	encoded, err := common.Marshal(report)
	if err != nil {
		return errors.Join(resultErr, err)
	}
	encoded = append(encoded, '\n')
	temporary, err := os.CreateTemp(a.runDir, ".summary-*.tmp")
	if err != nil {
		return errors.Join(resultErr, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(encoded); err != nil {
		return errors.Join(resultErr, err, temporary.Close())
	}
	if err := temporary.Sync(); err != nil {
		return errors.Join(resultErr, err, temporary.Close())
	}
	if err := temporary.Close(); err != nil {
		return errors.Join(resultErr, err)
	}
	if err := os.Rename(temporaryPath, a.summaryPath); err != nil {
		return errors.Join(resultErr, err)
	}
	return resultErr
}

func parseConfig() config {
	cfg := config{}
	flag.StringVar(&cfg.Target, "target", defaultTarget, "base URL or full Chat Completions endpoint")
	flag.StringVar(&cfg.Model, "model", defaultModel, "model ID")
	flag.IntVar(&cfg.StartConcurrency, "start-concurrency", defaultConcurrency, "first closed-loop stage concurrency")
	flag.IntVar(&cfg.MaxConcurrency, "max-concurrency", defaultMaxConcurrency, "hard concurrency ceiling")
	flag.DurationVar(&cfg.StageDuration, "stage-duration", defaultStageDuration, "request scheduling duration per stage")
	flag.Float64Var(&cfg.SuccessThreshold, "success-threshold", .90, "stop when stage success rate is below this fraction")
	flag.BoolVar(&cfg.PreflightOnly, "preflight-only", false, "run exactly one validated request and skip all load stages")
	flag.StringVar(&cfg.OutputDir, "output-dir", "stress-results", "artifact directory")
	flag.StringVar(&cfg.RunID, "run-id", "", "run identifier; defaults to a UTC timestamp")
	flag.DurationVar(&cfg.ConnectTimeout, "connect-timeout", 10*time.Second, "TCP connection timeout")
	flag.DurationVar(&cfg.HeaderTimeout, "header-timeout", 20*time.Second, "response header timeout")
	flag.DurationVar(&cfg.IdleTimeout, "idle-timeout", 20*time.Second, "maximum gap between SSE events")
	flag.DurationVar(&cfg.RequestTimeout, "request-timeout", 90*time.Second, "per-request deadline")
	flag.StringVar(&cfg.HTTPVersion, "http-version", defaultHTTPVersion, "HTTP protocol to require: 1.1 or 2")
	flag.BoolVar(&cfg.AllowPaidSonnet, "allow-paid-sonnet", false, "explicitly allow billed claude-sonnet-5 traffic")
	flag.Float64Var(&cfg.MaxCostUSD, "max-cost-usd", 0, "paid Sonnet estimated list-cost scheduling cap (required, max 2)")
	flag.IntVar(&cfg.MaxRequests, "max-requests", 0, "paid Sonnet hard request cap including preflight (required, max 50000)")
	flag.Parse()
	cfg.APIKey = os.Getenv(defaultAPIKeyEnv)
	if cfg.RunID == "" {
		cfg.RunID = "newapi-stress-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	}
	return cfg
}

func run(ctx context.Context, cfg config) (samplesPath, summaryPath string, returnErr error) {
	if err := cfg.validate(); err != nil {
		return "", "", fmt.Errorf("configuration: %w", err)
	}
	runner, err := newRunner(cfg)
	if err != nil {
		return "", "", fmt.Errorf("initialization: %w", err)
	}
	artifacts, err := createArtifacts(cfg)
	if err != nil {
		return "", "", fmt.Errorf("create artifacts: %w", err)
	}
	report := runSummary{
		RunID: cfg.RunID, Target: runner.endpoint, Model: cfg.Model, HTTPVersion: cfg.HTTPVersion,
		StartedAt: time.Now().UTC(), SuccessThreshold: cfg.SuccessThreshold,
	}
	defer func() {
		report.FinishedAt = time.Now().UTC()
		if runner.limiter != nil && report.StopReason == "" {
			report.StopReason = runner.limiter.reason()
		}
		if err := artifacts.finish(report); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("finalize artifacts: %w", err))
		}
	}()

	if runner.limiter != nil && !runner.limiter.tryStart() {
		return artifacts.samplesPath, artifacts.summaryPath, errors.New("paid preflight blocked by safety cap")
	}
	report.Preflight = runner.execute(ctx, 1, runner.sequence.Add(1))
	if runner.limiter != nil {
		runner.limiter.observe(report.Preflight)
	}
	report.TotalUsage.observe(report.Preflight)
	if err := artifacts.writeSample(report.Preflight); err != nil {
		return artifacts.samplesPath, artifacts.summaryPath, fmt.Errorf("write preflight sample: %w", err)
	}
	if !report.Preflight.Success {
		return artifacts.samplesPath, artifacts.summaryPath, fmt.Errorf(
			"preflight failed (%s): %s", report.Preflight.FailureKind, report.Preflight.Error,
		)
	}
	if cfg.PreflightOnly {
		if runner.limiter != nil {
			report.StopReason = runner.limiter.reason()
		}
		return artifacts.samplesPath, artifacts.summaryPath, nil
	}
	if runner.limiter != nil && runner.limiter.reason() != "" {
		report.StopReason = runner.limiter.reason()
		return artifacts.samplesPath, artifacts.summaryPath, nil
	}

	for concurrency := cfg.StartConcurrency; concurrency <= cfg.MaxConcurrency; concurrency *= 2 {
		stage, runErr := runner.runStage(ctx, concurrency, artifacts.writeSample)
		report.Stages = append(report.Stages, stage)
		report.TotalUsage.add(stage.Usage)
		if stage.StoppedByCap != "" {
			report.StopReason = stage.StoppedByCap
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return artifacts.samplesPath, artifacts.summaryPath, ctxErr
		}
		if runErr != nil {
			return artifacts.samplesPath, artifacts.summaryPath, fmt.Errorf("stage %d: %w", concurrency, runErr)
		}
		encoded, _ := common.Marshal(stage)
		fmt.Println(string(encoded))
		if stage.SuccessRate < cfg.SuccessThreshold {
			report.StoppedBelowLimit = true
			break
		}
		if stage.StoppedByCap != "" {
			break
		}
		if concurrency > cfg.MaxConcurrency/2 {
			break
		}
	}
	if ctx.Err() != nil {
		return artifacts.samplesPath, artifacts.summaryPath, ctx.Err()
	}
	return artifacts.samplesPath, artifacts.summaryPath, nil
}

func main() {
	cfg := parseConfig()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	samplesPath, summaryPath, err := run(ctx, cfg)
	stop()
	if samplesPath != "" {
		fmt.Printf("samples: %s\nsummary: %s\n", samplesPath, summaryPath)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

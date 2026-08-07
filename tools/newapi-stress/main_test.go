package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testConfig(target string) config {
	return config{
		Target: target, Model: defaultModel, APIKey: "test-key",
		StartConcurrency: 1, MaxConcurrency: 2, StageDuration: 20 * time.Millisecond,
		SuccessThreshold: .9, OutputDir: "unused", RunID: "test",
		ConnectTimeout: time.Second, HeaderTimeout: time.Second,
		IdleTimeout: time.Second, RequestTimeout: 2 * time.Second,
		HTTPVersion: "1.1",
	}
}

func sonnetTestConfig(target string) config {
	cfg := testConfig(target)
	cfg.Model = paidSonnetModel
	cfg.AllowPaidSonnet = true
	cfg.StartConcurrency = paidSonnetConcurrency
	cfg.MaxConcurrency = paidSonnetConcurrency
	cfg.StageDuration = time.Minute
	cfg.MaxCostUSD = 1
	cfg.MaxRequests = 50_000
	return cfg
}

func validStream(inputTokens, outputTokens int64) string {
	return "data: {\"id\":\"chatcmpl_test\",\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl_test\",\"choices\":[{\"delta\":{\"content\":\"cortex-fake-llm hi\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl_test\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		fmt.Sprintf("data: {\"id\":\"chatcmpl_test\",\"choices\":[],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d}}\n\n", inputTokens, outputTokens) +
		"data: [DONE]\n\n"
}

func validSonnetStream(content string, inputTokens, cachedInputTokens, outputTokens int64) string {
	return validSonnetStreamWithFinish(content, inputTokens, cachedInputTokens, outputTokens, "stop")
}

func validSonnetStreamWithFinish(content string, inputTokens, cachedInputTokens, outputTokens int64, finishReason string) string {
	return "data: {\"id\":\"chatcmpl_test\",\"choices\":[{\"delta\":{\"content\":" + fmt.Sprintf("%q", content) + "},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl_test\",\"choices\":[{\"delta\":{},\"finish_reason\":" + fmt.Sprintf("%q", finishReason) + "}]}\n\n" +
		fmt.Sprintf("data: {\"id\":\"chatcmpl_test\",\"choices\":[],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d,\"prompt_tokens_details\":{\"cached_tokens\":%d}}}\n\n", inputTokens, outputTokens, cachedInputTokens) +
		"data: [DONE]\n\n"
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestEndpointDefaultsToChatCompletions(t *testing.T) {
	t.Parallel()
	cfg := testConfig("https://newapi.withcortex.ai/v1")
	endpoint, err := cfg.endpoint()
	require.NoError(t, err)
	assert.Equal(t, "https://newapi.withcortex.ai/v1/chat/completions", endpoint)
}

func TestValidateRequiresAPIKeyFromEnvironmentConfiguration(t *testing.T) {
	t.Parallel()
	cfg := testConfig("https://example.test/v1")
	cfg.APIKey = ""
	err := cfg.validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), defaultAPIKeyEnv)
}

func TestValidatePaidSonnetInterlocks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*config)
		want   string
	}{
		{name: "requires opt in", mutate: func(c *config) { c.AllowPaidSonnet = false }, want: "--allow-paid-sonnet"},
		{name: "rejects another paid model", mutate: func(c *config) { c.Model = "claude-opus-5" }, want: "--model"},
		{name: "fixed start concurrency", mutate: func(c *config) { c.StartConcurrency = 299 }, want: "exactly 300"},
		{name: "fixed max concurrency", mutate: func(c *config) { c.MaxConcurrency = 600 }, want: "exactly 300"},
		{name: "positive duration", mutate: func(c *config) { c.StageDuration = 0 }, want: "--stage-duration"},
		{name: "duration at most two minutes", mutate: func(c *config) { c.StageDuration = 2*time.Minute + time.Nanosecond }, want: "--stage-duration"},
		{name: "positive cost cap", mutate: func(c *config) { c.MaxCostUSD = 0 }, want: "--max-cost-usd"},
		{name: "cost cap at most two dollars", mutate: func(c *config) { c.MaxCostUSD = 2.01 }, want: "--max-cost-usd"},
		{name: "positive request cap", mutate: func(c *config) { c.MaxRequests = 0 }, want: "--max-requests"},
		{name: "request cap at most fifty thousand", mutate: func(c *config) { c.MaxRequests = 50_001 }, want: "--max-requests"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := sonnetTestConfig("https://example.test/v1")
			tt.mutate(&cfg)
			err := cfg.validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestValidateFakeModeRejectsPaidControlsAndPreservesDefaults(t *testing.T) {
	t.Parallel()
	cfg := testConfig("https://example.test/v1")
	require.NoError(t, cfg.validate())
	mutations := []func(*config){
		func(c *config) { c.AllowPaidSonnet = true },
		func(c *config) { c.MaxCostUSD = 1 },
		func(c *config) { c.MaxRequests = 1 },
	}
	for _, mutate := range mutations {
		candidate := cfg
		mutate(&candidate)
		err := candidate.validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "only valid")
	}
}

func TestSonnetPriceUsesCachedAndUncachedInputRates(t *testing.T) {
	t.Parallel()
	usage := tokenUsage{InputTokens: 1000, CachedInputTokens: 400, OutputTokens: 20}
	assert.InDelta(t, 0.00148, usage.estimatedSonnetCostUSD(), 1e-12)
}

func TestPaidCostCapAllowsOnlyOneInFlightWaveOfOvershoot(t *testing.T) {
	t.Parallel()
	limiter := newPaidLimiter(50_000, 0.00001)
	for range paidSonnetConcurrency {
		require.True(t, limiter.tryStart())
	}
	limiter.observe(sample{UsageKnown: true, EstimatedListCostUSD: 0.00001})
	for range paidSonnetConcurrency {
		assert.False(t, limiter.tryStart())
	}
	limiter.mu.Lock()
	started := limiter.started
	limiter.mu.Unlock()
	assert.Equal(t, paidSonnetConcurrency, started)
	assert.Equal(t, "max_cost_usd", limiter.reason())
}

func TestPaidLimiterStopsOnFirstUnknownUsage(t *testing.T) {
	t.Parallel()
	limiter := newPaidLimiter(50_000, 1)
	require.True(t, limiter.tryStart())
	limiter.observe(sample{UsageKnown: false})
	assert.Equal(t, "unknown_usage", limiter.reason())
	assert.False(t, limiter.tryStart())
}

func TestValidateRejectsUnknownHTTPVersion(t *testing.T) {
	t.Parallel()
	cfg := testConfig("https://example.test/v1")
	cfg.HTTPVersion = "3"
	err := cfg.validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--http-version")
}

func TestNewRunnerConfiguresRequiredHTTPVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		version           string
		forceAttemptHTTP2 bool
		disablesTLSHTTP2  bool
	}{
		{name: "HTTP/2 by default", version: "2", forceAttemptHTTP2: true},
		{name: "HTTP/1.1", version: "1.1", disablesTLSHTTP2: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig("https://example.test/v1")
			cfg.HTTPVersion = tt.version
			runner, err := newRunner(cfg)
			require.NoError(t, err)
			transport, ok := runner.client.Transport.(*http.Transport)
			require.True(t, ok)
			assert.Equal(t, tt.forceAttemptHTTP2, transport.ForceAttemptHTTP2)
			assert.Equal(t, tt.disablesTLSHTTP2, transport.TLSNextProto != nil)
		})
	}
}

func TestExecuteRejectsNegotiatedProtocolMismatch(t *testing.T) {
	t.Parallel()
	cfg := testConfig("https://example.test/v1")
	cfg.HTTPVersion = "2"
	runner, err := newRunner(cfg)
	require.NoError(t, err)
	runner.client.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(validStream(fakeInputTokens, fakeOutputTokens))),
		}, nil
	})

	result := runner.execute(context.Background(), 1, 1)

	assert.False(t, result.Success)
	assert.Equal(t, "protocol", result.FailureKind)
	assert.Equal(t, "HTTP/1.1", result.HTTPProtocol)
}

func TestValidFakeStreamRecordsTTFTCompletionAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, validStream(fakeInputTokens, fakeOutputTokens))
	}))
	defer server.Close()
	runner, err := newRunner(testConfig(server.URL))
	require.NoError(t, err)

	result := runner.execute(context.Background(), 1, 1)

	require.True(t, result.Success, result.Error)
	assert.Positive(t, result.TTFTMS)
	assert.GreaterOrEqual(t, result.CompletionMS, result.TTFTMS)
	assert.Equal(t, fakeInputTokens, result.InputTokens)
	assert.Equal(t, fakeOutputTokens, result.OutputTokens)
	assert.Equal(t, len("cortex-fake-llm hi"), result.ContentBytes)
}

func TestValidSonnetStreamRequiresExactHiAndSendsBoundedPrompt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request chatRequest
		require.NoError(t, common.DecodeJson(r.Body, &request))
		assert.Equal(t, paidSonnetModel, request.Model)
		assert.Equal(t, paidSonnetMaxTokens, request.MaxTokens)
		require.Len(t, request.Messages, 1)
		assert.Equal(t, paidSonnetPrompt, request.Messages[0].Content)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, validSonnetStream("hi", 12, 4, 1))
	}))
	defer server.Close()
	runner, err := newRunner(sonnetTestConfig(server.URL))
	require.NoError(t, err)

	result := runner.execute(context.Background(), paidSonnetConcurrency, 1)

	require.True(t, result.Success, result.Error)
	assert.True(t, result.UsageKnown)
	assert.EqualValues(t, 12, result.InputTokens)
	assert.EqualValues(t, 4, result.CachedInputTokens)
	assert.EqualValues(t, 1, result.OutputTokens)
	assert.InDelta(t, 0.0000268, result.EstimatedListCostUSD, 1e-12)
}

func TestSonnetStreamRejectsNonHiContentAndInvalidCachedUsage(t *testing.T) {
	tests := []struct {
		name string
		body string
		kind string
	}{
		{name: "punctuated content", body: validSonnetStream("hi!", 10, 0, 1), kind: "invalid_content"},
		{name: "uppercase content", body: validSonnetStream("HI", 10, 0, 1), kind: "invalid_content"},
		{name: "whitespace content", body: validSonnetStream(" hi", 10, 0, 1), kind: "invalid_content"},
		{name: "newline content", body: validSonnetStream("hi\n", 10, 0, 1), kind: "invalid_content"},
		{name: "cached exceeds prompt", body: validSonnetStream("hi", 10, 11, 1), kind: "usage"},
		{name: "zero completion usage", body: validSonnetStream("hi", 10, 0, 0), kind: "usage"},
		{name: "more than requested completion tokens", body: validSonnetStream("hi", 10, 0, 3), kind: "usage"},
		{name: "missing usage", body: strings.Replace(validSonnetStream("hi", 10, 0, 1), "data: {\"id\":\"chatcmpl_test\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1,\"prompt_tokens_details\":{\"cached_tokens\":0}}}\n\n", "", 1), kind: "malformed_sse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			runner, err := newRunner(sonnetTestConfig(server.URL))
			require.NoError(t, err)
			result := runner.execute(context.Background(), paidSonnetConcurrency, 1)
			assert.False(t, result.Success)
			assert.Equal(t, tt.kind, result.FailureKind)
		})
	}
}

func TestPaidSonnetAcceptsLengthFinishForExactHiWithinTwoTokenBound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, validSonnetStreamWithFinish("hi", 10, 0, 2, "length"))
	}))
	defer server.Close()
	runner, err := newRunner(sonnetTestConfig(server.URL))
	require.NoError(t, err)
	result := runner.execute(context.Background(), paidSonnetConcurrency, 1)
	require.True(t, result.Success, result.Error)
}

func TestFakeStreamRejectsWrongUsageAndDuplicateDone(t *testing.T) {
	tests := []struct {
		name string
		body string
		kind string
	}{
		{name: "wrong usage", body: validStream(1, 1), kind: "usage"},
		{name: "missing content marker", body: strings.Replace(validStream(fakeInputTokens, fakeOutputTokens), fakeContentMarker, "other-output", 1), kind: "missing_content"},
		{name: "length finish remains invalid", body: strings.Replace(validStream(fakeInputTokens, fakeOutputTokens), `"finish_reason":"stop"`, `"finish_reason":"length"`, 1), kind: "missing_finish"},
		{name: "duplicate done", body: validStream(fakeInputTokens, fakeOutputTokens) + "data: [DONE]\n\n", kind: "terminal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			runner, err := newRunner(testConfig(server.URL))
			require.NoError(t, err)
			result := runner.execute(context.Background(), 1, 1)
			assert.False(t, result.Success)
			assert.Equal(t, tt.kind, result.FailureKind)
		})
	}
}

func TestRunStageStopsSchedulingThenDrains(t *testing.T) {
	var active atomic.Int64
	var executions atomic.Int64
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		executions.Add(1)
		active.Add(1)
		defer active.Add(-1)
		started <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, validStream(fakeInputTokens, fakeOutputTokens))
	}))
	defer server.Close()
	cfg := testConfig(server.URL)
	runner, err := newRunner(cfg)
	require.NoError(t, err)
	stopScheduling := make(chan time.Time, 1)
	runner.after = func(time.Duration) <-chan time.Time { return stopScheduling }
	baseTime := time.Unix(1_000, 0)
	deadline := baseTime.Add(cfg.StageDuration)
	var currentTimeNS atomic.Int64
	currentTimeNS.Store(baseTime.UnixNano())
	deadlineChecks := make(chan struct{}, 2)
	runner.now = func() time.Time {
		now := time.Unix(0, currentTimeNS.Load())
		if !now.Before(deadline) {
			deadlineChecks <- struct{}{}
		}
		return now
	}
	coordinatorAtBoundary := make(chan struct{})
	allowStopClosure := make(chan struct{})
	var elapsedCalls atomic.Int64
	runner.stageElapsed = func(time.Time) time.Duration {
		if elapsedCalls.Add(1) == 1 {
			close(coordinatorAtBoundary)
			<-allowStopClosure
		}
		return cfg.StageDuration
	}
	type stageResult struct {
		stage stageSummary
		err   error
	}
	completed := make(chan stageResult, 1)
	go func() {
		stage, err := runner.runStage(context.Background(), 2, func(sample) error { return nil })
		completed <- stageResult{stage: stage, err: err}
	}()
	<-started
	<-started
	currentTimeNS.Store(deadline.UnixNano())
	stopScheduling <- time.Now()
	<-coordinatorAtBoundary
	close(release)
	<-deadlineChecks
	<-deadlineChecks
	close(allowStopClosure)

	result := <-completed

	require.NoError(t, result.err)
	assert.Equal(t, 2, result.stage.Requests)
	assert.Equal(t, 2, result.stage.Successes)
	assert.EqualValues(t, 2, executions.Load())
	assert.EqualValues(t, 0, active.Load())
}

func TestRunStageCancelsImmediatelyWhenArtifactWriteFails(t *testing.T) {
	started := make(chan int64, 2)
	releaseSuccess := make(chan struct{})
	canceled := make(chan struct{}, 1)
	var requests atomic.Int64
	roundTrip := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		requestNumber := requests.Add(1)
		if requestNumber <= 2 {
			started <- requestNumber
		}
		if requestNumber == 1 {
			<-releaseSuccess
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(validStream(fakeInputTokens, fakeOutputTokens))),
				Header:     make(http.Header),
			}, nil
		}
		<-request.Context().Done()
		select {
		case canceled <- struct{}{}:
		default:
		}
		return nil, request.Context().Err()
	})
	runner := &runner{
		cfg: testConfig("https://example.test/v1"), endpoint: "https://example.test/v1/chat/completions",
		client: &http.Client{Transport: roundTrip},
	}
	runner.after = func(time.Duration) <-chan time.Time { return make(chan time.Time) }
	var elapsedCalls atomic.Int64
	runner.stageElapsed = func(time.Time) time.Duration {
		if elapsedCalls.Add(1) == 1 {
			return 7 * time.Millisecond
		}
		return 11 * time.Millisecond
	}
	writeErr := errors.New("artifact disk full")
	type stageResult struct {
		stage stageSummary
		err   error
	}
	completed := make(chan stageResult, 1)
	go func() {
		stage, err := runner.runStage(context.Background(), 2, func(sample) error { return writeErr })
		completed <- stageResult{stage: stage, err: err}
	}()
	<-started
	<-started
	close(releaseSuccess)

	result := <-completed
	assert.ErrorIs(t, result.err, writeErr)
	assert.Equal(t, 7.0, result.stage.SchedulingMS)
	assert.Equal(t, 11.0, result.stage.ElapsedMS)
	<-canceled
}

func TestSummarizeStageUsesNearestRankPercentiles(t *testing.T) {
	samples := []sample{
		{Success: true, HTTPStatus: 200, TTFTMS: 1, CompletionMS: 10, RequestDurationMS: 11},
		{Success: true, HTTPStatus: 200, TTFTMS: 2, CompletionMS: 20, RequestDurationMS: 21},
		{Success: false, HTTPStatus: 429, FailureKind: "http_status", RequestDurationMS: 5},
		{Success: true, HTTPStatus: 200, TTFTMS: 4, CompletionMS: 40, RequestDurationMS: 41},
	}
	report := summarizeStageForTest(300, time.Now(), time.Minute, time.Minute, 300, samples)
	assert.Equal(t, .75, report.SuccessRate)
	assert.Equal(t, 2.0, report.TTFT.P50)
	assert.Equal(t, 4.0, report.TTFT.P90)
	assert.Equal(t, 40.0, report.Completion.P99)
	assert.Equal(t, 41.0, report.AllRequests.P99)
	assert.Equal(t, 3, report.HTTPStatuses["200"])
	assert.Equal(t, 1, report.HTTPStatuses["429"])
}

func TestStageSummaryIncludesKnownUsageCostAndUnknownChargedUsage(t *testing.T) {
	samples := []sample{
		{Success: true, UsageKnown: true, InputTokens: 10, CachedInputTokens: 4, OutputTokens: 1, EstimatedListCostUSD: 0.0000228},
		{Success: false, UsageKnown: true, InputTokens: 5, OutputTokens: 1, EstimatedListCostUSD: 0.00002, FailureKind: "terminal"},
		{Success: false, FailureKind: "transport"},
	}
	report := summarizeStageForTest(300, time.Now(), time.Minute, time.Minute, 300, samples)
	assert.Equal(t, 2, report.Usage.RequestsWithUsage)
	assert.Equal(t, 1, report.Usage.UnknownUsageRequests)
	assert.EqualValues(t, 15, report.Usage.InputTokens)
	assert.EqualValues(t, 4, report.Usage.CachedInputTokens)
	assert.EqualValues(t, 11, report.Usage.UncachedInputTokens)
	assert.EqualValues(t, 2, report.Usage.OutputTokens)
	assert.InDelta(t, 0.0000428, report.Usage.EstimatedListCostUSD, 1e-12)
}

func TestRunAbortsAfterFailedPreflightAndFinalizesSummary(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, validStream(1, 1))
	}))
	defer server.Close()
	cfg := testConfig(server.URL)
	cfg.OutputDir = t.TempDir()
	cfg.RunID = "failed-preflight"

	samplesPath, summaryPath, err := run(context.Background(), cfg)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "preflight failed")
	assert.EqualValues(t, 1, requests.Load())
	_, samplesErr := os.Stat(samplesPath)
	require.NoError(t, samplesErr)
	_, summaryErr := os.Stat(summaryPath)
	require.NoError(t, summaryErr)
}

func TestPreflightOnlyRunsExactlyOneRequestAndFinalizesArtifacts(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, validStream(fakeInputTokens, fakeOutputTokens))
	}))
	defer server.Close()
	cfg := testConfig(server.URL)
	cfg.OutputDir = t.TempDir()
	cfg.RunID = "preflight-only"
	cfg.PreflightOnly = true

	samplesPath, summaryPath, err := run(context.Background(), cfg)

	require.NoError(t, err)
	assert.EqualValues(t, 1, requests.Load())
	samples, err := os.ReadFile(samplesPath)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(strings.TrimSpace(string(samples)), "\n")+1)
	summaryBytes, err := os.ReadFile(summaryPath)
	require.NoError(t, err)
	var summary runSummary
	require.NoError(t, common.Unmarshal(summaryBytes, &summary))
	assert.True(t, summary.Preflight.Success)
	assert.Empty(t, summary.Stages)
	assert.False(t, summary.FinishedAt.IsZero())
}

func TestPaidRunStopsAtHardRequestCapIncludingPreflight(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, validSonnetStream("hi", 10, 0, 1))
	}))
	defer server.Close()
	cfg := sonnetTestConfig(server.URL)
	cfg.OutputDir = t.TempDir()
	cfg.RunID = "request-cap"
	cfg.MaxRequests = 4
	cfg.StageDuration = 2 * time.Minute

	_, summaryPath, err := run(context.Background(), cfg)
	require.NoError(t, err)
	assert.EqualValues(t, 4, requests.Load())
	summaryBytes, err := os.ReadFile(summaryPath)
	require.NoError(t, err)
	var summary runSummary
	require.NoError(t, common.Unmarshal(summaryBytes, &summary))
	assert.Equal(t, "max_requests", summary.StopReason)
	assert.Equal(t, 4, summary.TotalUsage.RequestsWithUsage)
	require.Len(t, summary.Stages, 1)
	assert.Equal(t, 3, summary.Stages[0].Requests)
}

func TestPaidRunStopsOnCostCapAfterPreflight(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, validSonnetStream("hi", 10, 0, 1))
	}))
	defer server.Close()
	cfg := sonnetTestConfig(server.URL)
	cfg.OutputDir = t.TempDir()
	cfg.RunID = "cost-cap"
	cfg.MaxCostUSD = 0.00001

	_, summaryPath, err := run(context.Background(), cfg)
	require.NoError(t, err)
	assert.EqualValues(t, 1, requests.Load())
	summaryBytes, err := os.ReadFile(summaryPath)
	require.NoError(t, err)
	var summary runSummary
	require.NoError(t, common.Unmarshal(summaryBytes, &summary))
	assert.Equal(t, "max_cost_usd", summary.StopReason)
	assert.InDelta(t, 0.00003, summary.TotalUsage.EstimatedListCostUSD, 1e-12)
	assert.Empty(t, summary.Stages)
}

func TestPaidRunReportsUnknownUsageFromFailedPreflight(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	cfg := sonnetTestConfig(server.URL)
	cfg.OutputDir = t.TempDir()
	cfg.RunID = "unknown-usage"

	_, summaryPath, err := run(context.Background(), cfg)
	require.Error(t, err)
	summaryBytes, readErr := os.ReadFile(summaryPath)
	require.NoError(t, readErr)
	var summary runSummary
	require.NoError(t, common.Unmarshal(summaryBytes, &summary))
	assert.Equal(t, "unknown_usage", summary.StopReason)
	assert.Equal(t, 1, summary.TotalUsage.UnknownUsageRequests)
	assert.Empty(t, summary.Stages)
}

func TestSSEReaderRejectsUnterminatedEvent(t *testing.T) {
	_, err := readSSEEvent(bufioReader("data: [DONE]"))
	require.Error(t, err)
	assert.ErrorIs(t, err, errMalformedSSE)
}

func bufioReader(value string) *bufio.Reader {
	return bufio.NewReader(strings.NewReader(value))
}

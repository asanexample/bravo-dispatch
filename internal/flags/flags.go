// Package flags is Bravo Dispatch's feature-flag client — OpenFeature (ADR-099) evaluated in-process by the
// flagd resolver, fed from the platform flagship service's HTTP sync source. We write no eval engine and no
// SDK: flagd's resolver does the evaluation (incl. flagship's JsonLogic targeting / fractional rollouts), and
// the OpenFeature OTel hook stamps feature_flag.key / feature_flag.variant onto the active request span — so a
// flag decision shows up directly in the dispatch trace. Fail-static: if flagship is unreachable the resolver
// keeps the last-known set (or serves in-code defaults on cold start), never blocking dispatch.
//
// Ported near-verbatim from alpha-shop's internal/flags (same shape, ADR-099) — this is the first Bravo
// Dispatch consumer of the flagship sync pattern (enable-priority-routing, evaluated by dispatch-worker).
package flags

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	flagdsync "github.com/open-feature/flagd/core/pkg/sync"
	othooks "github.com/open-feature/go-sdk-contrib/hooks/open-telemetry/pkg"
	flagd "github.com/open-feature/go-sdk-contrib/providers/flagd/pkg"
	"github.com/open-feature/go-sdk/openfeature"
)

// Setup registers the flagd in-process provider (fed by flagship's HTTP sync source at syncURL) + the OTel
// traces hook, and returns an OpenFeature client. When syncURL is empty (local dev) no provider is set, so
// evaluations return their in-code defaults. Non-blocking: the service starts even if flagship is down (evals
// return defaults until the first sync lands — fail-static, ADR-099 D3).
func Setup(ctx context.Context, syncURL string) (*openfeature.Client, error) {
	openfeature.AddHooks(othooks.NewTracesHook())
	if syncURL == "" {
		return openfeature.NewClient("bravo-dispatch"), nil
	}
	syncer := &httpSyncer{url: syncURL, interval: 15 * time.Second, client: &http.Client{Timeout: 5 * time.Second}}
	p, err := flagd.NewProvider(
		flagd.WithInProcessResolver(),
		flagd.WithCustomSyncProviderAndUri(syncer, syncURL),
	)
	if err != nil {
		return nil, err
	}
	// SetProvider (not …AndWait): a flagship outage must not block startup.
	if err := openfeature.SetProvider(p); err != nil {
		return nil, err
	}
	return openfeature.NewClient("bravo-dispatch"), nil
}

// EvalContext builds an evaluation context keyed on the given targeting key — flagship's `fractional` rollout
// ops bucket on this targetingKey, so a percentage rollout is sticky per key (e.g. per shipment).
func EvalContext(targetingKey string) openfeature.EvaluationContext {
	return openfeature.NewEvaluationContext(targetingKey, map[string]any{})
}

// --- flagship HTTP sync source → flagd DataSync (implements flagd/core sync.ISync) ---

type httpSyncer struct {
	url      string
	interval time.Duration
	client   *http.Client
	ready    atomic.Bool
	last     string
}

func (s *httpSyncer) Init(context.Context) error { return nil }
func (s *httpSyncer) IsReady() bool              { return s.ready.Load() }

func (s *httpSyncer) fetch(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return "", err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(b), err
}

// emit fetches the flag-set and pushes it (unchanged) as a DataSync snapshot when it has changed.
func (s *httpSyncer) emit(ctx context.Context, out chan<- flagdsync.DataSync) {
	data, err := s.fetch(ctx)
	if err != nil {
		return // transient — the resolver keeps the last-good snapshot (fail-static)
	}
	s.ready.Store(true)
	if data == s.last {
		return
	}
	s.last = data
	select {
	case out <- flagdsync.DataSync{FlagData: data, Source: s.url}:
	case <-ctx.Done():
	}
}

func (s *httpSyncer) Sync(ctx context.Context, out chan<- flagdsync.DataSync) error {
	s.emit(ctx, out) // initial snapshot → marks the provider ready
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			s.emit(ctx, out)
		}
	}
}

func (s *httpSyncer) ReSync(ctx context.Context, out chan<- flagdsync.DataSync) error {
	s.last = ""
	s.emit(ctx, out)
	return nil
}

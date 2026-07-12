# bravo-dispatch

Team `bravo`'s **Bravo Dispatch** — a last-mile parcel-tracking demo product, scaffolded from the platform's
**New Product** shape (ADR-067 v3) and grown by hand past its first service, the same way `alpha-shop` did.
It exists to give a customer somewhere a believable reason to type in a tracking number and watch a parcel
move — and, along the way, to prove out platform capability (a second flagship-pattern edge service,
DynamoDB-backed internal services, self-service SQS/SNS, feature flags, and a Beyla-only non-Go service)
beyond what `alpha-shop` already covers.

## What's here (P1–P3)

| Path | Purpose |
|------|---------|
| `cmd/tracker`, `web/` | **tracker** — the edge BFF + embedded React SPA. The ONLY edge-exposed service: one Kyverno-compliant image serves both the SPA (Vite-built, `go:embed`'d under the `tracker` build tag) and the BFF API it calls same-origin. The SPA is a tracking-number lookup with a live status timeline — no router library, just a state/hash-based view switch between the landing page and a shipment's detail view. |
| `cmd/shipments`, `internal/shipments` | **shipments** — the internal, DynamoDB-backed shipment record store. No HTTPRoute; only reachable east-west, from `tracker` (lookups), `intake` (create), and `dispatch-worker` (status updates). Seeds a small set of obviously-fictional demo shipments on startup. |
| `cmd/intake` | **intake** — the shipment-creation orchestrator. Internal only, no AWS resources of its own: validates a create request, calls `shipments` to persist it, then calls `dispatch-worker` to route it, and returns the created shipment. Modeled on `tracker`'s BFF shape, minus the SPA. |
| `cmd/dispatch-worker`, `internal/carrier` | **dispatch-worker** — owns its own self-service SQS queue (`requests`) AND SNS topic (`events`), and is BOTH the producer (its `POST /dispatch` handler enqueues) and the consumer (a background goroutine long-polls) of that one queue. Per message: evaluates the `enable-priority-routing` feature flag (`internal/flags`, flagship-fed OpenFeature/flagd), picks a simulated carrier/route (`internal/carrier`), advances the shipment's status in `shipments`, publishes a proof-of-capability event to its SNS topic, and delivers the real fan-out notification to `notify`. |
| `services/notify` | **notify** — the fleet's first Node.js/TypeScript service, and its Beyla (eBPF) auto-instrumentation reference: deliberately **no OpenTelemetry SDK code at all**. A small Express app (`POST /events`) that logs a couple of simulated downstream-integration lines. |
| `internal/telemetry` | Shared OTel traces + metrics + Pyroscope profiling + trace-stamped structured logs (ADR-077/ADR-100 golden path), ported from `alpha-shop`. Every Go service uses it; `notify` deliberately does not. |
| `internal/awskv` | The self-service DynamoDB key-value wrapper (ADR-073), ported verbatim — generic across every product that uses it. |
| `internal/awsqueue` | The self-service SQS wrapper (ADR-073): send + long-poll receive/delete for a single owning service. Falls back to a real in-memory queue (not a bare no-op) when unconfigured, so `dispatch-worker`'s producer+consumer loop works locally without AWS. |
| `internal/awssns` | The self-service SNS wrapper (ADR-073), mirroring `awsqueue`'s `Open` shape. No-op when unconfigured — nothing currently subscribes to the topic it publishes to (see `cmd/dispatch-worker`'s comment). |
| `internal/flags` | Feature flags via OpenFeature + the in-process flagd resolver, fed by the platform flagship service's HTTP sync source (ADR-099) — ported near-verbatim from `alpha-shop`. |
| `internal/shipmentsclient` | The typed, trace-propagating HTTP client for `shipments`, shared by every caller (`tracker`, `intake`, `dispatch-worker`) — the thing that makes a request one connected trace end to end. |
| `k8s/base/` + `k8s/overlays/<stage>/` | Namespace-/host-agnostic `base/` + thin per-stage overlays (`dev`/`test`/`uat`/`staging`/`prod`). The per-Product ApplicationSet syncs `k8s/overlays/<stage>`, injecting the namespace + host; `deploy.yml` pins the dev overlay's image digest per service (promotion to other stages is by PR). |
| `.github/workflows/` | `deploy.yml`/`preview.yml` (thin callers of `asanexample/trusted-ci`, one build→provenance→(promote) chain per service), `validate.yml` (overlay/ns guards + Go + Node unit tests), `security.yml` (Trivy + Semgrep). `dependabot.yml` keeps deps + base images current (`gomod`, `npm` for `services/notify`, `github-actions`, `docker`). |

### Why no shared SQS/SNS between services

Every self-service AWS resource this platform's Crossplane Composition provisions (`sqs`/`sns`/`dynamodb`/`s3`)
is scoped to exactly **one** owning `(service, resourceName)` pair — there's no built-in mechanism for two
services to share a physical queue/topic, and no SNS→SQS auto-subscription wiring. So every cross-service
handoff here is plain internal HTTP (the same trace-propagating east-west pattern `tracker`→`shipments` uses),
and `dispatch-worker` is both the producer and the consumer of its own queue — the only way one service gets
async decoupling without needing a second owner. `dispatch-worker`'s SNS publish is a deliberate
proof-of-capability, not a delivery mechanism: `notify` is reached directly over HTTP, not via the topic.

## How the supply chain works

`deploy.yml` is a few small jobs per service that call shared, app-team-unwritable reusable workflows:

1. **build** → `trusted-ci/build-sign.yml` — builds the image, pushes it to the product-scoped repo
   (`team-bravo/dispatch-<service>`) in the platform ECR (via the per-Product OIDC role
   `github-actions-ecr-push-product-bravo-dispatch`), cosign-keyless-signs it, attaches a CycloneDX SBOM.
2. **provenance** → `trusted-ci/slsa-provenance.yml` — attaches the SLSA build provenance (SLSA Build L3).
3. **promote** → `trusted-ci/promote.yml` — records the freshly signed digest into the platform control-plane
   Release (`gitops/releases/bravo/dispatch/dev.yaml`, keyed by service); the per-Product ApplicationSet syncs
   it. Promotion to test/uat/staging/prod is by PR (`promote.yml`'s `workflow_dispatch`).

Signatures, SBOM, and provenance carry this repo's identity (the `githubWorkflowRepository` cert extension),
which the platform's Kyverno `verify-images-product` / `verify-attestations-product` policies require at
admission. Nothing per-app to maintain — it lives in `trusted-ci`.

## Conventions (enforced by platform policy)

- **Do not** hardcode a hostname or namespace — the platform injects both (the ApplicationSet sets the
  destination namespace and patches the real host onto `tracker`'s `HTTPRoute`). Leave the
  `placeholder.invalid` host and the namespace-agnostic `base/`.
- A new Service for this product → add `k8s/base/<service>/` + its image; a new Stage/Environment → author
  `gitops/environments/bravo/dispatch/<stage>.yaml` via the New Environment portal template.
- All shipment data (names, addresses, tracking numbers) is fabricated for the demo — never real customer PII.

The team and product are registered in the platform repo — the `gitops/products/bravo/dispatch.yaml` registry
entry and the `dev` Environment claim (`gitops/environments/bravo/dispatch/dev.yaml`) — separately from this
repo. See `docs/runbooks/app-supply-chain-onboarding.md` in the platform repo.

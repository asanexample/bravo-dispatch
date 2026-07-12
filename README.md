# bravo-dispatch

Team `bravo`'s **Bravo Dispatch** — a last-mile parcel-tracking demo product, scaffolded from the platform's
**New Product** shape (ADR-067 v3) and grown by hand past its first service, the same way `alpha-shop` did.
It exists to give a customer somewhere a believable reason to type in a tracking number and watch a parcel
move — and, along the way, to prove out platform capability (a second flagship-pattern edge service,
DynamoDB-backed internal services, SNS/SQS fan-out in later phases) beyond what `alpha-shop` already covers.

## What's here (Phase A / P1)

| Path | Purpose |
|------|---------|
| `cmd/tracker`, `web/` | **tracker** — the edge BFF + embedded React SPA. The ONLY edge-exposed service: one Kyverno-compliant image serves both the SPA (Vite-built, `go:embed`'d under the `tracker` build tag) and the BFF API it calls same-origin. The SPA is a tracking-number lookup with a live status timeline — no router library, just a state/hash-based view switch between the landing page and a shipment's detail view. |
| `cmd/shipments`, `internal/shipments` | **shipments** — the internal, DynamoDB-backed shipment record store. No HTTPRoute; only reachable east-west from `tracker`. Seeds a small set of obviously-fictional demo shipments on startup. |
| `internal/telemetry` | Shared OTel traces + metrics + Pyroscope profiling + trace-stamped structured logs (ADR-077/ADR-100 golden path), ported from `alpha-shop`. |
| `internal/awskv` | The self-service DynamoDB key-value wrapper (ADR-073), ported verbatim — generic across every product that uses it. |
| `internal/shipmentsclient` | `tracker`'s typed, trace-propagating HTTP client for `shipments` — the thing that makes a page load one connected trace. |
| `k8s/base/` + `k8s/overlays/<stage>/` | Namespace-/host-agnostic `base/` + thin per-stage overlays (`dev`/`test`/`uat`/`staging`/`prod`). The per-Product ApplicationSet syncs `k8s/overlays/<stage>`, injecting the namespace + host; `deploy.yml` pins the dev overlay's image digest per service (promotion to other stages is by PR). |
| `.github/workflows/` | `deploy.yml`/`preview.yml` (thin callers of `asanexample/trusted-ci`, one build→provenance→(promote) chain per service), `validate.yml` (overlay/ns guards + unit tests), `security.yml` (Trivy + Semgrep). `dependabot.yml` keeps deps + base images current. |

## What's coming (later phases, not in this repo yet)

- **P2 — Ship**: `intake` (public create-shipment API, SQS producer) + `dispatch-worker` (consumes the intake
  queue, assigns a route, publishes a status-changed event to SNS, and checks a flagship
  `enable-priority-routing` flag). First cross-service async trace in the fleet.
- **P3 — Notify**: `notify` — Node.js/TypeScript, consumes its own SQS queue subscribed to the SNS topic, fans
  out to a couple of simulated downstreams. Deliberately **no OTel SDK** — the fleet's reference proof that
  Beyla's eBPF baseline (ADR-100 L0) works on a non-Go, non-instrumented workload, not just that Beyla is
  excluded where an SDK is present.

Each phase lands as its own pass, the same way `alpha-shop` shipped its storefront before cart/orders.

## How the supply chain works

`deploy.yml` is a few small jobs per service that call shared, app-team-unwritable reusable workflows:

1. **build** → `trusted-ci/build-sign.yml` — builds the image, pushes it to the product-scoped repo
   (`team-bravo/dispatch-tracker` / `team-bravo/dispatch-shipments`) in the platform ECR (via the per-Product
   OIDC role `github-actions-ecr-push-product-bravo-dispatch`), cosign-keyless-signs it, attaches a
   CycloneDX SBOM.
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

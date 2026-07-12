# dispatch-worker — internal Go service (producer+consumer of its own SQS queue + SNS topic). Static
# distroless-nonroot image (Kyverno-compliant). Context = repo root.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/dispatch-worker ./cmd/dispatch-worker

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/dispatch-worker /dispatch-worker
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/dispatch-worker"]

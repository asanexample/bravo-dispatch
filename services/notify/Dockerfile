# notify — Bravo Dispatch's fleet reference for the platform's Beyla (eBPF) auto-instrumentation baseline
# (ADR-100 L0). Deliberately the only non-Go service and the only one with NO OpenTelemetry SDK code — see
# src/app.ts. Multi-stage: a build stage with devDependencies (tsc) and TS source, and a separate minimal
# non-root runtime stage with only the compiled JS + prod deps (no devDependencies, no TS source). Build
# context is the repo root; dockerfile is services/notify/Dockerfile.

# 1) Install deps (incl. dev) + compile TypeScript -> dist/.
FROM node:22-alpine AS build
WORKDIR /app
COPY services/notify/package.json services/notify/package-lock.json ./
RUN npm ci
COPY services/notify/tsconfig.json ./
COPY services/notify/src ./src
RUN npm run build

# 2) Prod-only node_modules, installed fresh (never copied from the build stage — no devDependencies leak in).
FROM node:22-alpine AS prod-deps
WORKDIR /app
COPY services/notify/package.json services/notify/package-lock.json ./
RUN npm ci --omit=dev

# 3) Minimal non-root runtime: compiled JS + prod deps only, no TS source, no devDependencies, no npm/tsc.
FROM node:22-alpine
WORKDIR /app
ENV NODE_ENV=production
COPY --from=prod-deps /app/node_modules ./node_modules
COPY --from=build /app/dist ./dist
COPY services/notify/package.json ./
EXPOSE 8080
USER node
ENTRYPOINT ["node", "dist/server.js"]

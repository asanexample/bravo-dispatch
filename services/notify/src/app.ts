// The notify HTTP app — factored out from server.ts so tests can exercise it directly (supertest) without
// binding a real port.
//
// notify is Bravo Dispatch's fleet reference for the platform's Beyla (eBPF) auto-instrumentation baseline
// (ADR-100 L0): it deliberately carries NO OpenTelemetry SDK code at all. Every other service in this fleet is
// hand-instrumented (internal/telemetry); this one exists to prove the zero-touch fallback actually works on a
// non-Go, un-instrumented workload — not just that Beyla is excluded where an SDK is present.
import express, { type Express, type Request, type Response } from "express";

export function createApp(): Express {
  const app = express();
  app.use(express.json());

  app.get("/healthz", (_req: Request, res: Response) => {
    res.status(200).json({ status: "ok" });
  });

  // Called by dispatch-worker (east-west) once it has picked a carrier and advanced a shipment's status —
  // this is the fleet's real fan-out delivery mechanism (the SNS publish dispatch-worker also does is a
  // separate proof-of-capability, not consumed here or anywhere else yet).
  app.post("/events", (req: Request, res: Response) => {
    const { shipmentId, status, carrier } = req.body ?? {};

    // Simulated downstream integrations — no real email/webhook provider is wired up for this demo. Logged as
    // structured-ish JSON lines so they're greppable in Loki (Beyla gives us the HTTP span; these lines are
    // the app-level signal that *something* happened inside it).
    console.log(JSON.stringify({ event: "customer_notified", channel: "email", shipmentId, status, carrier }));
    console.log(JSON.stringify({ event: "carrier_webhook_sent", shipmentId, carrier }));

    res.status(200).json({ status: "ok" });
  });

  return app;
}

import { useEffect, useState } from "react";
import { ShipmentTimeline } from "./ShipmentTimeline";

// ---- BFF response shapes (mirror internal/shipments.Shipment's JSON tags) ----

export interface TimelineEvent {
  status: string;
  label: string;
  at: string;
}

export interface Shipment {
  id: string;
  recipient: string;
  origin: string;
  destination: string;
  carrier: string;
  status: string;
  currentLocation: string;
  eta: string;
  timeline: TimelineEvent[];
}

// A handful of obviously-fictional tracking numbers shown on the landing page BEFORE the /api/shipments
// fetch resolves (and as a fallback if it never does) — so first load is never a dead empty box.
const FALLBACK_SAMPLE_IDS = ["BD-10023", "BD-10041", "BD-10077"];

// SampleLink is what the landing page's sample buttons render: always an id, enriched with the rest once
// /api/shipments resolves.
type SampleLink = Pick<Shipment, "id"> & Partial<Omit<Shipment, "id">>;

function sampleLinks(samples: Shipment[] | null): SampleLink[] {
  return samples ?? FALLBACK_SAMPLE_IDS.map((id) => ({ id }));
}

async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(path, { headers: { Accept: "application/json" } });
  if (!res.ok) {
    let detail = "";
    try {
      const body = (await res.json()) as { error?: string };
      detail = body?.error ?? "";
    } catch {
      /* ignore parse errors */
    }
    throw new Error(detail || `Request failed (${res.status}).`);
  }
  return (await res.json()) as T;
}

// Hash-based "routing" (#/BD-10023) — enough for a two-screen demo without pulling in a router library.
function useTrackingIdFromHash(): [string, (id: string) => void] {
  const parse = () => decodeURIComponent(window.location.hash.replace(/^#\/?/, ""));
  const [id, setId] = useState(parse);

  useEffect(() => {
    const onHashChange = () => setId(parse());
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, []);

  const navigate = (next: string) => {
    window.location.hash = next ? `/${encodeURIComponent(next)}` : "";
  };
  return [id, navigate];
}

export default function App() {
  const [trackingId, navigate] = useTrackingIdFromHash();

  return (
    <div className="page">
      <header className="topbar">
        <a
          className="brand"
          href="#/"
          onClick={(e) => {
            e.preventDefault();
            navigate("");
          }}
        >
          <span className="brand__mark" aria-hidden="true">
            ▲
          </span>
          Bravo Dispatch
        </a>
        <span className="topbar__tag eyebrow">Last-mile tracking demo</span>
      </header>

      <main className="container">
        {trackingId ? (
          <ShipmentDetail id={trackingId} onBack={() => navigate("")} />
        ) : (
          <Landing onTrack={navigate} />
        )}
      </main>

      <footer className="footer">
        <span>Bravo Dispatch — a demo product. All shipments, names, and addresses are fictional.</span>
      </footer>
    </div>
  );
}

// ---- Landing: tracking-number input + sample links ----

function Landing({ onTrack }: { onTrack: (id: string) => void }) {
  const [input, setInput] = useState("");
  const [samples, setSamples] = useState<Shipment[] | null>(null);
  const [sampleError, setSampleError] = useState(false);

  useEffect(() => {
    let cancelled = false;
    getJSON<{ shipments: Shipment[] }>("/api/shipments")
      .then((data) => {
        if (!cancelled) setSamples(data.shipments);
      })
      .catch(() => {
        if (!cancelled) setSampleError(true);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  function submit(e: React.FormEvent) {
    e.preventDefault();
    const id = input.trim().toUpperCase();
    if (id) onTrack(id);
  }

  return (
    <div className="landing">
      <section className="hero">
        <p className="eyebrow">Track a parcel</p>
        <h1 className="hero__title">Where's my shipment?</h1>
        <p className="hero__lede">
          Enter a Bravo Dispatch tracking number to see its live status timeline and estimated
          delivery.
        </p>
        <form className="track-form" onSubmit={submit}>
          <input
            className="track-form__input"
            type="text"
            inputMode="text"
            placeholder="e.g. BD-10023"
            value={input}
            onChange={(e) => setInput(e.target.value)}
            aria-label="Tracking number"
          />
          <button className="btn" type="submit" disabled={!input.trim()}>
            Track
          </button>
        </form>
      </section>

      <section className="samples">
        <p className="eyebrow">Try a demo shipment</p>
        <ul className="samples__list">
          {sampleLinks(samples).map((s) => (
            <li key={s.id}>
              <button className="sample-card" type="button" onClick={() => onTrack(s.id)}>
                <span className="sample-card__id mono">{s.id}</span>
                <span className="sample-card__meta">
                  {s.recipient && s.destination
                    ? `${s.recipient} · ${s.destination}`
                    : "Loading details…"}
                </span>
                {s.status && (
                  <span className="tag sample-card__status">{s.status.replace(/_/g, " ")}</span>
                )}
              </button>
            </li>
          ))}
        </ul>
        {sampleError && (
          <p className="samples__note">
            Couldn't reach the shipments service for live details — showing sample tracking
            numbers only.
          </p>
        )}
      </section>
    </div>
  );
}

// ---- Shipment detail: status timeline + current location/ETA ----

function ShipmentDetail({ id, onBack }: { id: string; onBack: () => void }) {
  const [shipment, setShipment] = useState<Shipment | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    setShipment(null);
    getJSON<Shipment>(`/api/shipments/${encodeURIComponent(id)}`)
      .then((data) => {
        if (!cancelled) setShipment(data);
      })
      .catch((err: Error) => {
        if (!cancelled) setError(err.message);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [id]);

  return (
    <div className="detail">
      <button className="btn btn--ghost back-link" type="button" onClick={onBack}>
        ← Track another shipment
      </button>

      {loading && (
        <div className="state">
          <p className="state__title">Looking up {id}…</p>
        </div>
      )}

      {!loading && error && (
        <div className="state state--error">
          <p className="state__title">Couldn't find that shipment</p>
          <p className="state__body">{error}</p>
        </div>
      )}

      {!loading && shipment && (
        <>
          <section className="detail__head">
            <p className="eyebrow mono">{shipment.id}</p>
            <h1 className="detail__title">{shipment.recipient}</h1>
            <p className="detail__route">
              {shipment.origin} → {shipment.destination}
            </p>
            <span className="tag detail__carrier">{shipment.carrier}</span>
          </section>

          <section className="readout">
            <div className="readout__item">
              <span className="eyebrow">Current location</span>
              <span className="readout__value">{shipment.currentLocation}</span>
            </div>
            <div className="readout__item">
              <span className="eyebrow">
                {shipment.status === "delivered" ? "Delivered" : "Estimated delivery"}
              </span>
              <span className="readout__value mono">
                {new Date(shipment.eta).toLocaleString(undefined, {
                  weekday: "short",
                  month: "short",
                  day: "numeric",
                  hour: "numeric",
                  minute: "2-digit",
                })}
              </span>
            </div>
          </section>

          <section className="timeline-section">
            <p className="eyebrow">Status timeline</p>
            <ShipmentTimeline shipment={shipment} />
          </section>
        </>
      )}
    </div>
  );
}

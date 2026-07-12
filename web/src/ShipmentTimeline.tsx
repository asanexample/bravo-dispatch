import type { Shipment } from "./App";

// The five stages every parcel passes through, in order. The backend only ever emits an Event for a stage
// once it's actually happened (see internal/shipments' fixed demo data), so a stage with no matching
// timeline entry is simply upcoming — not an error.
const STAGES: { status: string; label: string }[] = [
  { status: "created", label: "Created" },
  { status: "picked_up", label: "Picked up" },
  { status: "in_transit", label: "In transit" },
  { status: "out_for_delivery", label: "Out for delivery" },
  { status: "delivered", label: "Delivered" },
];

function formatTimestamp(iso: string): string {
  return new Date(iso).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}

export function ShipmentTimeline({ shipment }: { shipment: Shipment }) {
  const currentIndex = STAGES.findIndex((s) => s.status === shipment.status);

  return (
    <ol className="timeline">
      {STAGES.map((stage, i) => {
        const event = shipment.timeline.find((e) => e.status === stage.status);
        const done = currentIndex >= 0 && i <= currentIndex;
        return (
          <li
            key={stage.status}
            className={`timeline__step${done ? " timeline__step--done" : ""}`}
          >
            <span className="timeline__dot" aria-hidden="true" />
            <div className="timeline__body">
              <span className="timeline__label">{stage.label}</span>
              <span className="timeline__time">
                {event ? formatTimestamp(event.at) : "Not yet"}
              </span>
            </div>
          </li>
        );
      })}
    </ol>
  );
}

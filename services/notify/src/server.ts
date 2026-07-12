// Command notify's entry point — binds the Express app from app.ts to ADDR/PORT and handles graceful
// shutdown, mirroring the fleet's Go services' drain behavior (ADR-085) even though this one has no Go
// runtime to share the pattern with.
import { createApp } from "./app.js";

const port = Number(process.env.PORT ?? process.env.ADDR?.replace(/^:/, "") ?? 8080);
const app = createApp();

const server = app.listen(port, () => {
  console.log(JSON.stringify({ event: "starting", service: "dispatch-notify", port }));
});

function shutdown(signal: string): void {
  console.log(JSON.stringify({ event: "shutting_down", signal }));
  server.close((err) => {
    if (err) {
      console.error(JSON.stringify({ event: "shutdown_error", error: String(err) }));
      process.exit(1);
    }
    console.log(JSON.stringify({ event: "stopped" }));
    process.exit(0);
  });
}

process.on("SIGINT", () => shutdown("SIGINT"));
process.on("SIGTERM", () => shutdown("SIGTERM"));

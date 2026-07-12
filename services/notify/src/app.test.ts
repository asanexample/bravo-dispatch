import { describe, expect, it } from "vitest";
import request from "supertest";
import { createApp } from "./app.js";

describe("notify app", () => {
  it("GET /healthz returns ok", async () => {
    const res = await request(createApp()).get("/healthz");
    expect(res.status).toBe(200);
    expect(res.body).toEqual({ status: "ok" });
  });

  it("POST /events accepts a status-change payload and returns ok", async () => {
    const res = await request(createApp())
      .post("/events")
      .send({ shipmentId: "BD-10023", status: "picked_up", carrier: "Bravo Dispatch Priority Air" })
      .set("Content-Type", "application/json");

    expect(res.status).toBe(200);
    expect(res.body).toEqual({ status: "ok" });
  });

  it("POST /events tolerates a missing body", async () => {
    const res = await request(createApp()).post("/events").set("Content-Type", "application/json");
    expect(res.status).toBe(200);
  });
});

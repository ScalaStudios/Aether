import { connection } from "next/server"

export async function GET(req: Request) {
  await connection()
  const upstream = await fetch(`${process.env.AETHER_API_URL ?? "http://localhost:8080"}/api/v1/events`, {
    headers: {
      accept: "text/event-stream",
      cookie: req.headers.get("cookie") ?? "",
    },
    cache: "no-store",
    signal: req.signal,
  })
  return new Response(upstream.body, {
    status: upstream.status,
    headers: {
      "Content-Type": upstream.headers.get("Content-Type") ?? "text/event-stream",
      "Cache-Control": "no-cache, no-transform",
      Connection: "keep-alive",
      "X-Accel-Buffering": "no",
    },
  })
}

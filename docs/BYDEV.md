# Splitwise-QUIC — Architecture & Technology Guide

## What the app does

It's a Splitwise clone — track who paid for what in a group, automatically calculate who owes whom, and let you "settle up." The interesting part is **how** it talks to the browser: everything runs over **HTTP/3 + QUIC** (a modern transport replacing TCP), and live balance updates are pushed as **QUIC DATAGRAM frames** via **WebTransport** — the lowest-latency browser channel available today.

---

## Technology Glossary

### QUIC

**What it is:** A transport protocol developed by Google, now an IETF standard (RFC 9000). It runs over **UDP** instead of TCP.

**Why it matters vs TCP:**

| Problem with TCP | How QUIC fixes it |
|---|---|
| Head-of-line blocking: one lost packet stalls everything | Multiple independent streams — a lost packet only stalls *that* stream |
| 3-way handshake = 1 round-trip before any data | 0-RTT: returning clients send data *in the first packet* |
| Connection tied to IP:port pair | Connection migration: works seamlessly across Wi-Fi ↔ cellular switches |
| TLS is a separate layer on top | TLS 1.3 is *mandatory and built in* — encryption is not optional |

### HTTP/3

**What it is:** HTTP running on top of QUIC instead of TCP. This is "HTTP/3" — every HTTP request/response works exactly the same from the app's perspective, but the transport underneath is QUIC.

**In this app:** The browser makes a normal HTTP request (for the dashboard, adding expenses, etc.) but it travels over QUIC multiplexed streams, not a TCP socket.

### WebTransport

**What it is:** A browser API (like WebSocket, but newer) that lets you open a full-duplex channel to a server *inside an existing QUIC connection*.

**The killer feature here:** It supports **DATAGRAM frames** — fire-and-forget messages with no ordering guarantee, no delivery confirmation, minimal latency. Perfect for "someone added an expense, refresh your view" notifications where dropping one message is fine (you'll get the next one).

**Why not WebSocket?** WebSocket runs over TCP (or HTTP/2). It has head-of-line blocking and no datagrams. WebTransport over QUIC is strictly better for real-time updates.

### TLS 1.3

**What it is:** The encryption standard. QUIC *requires* TLS 1.3 — you cannot run unencrypted QUIC. It's faster than TLS 1.2 (1 round-trip for a new connection, 0 for resumption) and removes older insecure cipher suites.

### Alt-Svc Header

**What it is:** An HTTP header the server sends on *every TCP response*: `Alt-Svc: h3=":4433"; ma=2592000`. It tells the browser "this server also speaks HTTP/3 on port 4433." The browser upgrades automatically on the *next* request.

**The first-load dance:** Browser → TCP (HTTP/1.1 or HTTP/2) → server sends Alt-Svc → Browser's *next* request goes over QUIC (HTTP/3).

### HTMX

**What it is:** A JavaScript library that lets you do AJAX partial-page updates using only HTML attributes (`hx-get`, `hx-post`, `hx-swap`, `hx-trigger`). No React, no JSON API, no client-side routing.

**In this app:** When you add an expense, HTMX posts the form and swaps only the `#expenses` div with fresh HTML from the server. When a WebTransport/SSE event arrives, HTMX triggers a re-fetch of the relevant partial.

### SSE (Server-Sent Events)

**What it is:** A one-way stream from server → browser over a long-lived HTTP connection. The browser subscribes to `/g/{id}/events` and the server pushes `event: update\ndata: ...\n\n` messages whenever something changes.

**Why it exists alongside WebTransport:** SSE is the fallback. If the browser doesn't support WebTransport (Firefox, older browsers), SSE still delivers live updates. HTMX listens to both with `hx-trigger="sse:update"`.

### SQLite (pure-Go via `modernc.org/sqlite`)

**What it is:** A file-based SQL database. No separate database process — the DB is a single `.db` file.

**Why pure-Go matters:** The standard SQLite binding requires cgo (C compiler). `modernc.org/sqlite` is a Go port — no C compiler needed, cross-compiles to any platform, single binary deployment.

**WAL mode:** Write-Ahead Logging — concurrent reads don't block writes and vice versa. Essential here because QUIC multiplexes many streams simultaneously.

### bcrypt

**What it is:** A password hashing function. Intentionally slow to make brute-force attacks expensive. Used in `store/store.go` to hash passwords before storing them.

### Go's `html/template`

**What it is:** Go's standard template engine — generates HTML on the server. Templates are compiled into the binary via `//go:embed` (no external files at runtime).

---

## Architecture: The Layers

```
┌─────────────────────────────────────────────────────────┐
│                    Browser                              │
│  HTMX (partial swaps)  +  WebTransport JS  +  app.js   │
└────────────────┬──────────────────┬────────────────────┘
                 │ HTTPS / HTTP3    │ QUIC DATAGRAM frames
                 ▼                  ▼
┌─────────────────────────────────────────────────────────┐
│              internal/server  (Transport Layer)         │
│                                                         │
│  TCP listener (:4433) ──► HTTP/1.1 + HTTP/2 + Alt-Svc  │
│  UDP listener (:4433) ──► HTTP/3 + QUIC + WebTransport  │
│                                                         │
│  TLS cert: ECDSA P-256, 13-day self-signed (tls.go)     │
│  QUIC config: 0-RTT, datagrams, 512 streams             │
└─────────────────────────┬───────────────────────────────┘
                          │ http.Handler
                          ▼
┌─────────────────────────────────────────────────────────┐
│             internal/handlers  (HTTP Layer)             │
│                                                         │
│  Routes():  http.ServeMux wires all endpoints           │
│  auth():    middleware — reads "sid" cookie → DB lookup │
│  Pages:     dashboard, group page (full HTML)           │
│  Actions:   createExpense, settle, addMember, etc.      │
│  Partials:  HTMX fragment responses (no full page)      │
│  realtime:  SSE stream + WebTransport upgrade           │
│  export:    CSV + PDF generation                        │
└──────┬──────────────────┬──────────────────────────────┘
       │                  │
       ▼                  ▼
┌──────────────┐  ┌───────────────────────────────────┐
│internal/store│  │       internal/realtime             │
│(Data layer)  │  │       (Pub/Sub Hub)                 │
│              │  │                                     │
│ User auth    │  │ Hub{map[topic]→set of channels}     │
│ Groups       │  │                                     │
│ Expenses     │  │ Subscribe(topic) → chan Event       │
│ Balances     │  │ Publish(topic, event)               │
│ Comments     │  │                                     │
│ Settlements  │  │ Topics: groupID  → all viewers      │
│ Activities   │  │         "user:X" → one user         │
└──────┬───────┘  └───────────────────────────────────┘
       │
       ▼
┌─────────────────────────────────────────────────────────┐
│                internal/db  (SQLite)                    │
│                                                         │
│  modernc.org/sqlite  (pure Go, no cgo)                  │
│  WAL mode + foreign keys + 5s busy timeout              │
│  Schema: users, sessions, groups, group_members,        │
│          expenses, expense_shares, settlements,          │
│          activities, comments                            │
└─────────────────────────────────────────────────────────┘
       │
       ▼ (pure functions, no DB)
┌─────────────────────────────────────────────────────────┐
│             internal/splits  (Money Math)               │
│                                                         │
│  compute.go:   equal / exact / percentage / shares      │
│  simplify.go:  max-heap debt minimization (O(n log n))  │
│                                                         │
│  All amounts: int64 (minor units / cents)               │
│  Zero floating point: "12.34" → 1234 at input boundary  │
└─────────────────────────────────────────────────────────┘
```

---

## Request Lifecycle: Adding an Expense

```
1. User fills form, clicks "Add expense"
   └─ Browser sends POST /g/{id}/expenses over HTTP/3 (QUIC stream)

2. server/ receives the request on the QUIC listener
   └─ Passes to http.Handler with Alt-Svc header added

3. handlers/ auth middleware
   └─ Reads "sid" cookie → store.UserBySession() → user confirmed

4. handlers/actions.go createExpense()
   ├─ Parses form (description, amount, currency, split_type, who's included)
   ├─ store.CreateExpense() — calls splits.Compute() (pure math), then DB transaction: INSERT expense + INSERT shares
   ├─ store.LogActivity() — logs "Alice added expense Dinner"
   └─ hub.Publish(groupID, event) — fans out to all subscribers

5. realtime hub fires the event
   ├─ SSE handler: writes "event: update\ndata: ...\n\n" to a long-lived HTTP response stream (TCP or QUIC)
   └─ WebTransport handler: session.SendDatagram([]byte(msg)) over QUIC

6. Browser receives update (WebTransport or SSE)
   └─ app.js calls htmx.trigger(el, "sse:update")

7. HTMX sees hx-trigger="sse:update" on #expenses and #balances divs
   ├─ GET /g/{id}/expenses → server renders partial HTML → swaps #expenses
   └─ GET /g/{id}/balances → server renders partial HTML → swaps #balances

8. All other users viewing this group see the update too
   (hub published to all subscribers of that groupID)
```

---

## Real-Time: Two Parallel Channels

Every group page opens **both** simultaneously:

```
Browser                          Server
  │                                │
  ├─── GET /g/{id}/events ────────►│  SSE (HTTP stream over TCP or QUIC)
  │◄── event: update ──────────────┤  Fallback for all browsers
  │                                │
  ├─── GET /g/{id}/wt ────────────►│  WebTransport upgrade
  │◄═══ DATAGRAM frames ═══════════╡  Preferred: QUIC unreliable datagrams
  │                                │
  └─── GET /wt ───────────────────►│  Per-user personal channel
       ◄═══ DATAGRAM frames ════════╡  "You were added to group X"
```

**Why two channels?** SSE is reliable but slightly heavier. WebTransport datagrams are unreliable but near-zero overhead and latency. If WebTransport is available, the JS uses it and ignores SSE for live updates. Both trigger the same HTMX partial refresh, so the behavior is identical.

---

## Dependency Injection (how pieces connect)

`main.go` wires everything in order — each layer only knows about the one below it:

```
db.Open()         → *sql.DB
store.New(db)     → *Store         (data access)
render.New()      → *Renderer      (templates)
realtime.NewHub() → *Hub           (pub/sub)
handlers.New(store, render, hub)   → *Handlers
server.New(cfg, func(srv) { h.Routes(srv) }) → *Server
```

`handlers.New` doesn't receive `*server.Server` directly (circular dependency: server needs handler, handler needs server for WebTransport). The `Routes(srv)` call late-wires it — `main.go` passes a closure that resolves both.

---

## Key Design Decisions

| Decision | Why |
|---|---|
| Integer cents for money (`int64`) | Floating-point arithmetic loses pennies. `12.34` → `1234` at the boundary. Rounding remainder distributed via O(n × r) linear partial-max scan (r = leftover cents, typically 0–2). |
| Max-heap debt simplification | Two max-heaps (creditors + debtors). Always pops the true largest pair at every step — including after partial payments. Guarantees ≤ n+m-1 transfers. O(n log n) time, O(n) space. |
| Self-signed ECDSA cert, 13 days | WebTransport's `serverCertificateHashes` browser API only accepts ECDSA certs valid ≤ 14 days. No CA installation needed — the hash is pinned directly in JS. |
| Additive-only DB migrations | `ALTER TABLE` with ignored "duplicate column" errors means migrations are idempotent — safe to run on an already-migrated DB. |
| Embedded assets (`//go:embed`) | Single binary deployment: no external template or static file directories required at runtime. |
| Pure-Go SQLite | No C compiler, no system dependency, cross-compiles to any platform, single static binary. |
| `sync.RWMutex` in Hub | Many concurrent QUIC streams all read (subscribe) but writes (publish) are rare. RWMutex lets all readers run in parallel. Buffer of 16 on subscriber channels so a slow client can't stall the publisher. |

---

## Split & Debt Algorithms (detailed)

### `splits.Compute` — error model

`Compute(total, splitType, inputs)` returns typed sentinel errors. Always use `errors.Is`:

| Sentinel | Trigger |
|---|---|
| `ErrNonPositiveTotal` | `total ≤ 0` |
| `ErrEmptyInputs` | `len(inputs) == 0` |
| `ErrDuplicateUser` | Same `UserID` appears more than once |
| `ErrNegativeValue` | Negative amount / weight / basis-point value |
| `ErrOverflow` | `total × shareWeight` would exceed `int64` |
| `ErrBadSplit` | Exact amounts or percentages don't reconcile to total |
| `ErrInvalidSplitType` | Unrecognised `SplitType` string |

### `proportional()` — O(n × r) remainder distribution

For percentage and shares modes, each participant gets `floor(total × weight / denom)` cents.
The leftover `r = total − Σfloors` cents (always `0 ≤ r < n`) are awarded to the participants
with the largest fractional remainders.

**Previous approach:** `sort.SliceStable` → O(n log n) to distribute ≤ n pennies.  
**Current approach:** linear partial-max scan — iterate the array once per penny:

```
for each leftover cent:
    find index with largest unconsumed remainder  → O(n)
    award +1 cent, mark consumed
```

Total: O(n × r). Since r < n and is typically 0–2, this is O(n) in practice.
Worst case O(n²) only when r = n−1 (all but one penny left), which only occurs when
`total % n == n-1` — negligible for real expense amounts.

### `splits.Simplify` — max-heap greedy (O(n log n))

**Data structure:** `container/heap` max-heap, one for creditors, one for debtors.

**Previous approach:** sort both slices once, then scan with two pointers.  
**Problem with two-pointer:** after a partial payment, the non-exhausted party's reduced
balance may no longer be the largest in its slice — the scan doesn't re-sort.  
**Current approach:** the heap re-orders after every `Push`, so the next `Pop` always
returns the true maximum — even mid-run.

```
heap.Init(creditors)   // O(n) heapify
heap.Init(debtors)     // O(n) heapify
while both heaps non-empty:
    c = heap.Pop(creditors)   // O(log n)
    d = heap.Pop(debtors)     // O(log n)
    pay = min(c.amt, d.amt)
    emit transfer(d → c, pay)
    if c.amt - pay > 0: heap.Push(creditors, c-pay)   // O(log n)
    if d.amt - pay > 0: heap.Push(debtors,   d-pay)   // O(log n)
```

**Transfer count proof:** each iteration exhausts at least one side (whichever is ≤ the other).
Total iterations = (n − 1) + (m − 1) + 1 = n + m − 1 at most. This is the lower bound
for any algorithm that must clear n debtors and m creditors with no debt merging.

**Validation:** zero-balance entries are silently skipped before heap construction.
An imbalanced ledger (Σnet ≠ 0) logs a warning but does not abort.

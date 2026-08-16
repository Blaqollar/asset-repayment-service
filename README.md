# Asset Repayment Service

A REST API that takes a customer payment notification from the bank and applies it to the
outstanding balance of a deployed productive asset, updating that customer's position instantly.

A payment is applied in the request that delivers it — one atomic SQL statement — and the response
carries the customer's complete new position. For load beyond what a database can absorb
synchronously, the same service can route ingest through a durable queue instead.

Go, Postgres, optional Redis. `docker compose up` runs the lot.

---

## Contents

- [Approach](#approach) — what I built and why
- [The core write path](#the-core-write-path)
- [Idempotency](#idempotency)
- [Designing for 100,000 notifications per minute](#designing-for-100000-notifications-per-minute)
- [Architecture](#architecture)
- [Running it](#running-it)
- [API reference](#api-reference)
- [Testing](#testing)
- [What I would do next](#what-i-would-do-next)

---

## Approach
I approached this with these four questions and the answers to those questions are the design. 

### 1. What if the same payment arrives twice?

Banks resend. Networks drop replies. A response that never arrives looks exactly like a request that
was never received, so the bank sends it again — and at this volume that happens every day. Applying
one payment twice would hand a customer an asset they have not finished paying for.

The bank already gives every transfer a unique reference, so that reference *is* the payment's
identity here. The database is told it may only ever hold one row per reference, and refuses the
second. This matters: the guarantee lives in the database rather than in code that has to remember
to check, so it holds even when two copies of the same payment arrive at the very same moment.

### 2. What if two payments for one customer arrive at the same instant?

The obvious approach — look up the balance, add to it, save it back — quietly loses money when two
payments do it at once: both read the same starting figure, and the second overwrites the first.

So the service never reads a balance in order to change it. It tells the database to *add* the
amount, and the database does the arithmetic. Addition doesn't care what order it happens in, so two
payments landing together reach the same total either way. Nothing is lost.

### 3. How do you avoid recording a payment without moving the balance, or the reverse?

A payment is two changes: a line in the ledger, and a new balance. If only one happened, the books
are wrong — and a customer's records would disagree with what they actually owe.

Both changes are given to the database as a single instruction, so they either both happen or
neither does. Even pulling the plug mid-payment cannot leave half of it behind. As a bonus, one
instruction instead of five means one trip to the database per payment, which is most of the reason
the service keeps up at this volume.

### 4. What if payments arrive faster than they can be recorded?

The brief asks for the position to update instantly, so by default it does: the payment is applied
while the bank is still waiting, and the reply carries the customer's new position. At the stated
volume this is comfortably within what one database handles.

But the bank decides when a hundred thousand payments arrive, and any database has a ceiling. Past
it, a service that insists on doing everything immediately has only bad options: make the bank wait
until it gives up and resends, or turn money away.

So there is a second, optional way in. The service checks the payment, puts it in a queue that
survives a restart, and tells the bank "received". Workers then apply payments from that queue as
fast as the database comfortably goes. The customer's position updates a moment later instead of
immediately — which is the trade — so it is **off by default** and turned on with a single setting
when a spike calls for it.

### What shaped these decisions

| Because… | …the service does this |
|---|---|
| Banks resend payments, and copies can arrive simultaneously | Uniqueness is enforced by the database itself, not by application code that could be raced |
| This is money, and mistakes compound | Exact arithmetic throughout, a ledger that is only ever added to, and a rule that money reaching this service is never discarded — an unrecognised customer is filed for someone to look at |
| 100,000/minute reads as a spike, not a steady rate ([the arithmetic](#first-read-the-number-honestly)) | Apply immediately by default, refuse politely when genuinely overloaded, and keep the queue as a lever for spikes |
| If it *were* steady, that is 144 million records a day | The ledger is split across partitions from day one — reorganising it later, with billions of rows in it, is a job nobody wants |
| The brief is in naira, but the code shouldn't care | The currency and its decimal places are data attached to each deployment, so other currencies work without changes |
| The bank sends amounts as text, and times without a timezone | Amounts are read exactly, never through the kind of number that rounds; the bank's timezone is stated in configuration rather than assumed |
| Anyone who could reach this endpoint could clear someone's debt | Every payment is cryptographically signed by the sender, and the service refuses to start in production without that configured |

---

## The core write path

**Default (synchronous).**
`POST /api/v1/payments` → validate → resolve deployment (cache) → one atomic statement → `200` with
the customer's complete new position.

**With `QUEUE_ENABLED=true`.**
`POST /api/v1/payments` → validate → enqueue → `202`.
Worker → resolve deployment (cache) → *the same atomic statement* → balance moved.

The write itself, its idempotency and its concurrency behaviour are identical on both paths. The
queue changes *when* the statement runs, not what it does.

```sql
WITH moved AS (
    UPDATE deployments d
       SET amount_paid   = d.amount_paid + $amount,          -- pure addition: race-free
           payment_count = d.payment_count + 1,
           status = CASE WHEN d.amount_paid + $amount >= d.principal
                         THEN 'completed' ELSE d.status END
     WHERE d.id = $deployment_id
       AND NOT EXISTS (SELECT 1 FROM payments WHERE transaction_reference = $ref)
    RETURNING …
),
split AS MATERIALIZED (
    SELECT m.*, LEAST($amount, GREATEST(m.principal - (m.amount_paid - $amount), 0))
                AS applied_amount                            -- applied vs. overpayment credit
      FROM moved m
),
recorded AS (
    INSERT INTO payments (…, applied_amount, excess_amount, balance_before, balance_after, …)
    SELECT …, s.applied_amount, $amount - s.applied_amount,
           s.amount_paid - $amount, s.amount_paid
      FROM split s
    RETURNING …
)
SELECT … FROM recorded CROSS JOIN split;
```

| Case | What happens |
|---|---|
| New reference | `NOT EXISTS` passes, balance moves, ledger row written, one row returned |
| Replay minutes later | `NOT EXISTS` fails, nothing is inserted, **zero rows** → the stored original result is replayed |
| Two identical notifications racing | Both pass the probe; the loser's `INSERT` violates the primary key and **its balance update rolls back with it** |

---

## Idempotency

A payment must never be applied twice. Four layers enforce that, and only the last one is
authoritative:

1. **`PRIMARY KEY (transaction_reference)`** on the ledger. Everything else is an optimisation in
   front of it. Because `payments` is hash-partitioned on that column, the guarantee is global
   rather than per-partition.
2. **The `NOT EXISTS` probe** inside the write, which makes the ordinary replay cheap: the statement
   affects zero rows instead of raising a constraint error.
3. **The result cache** (`reference → settled outcome`), so a provider retry storm costs a Redis
   `GET` rather than a write transaction on the primary. Best-effort; a miss falls through.
4. **Conflict detection on replay.** A reference identifies one request, not one customer and not
   one amount. Reusing it with different details returns **409** rather than replaying somebody
   else's figures — checked on both the cache and the database paths.

| Scenario | Answer |
|---|---|
| Identical notification, sent again | `200 duplicate`, original result replayed, balance unchanged |
| 40 identical notifications, concurrently | Exactly one applies; the rest replay |
| Same reference, different customer | `409` with `customer_id` detail |
| Same reference, different amount or currency | `409` with the offending field |
| Redelivered by the queue (at-least-once) | Applied once; the constraint turns the repeat into a replay |
| Retried after an unmatched or ignored outcome | `duplicate`, counted once in reconciliation |

The queue's delivery guarantee is deliberately the cheap one. Exactly-once machinery would buy
nothing when the consumer is idempotent.

---

## Architecture

```
┌──────────────────────────── infrastructure ────────────────────────────┐
│  handlers/http  request in, usecase call, status code out              │
│  pkg/router     the route table and each route's middleware            │
│  pkg/middleware request id, recovery, metrics, body limit, HMAC,       │
│                 timeout, load shedding                                 │
│  pkg/response   the one envelope every reply is written through        │
│  queue          Redis stream: producer, consumer group, reclaimer,     │
│                 dead-letter (optional)                                 │
│  database       sqlx over pgx, embedded migrations, repositories       │
│  cache          Redis lookaside, degrades to a no-op                   │
│  observability  Prometheus registry                                    │
│  config         environment-backed, validated at startup               │
└───────────────────────────────┬────────────────────────────────────────┘
                                │  implements domain interfaces
┌───────────────────────────────▼────────────────────────────────────────┐
│                             application                                │
│  usecases  apply_payment, get_position, create_deployment              │
│  dtos      transport shapes; money as strings at the right precision   │
└───────────────────────────────┬────────────────────────────────────────┘
                                │  depends on domain services only
┌───────────────────────────────▼────────────────────────────────────────┐
│                               domain                                   │
│  payment     validation, outcome policy, idempotency rules             │
│  deployment  entity + repayment maths, repository & cache contracts    │
│  pkg/money   exact decimal amounts + currency precision                │
└────────────────────────────────────────────────────────────────────────┘
```

Dependencies point inward. Repository interfaces live in domain packages and their implementations
return those interfaces, so a usecase cannot reach past the domain contract into SQL. The domain
never imports config: what it needs from the environment arrives as its own types
(`payment.Settings`, `deployment.Defaults`).

```
src/
├── main.go                           build the FX graph and run it
├── bootstrap/bootstrap.go            the whole object graph, in one file
├── internal/
│   ├── config/                       env-backed config + validation
│   ├── domain/{deployment,payment}/  entities, services, repository contracts
│   ├── application/{dtos,usecases}/
│   └── infrastructure/
│       ├── handlers/http/            handlers + server lifecycle
│       ├── queue/                    stream producer, worker pool, reclaimer
│       ├── cache/                    Redis lookaside
│       ├── database/{migrations,models,repositories}/
│       └── observability/            Prometheus metrics
├── pkg/{router,middleware,response,money,errors,logger}/
└── tests/{integration,queue}/        full-stack tests on a real Postgres
```

**Conventions worth stating**, because they are choices rather than defaults:

- `BIGSERIAL` internally, UUIDs publicly. `payments.deployment_id` is a foreign key repeated 144M
  times a day; an 8-byte key is half the width of a UUID in the column and every index. `payments`
  goes further and uses `transaction_reference` as its primary key, because a partitioned table's
  unique index must include the partition key — and that constraint *is* the idempotency guarantee.
- sqlx over **pgx**, not lib/pq: sqlx's ergonomics with pgx's binary protocol and statement cache.

---

## Running it

### Docker Compose

```bash
docker compose up --build
```

Postgres, Redis and the service come up together; migrations run on startup.

### Locally

```bash
cd src
cp ../.env.example .env
make run                  # or: go run .
```

Nothing is defaulted in Go code. Settings come from the environment, and the service loads an env
file to populate it — `src/.env` if you have one, otherwise the committed `.env.example`, found by
walking up from the working directory. Real environment variables beat both, and a key set nowhere
is a startup error naming it rather than a silent default.

Postgres has to be running — the service refuses to serve without one rather than reporting itself
healthy and failing every payment. With no Docker, this runs one out of a user-owned directory:

```bash
./scripts/local-postgres.sh start     # initdb, start on :5432, create both databases
./scripts/local-postgres.sh psql
./scripts/local-postgres.sh stop
```

### Try it

```bash
# 1. Register a deployment on the standard terms (1,000,000 over 50 weeks), backdated 10 weeks
curl -s -X POST localhost:8080/api/v1/deployments \
  -H 'Content-Type: application/json' \
  -d '{"customer_id":"GIG00001","asset_id":"AST-001",
       "virtual_account_number":"9012345678",
       "start_date":"'"$(date -v-70d +%F 2>/dev/null || date -d '70 days ago' +%F)"'"}' | jq

# 2. Apply the sample payment payload
curl -s -X POST localhost:8080/api/v1/payments \
  -H 'Content-Type: application/json' \
  -d '{"customer_id":"GIG00001",
       "payment_status":"COMPLETE",
       "transaction_amount":"10000",
       "transaction_date":"2025-11-07 14:54:16",
       "transaction_reference":"VPAY25110713542114478761522000"}' | jq

# 3. Send it again — idempotent, the balance does not move
curl -s -X POST localhost:8080/api/v1/payments \
  -H 'Content-Type: application/json' \
  -d '{"customer_id":"GIG00001","payment_status":"COMPLETE","transaction_amount":"10000",
       "transaction_date":"2025-11-07 14:54:16",
       "transaction_reference":"VPAY25110713542114478761522000"}' | jq '.data.outcome'
# "duplicate"

# 4. Current position
curl -s localhost:8080/api/v1/customers/GIG00001/position | jq
```

### Turning the queue on

```bash
REDIS_ADDRESS=localhost:6379 QUEUE_ENABLED=true QUEUE_WORKERS=16 make run
```

Same endpoint, same payload, same ledger, same idempotency. `POST /payments` starts answering
`202 queued`, and the applied figures show up on the position endpoint a beat later.

---

## API reference

The brief asks for one endpoint. The surface is three, because each read endpoint is load the
payment path competes for:

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/api/v1/payments` | **Apply a payment notification** (the endpoint the bank calls) |
| `POST` | `/api/v1/deployments` | Register an asset deployment — a payment needs one to apply against |
| `GET` | `/api/v1/customers/{customer_id}/position` | Current repayment position |
| `GET` | `/healthz` `/readyz` `/metrics` | Liveness, readiness, Prometheus |

Statements, deployment listings and reference lookups are deliberately absent: easy to add the day
someone needs them, and until then they are index maintenance bought for nothing.

### `POST /api/v1/payments`

```json
{
  "customer_id": "GIG00001",
  "payment_status": "COMPLETE",
  "transaction_amount": "10000",
  "transaction_date": "2025-11-07 14:54:16",
  "transaction_reference": "VPAY25110713542114478761522000"
}
```

`currency` is an optional extra field; without it the configured currency (`PAYMENT_CURRENCY`,
default `NGN`) applies.

```json
{
  "status": "success",
  "message": "payment applied to outstanding balance",
  "request_id": "0f6c…",
  "data": {
    "outcome": "applied",
    "duplicate": false,
    "payment": {
      "transaction_reference": "VPAY25110713542114478761522000",
      "currency": "NGN",
      "amount": "10000.00",
      "amount_applied": "10000.00",
      "excess": "0.00",
      "balance_before": "0.00",
      "balance_after": "10000.00"
    },
    "position": {
      "customer_id": "GIG00001",
      "status": "active",
      "schedule_status": "behind",
      "currency": "NGN",
      "principal": "1000000.00",
      "total_paid": "10000.00",
      "outstanding": "990000.00",
      "excess_credit": "0.00",
      "weekly_due": "20000.00",
      "expected_paid_to_date": "200000.00",
      "arrears": "190000.00",
      "term_weeks": 50,
      "weeks_elapsed": 10,
      "weeks_of_coverage": -9.5,
      "percent_repaid": 1,
      "payment_count": 1,
      "expected_completion_date": "2026-08-06",
      "projected_completion_date": null
    }
  }
}
```

With `QUEUE_ENABLED=true` the same call answers `202` with `"outcome": "queued"` and no position —
the pre-payment balance is not known until a worker reads it, and reporting a figure the service has
not computed would be a guess.

**Status codes.** `200` applied, or a replay of an already-applied notification. `202` accepted but
the balance has not moved: queued, no open deployment matched, or a provider status that is not
settled funds — all terminal, none retryable. `400` malformed payload, with per-field detail.
`401` bad or missing signature. `409` the reference was already used with different details.
`429` at capacity, retry shortly. `500` unexpected — safe to retry, the idempotency key protects the
balance.

### Other design decisions worth knowing

- **Money is an exact decimal, never a float — and never a fixed minor-unit count.** `0.1 + 0.2 !=
  0.3` in IEEE-754, so amounts are `decimal.Decimal` in Go and `NUMERIC(24,6)` in Postgres. An
  integer kobo count would be exact too, but it bakes `100` into the type; here the number of decimal
  places is a property of the currency, so NGN (2dp), JPY (0dp) and KWD (3dp) work unchanged.
- **A materialised balance on an immutable ledger.** `payments` is append-only;
  `deployments.amount_paid` is a running total advanced by the same statement. The position is one
  index lookup and the ledger can always re-derive it. Each row also stores
  `balance_before`/`balance_after` snapshots taken inside the write, so any payment audits on its own.
- **Overpayment becomes credit, not an error.** It keeps the balance update a pure addition, and a
  customer who overpays has credit rather than a rejected transfer.
- **A credit that reached this service is never dropped.** An unknown customer is recorded as
  `unmatched` for reconciliation; an unsettled provider status is recorded as `ignored`. Neither
  moves a balance, and the provider is told not to retry.
- **Only settled funds move a balance.** `PENDING`/`FAILED`/`REVERSED` are recorded, never applied —
  applying a pending credit that later fails hands over an asset nobody paid for.
- **Naive timestamps are read in the provider's timezone** (`PAYMENT_PROVIDER_TIMEZONE`, default
  `Africa/Lagos`); an explicit offset in the payload always wins. Reading West Africa Time as UTC
  would shift payments across week boundaries and corrupt arrears.
- **Payments are authenticated.** HMAC-SHA256 over the verbatim body, compared in constant time.
  `Config.Validate` refuses to start in production without a secret.

---

## Testing

```bash
make test-unit          # domain, money, queue, DI graph — no database required
make test-integration   # full stack against a real Postgres
make test-all
```

`make test-integration` starts a throwaway Postgres with testcontainers. Set `TEST_DATABASE_URL` to
use a database you already have instead, which is what makes the suite runnable without Docker:

```bash
TEST_DATABASE_URL=postgres://$USER@localhost:5432/asset_repayment_test?sslmode=disable \
  make test-integration
```

**Unit tests** cover the FX graph (`fx.ValidateApp` proves every dependency is satisfiable), the
money parser including float-hostile values, the repayment maths (arrears, coverage in weeks,
run-rate projection, the one-instalment delinquency tolerance, fifty instalments settling exactly),
notification validation, and the queue's producer, consumer group, reclaimer and dead-letter paths
against an in-process Redis.

**Integration tests** run the real FX graph and HTTP server against a real Postgres, because the
behaviours that matter are properties of the database and a mocked repository would only prove the
mock behaves as written:

- a payment reducing the balance and returning a correct position;
- the same reference replayed four times applying exactly once;
- **40 concurrent copies** of one notification applying exactly once;
- **50 concurrent distinct** payments to one deployment all landing, ledger and balance in agreement
  (the lost-update proof);
- a reference reused with a different customer or amount rejected with `409`;
- overpayment settling the asset and carrying credit, with the SQL split matching
  `deployment.SplitPayment`;
- fifty weekly instalments settling 1,000,000 exactly, and the settled customer still able to read
  their position;
- unknown customers recorded as unmatched, `PENDING`/`FAILED`/`REVERSED` recorded but not applied,
  malformed payloads rejected with nothing persisted;
- a 0-decimal currency (JPY) end to end, and a fractional yen rejected;
- the asynchronous path end to end: webhook `202`, worker applies, position reflects it, including a
  100-payment burst and a five-times-redelivered notification applying once.

Both suites pass under `-race` against Postgres 16.4.

---

## What I would do next

### Before production

1. **PgBouncer in transaction pooling mode.** The write is a single statement, so it pools perfectly,
   and it is the cheapest way to stop connection count scaling with pod count.
2. **Decide the durability trade explicitly.** `docker-compose.yml` sets `synchronous_commit=off`,
   the biggest write-throughput lever, which means a crash can lose the last ~200ms of commits. For a
   payment ledger that is very likely wrong in production — it should be a decision, not an inherited
   setting.
3. **Emit domain events through a transactional outbox.** A `payment.applied` event drives customer
   SMS, collections and analytics. The ingest queue does not solve this: publishing after the write
   can still lose the event, whereas an outbox row written *inside the same statement* cannot. The
   seam is marked in `usecases/apply_payment.go`.
4. **Rotate and scope the webhook secret**, and consider IP allow-listing the provider.
5. **Distributed tracing.** OpenTelemetry spans across handler → usecase → SQL. At 1,667 req/s,
   sampled tracing is how you find the one slow customer rather than staring at aggregates.
6. **Alert on the invariant, not just the infrastructure.** A nightly job asserting
   `SUM(applied + excess) == amount_paid` per deployment catches bugs no uptime monitor will. Alert
   also on `payment_shed_total`, unmatched payments accumulating, and `payment_queue_depth`.

### Product gaps

7. **Reversals and chargebacks.** A settled credit can still be reversed by the bank, and there is no
   path to unwind one. Model it as a compensating ledger entry referencing the original, not as a
   mutation — the ledger must stay append-only.
8. **Fees, penalties and allocation order.** Real products apply a payment to fees, then arrears,
   then principal. When that lands, the rule belongs in `deployment.SplitPayment` — one pure, tested
   function — not spread across SQL.
9. **Delinquency as a scheduled job.** `ScheduleStatus` derives `behind` on read; nothing writes
   `status = 'delinquent'`. A daily sweep over `outstanding` with the partial index already in place
   should promote and demote deployments and emit events for collections.
10. **Settlement reconciliation.** A daily job comparing the bank's settlement file against the
    ledger, surfacing what the bank thinks it sent that never arrived. Idempotency makes the fix
    safe: replay the missing notifications.

### If sustained volume really is 100k/minute

11. **Shard by `customer_id`** (Citus or application-level). The workload is naturally sharded, so
    this scales close to linearly.
12. **Tier the ledger.** Keep 90 days hot; stream older partitions to object storage and query them
    through a warehouse. `raw_payload` is the bulk of each row and only needed for disputes.
13. **Kafka instead of Redis** if the queue must outlive Redis memory — retention that is disk-shaped
    and replayable. `payment.Queue` is one method wide, so it is a new implementation of an existing
    contract.

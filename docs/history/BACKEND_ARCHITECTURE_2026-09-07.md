# Tầng serving local — đánh giá và thiết kế refactor (2026-09-07)

Phạm vi: `internal/proxy`, `internal/hub`, `internal/pools`, `internal/upstream`,
`internal/ownership`, `cmd/anypick/serve.go`. Người review: Backend Architect.

> **Lưu ý về thuật ngữ.** "Backend" ở đây không phải dịch vụ cloud. Đây là
> tầng HTTP loopback mà AnyPick dựng lên để đứng giữa AI coding client và
> vendor upstream. Bài toán "scale" của nó là: nhiều stream đồng thời, nhiều
> account trong pool, process sống nhiều ngày không được leak, treo, hay crash.

---

## 0. Tóm tắt cho người bận rộn

1. **Có một lỗi làm crash process, đã chứng minh bằng test**, không phải suy đoạn:
   `internal/upstream/quota_guard.go` có concurrent map access giữa đọc vòng
   ring và ghi cooldown. Hai request đi qua cùng một pool đồng thời ⇒
   `fatal error: concurrent map iteration and map write` ⇒ process chết, không
   recover được. Xem §2.1.
2. **Nguyên nhân gốc không phải lỗi đó.** Nó là triệu chứng của việc tồn tại
   **bốn pipeline request khác nhau** (`proxy.Server`, `PoolServer`,
   `hub.Server`, và đường `ServeAuthorized` của pool nằm trong hub). Mỗi yêu cầu
   mới — timeout, metric, giới hạn concurrency — phải được thêm ở bốn chỗ, và
   thực tế mới được thêm ở một. Đó là lý do tầng này vừa thiếu ổn định vừa
   không đo được.
3. **Thuốc không phải là viết lại.** Là gom bốn pipeline thành một lõi có bốn
   "cửa trước" mỏng. Kế hoạch sáu bước ở §4, mỗi bước land độc lập; bước 0 là
   một commit sửa crash.

---

## 1. Hiện trạng

```
Claude Code / Codex / dsh / zcode / opencode ...
        │
        ├──► proxy.Server        (1 listener / account)   ─┐
        ├──► proxy.PoolServer    (1 listener / provider)   ─┤
        │       └── failover ring + QuotaGuard             │
        └──► hub.Server          (1 listener / root)       ─┤
                ├── pool source  → PoolServer.ServeAuthorized
                └── account src  → proxy.Serve             ─┘
                                        │
                                        ▼
                                 vendor upstream
```

Bốn đường đều tự làm lại: xác thực, đọc/buffer body, resolve upstream, translate
protocol, forward, quyết định failover. Không có chỗ nào là "đường đi chuẩn".

### 1.1 Bất biến không được phá (đây là ràng buộc, không phải gợi ý)

| Bất biến | Nguồn | Hệ quả cho thiết kế |
| --- | --- | --- |
| Bind loopback, mọi route có credential cần per-instance secret | ADR-0006 | `AssertLoopbackHost` phải giữ ở mọi chỗ bind; thêm listener mới mà quên là lỗi bảo mật |
| Token không bao giờ rời khỏi ranh giới thực thi | ADR-0006 | Observer/metrics **không được** ghi token; cần gate chặn |
| Mọi mutation đi qua coordinator, lock theo provider scope | ADR-0009, 0011 | Cooldown writer mới cũng phải tuân theo |
| Model ID không đổi, không có first-source-wins | ADR-0013 | Xem finding §2.6 |
| Credential routing là object của backend; HTTP server chỉ lo auth + route + convert | ADR-0016 | Lõi pipeline mới không được biết provider ID |
| Activation: planned → journalled → executed | ADR-0003 | Pipeline không được tự sửa activation state |

---

## 2. Findings

Mức độ: **SEV1** = sập hoặc treo dưới tải; **SEV2** = suy giảm nghiêm trọng;
**SEV3** = vệ sinh.

### 2.1 SEV1 — `QuotaGuard` có concurrent map access, crash process

`quota_guard.go:150` `load()` trả `State` **theo giá trị**, nhưng `State.Cooling`
là map header ⇒ người gọi nhận cùng map đang sống, không phải bản sao.
- `load():162-166` **duyệt và xoá** map đó, có giữ `mu`.
- `Ordered():69` **đọc** map đó, *sau khi* `load()` đã nhả `mu`.
- `mutate():112-113` **ghi** `state.Cooling[from]`, chỉ giữ `writeMu`, **không**
  giữ `mu`.

`writeMu` và `mu` không phải hai cấp khoá có chủ đích — chúng là hai khoá bảo vệ
cùng một map, và mỗi bên thiếu một nửa.

**Bằng chứng** (probe tạm thời, 8 goroutine × 300 iteration, đã xoá sau khi chạy):

```
$ go test -race -run TestZZGuardRaceProbe ./internal/upstream/
WARNING: DATA RACE
Read at 0x00c0000a3320 by goroutine 10:
  upstream.(*Guard).load()      quota_guard.go:162
  upstream.(*Guard).Ordered()   quota_guard.go:61
Previous write at 0x00c0000a3320 by goroutine 14:
  upstream.(*Guard).Exhausted.func1()  quota_guard.go:113
  upstream.(*Guard).mutate()           quota_guard.go:178

$ go test -count=1 -run TestZZGuardRaceProbe ./internal/upstream/
fatal error: concurrent map iteration and map write
  upstream.(*Guard).load()  quota_guard.go:162
FAIL
```

`fatal error` không phải panic có thể recover. Nó giết cả process — tức là giết
proxy, và theo ADR-0003 §3 là mất ownership, client bên trên nhận connection
refused. Điều kiện kích hoạt: pool có ≥ 2 account + guard bật + hai request
trùng thời điểm. Một coding agent bắn request song song là đủ.

### 2.2 SEV2 — Không có `http.Transport` nào được cấu hình; mọi client ăn default

`grep "http.Transport{|DefaultTransport|MaxIdleConns"` trên toàn `src/go`
**không trả về kết quả nào**. Hệ quả: mọi client kế thừa
`MaxIdleConnsPerHost = 2` và `ResponseHeaderTimeout = 0` (vô hạn).

Client được tạo theo request — mỗi lần một `&http.Client{Timeout: 10m}`:
`upstream_serve.go:208`, `pool_server.go:362`, `gemini_serve.go:186,292,411`.
Tất cả `httputil.ReverseProxy` cũng để `Transport: nil`
(`upstream_serve.go:58,110,154`, `pool_server.go:281`).

Hai hệ quả:
- **Churn kết nối:** quá 2 request đồng thời tới cùng host, số connection dư bị
  đóng sau khi xong và phải TCP + TLS handshake lại ở đợt sau.
- **Không có deadline thực:** `ResponseHeaderTimeout = 0` nghĩa là vendor nhận
  connection rồi không trả header ⇒ request treo vô hạn. `Client.Timeout = 10m`
  là tổng thời gian, nên với stream hợp lệ nó lại là cái có thể cắt nhầm.

### 2.3 SEV2 — Failover stampede, không có single-flight

`pool_server.go:109` mỗi request gọi `Ordered()` và nhận **cùng một** thứ tự
đầu ring. N request đồng thời ⇒ N lần đánh vào cùng member đã hết quota ⇒ N lần
gọi `Exhausted()` (`:145`). Không có lease, không có dedup. Gộp với §2.1, đây
chính là cách kích hoạt crash.

### 2.4 SEV2 — Ghi state file trên đường nóng, dưới khoá

`mutate()` gọi `WriteState` (`:183`) khi đang giữ `writeMu`; `load()` gọi
`os.ReadFile` (`:154`) khi đang giữ `mu`. Mỗi lần failover trả giá bằng một lần
ghi file đồng bộ, và mọi request khác xếp hàng sau nó.

### 2.5 SEV2 — Hub ghi field không khoá, nuốt lỗi serve loop

`hub/server.go:84` `go func() { _ = hs.Serve(ln) }()` — lỗi accept loop bị bỏ.
`:82` và `:85` ghi `s.endpoint` / `s.httpSrv` **không giữ `s.mu`**, trong khi
`Stop():96` đọc có khoá và `Endpoint():90` đọc **không khoá**. Không có `Err()`
như hai server kia. Nếu hub listener chết, nó biến mất trong im lặng.

### 2.6 SEV3 — Hub mặc định chọn source 0, mâu thuẫn với ADR-0013 §3

`hub/server.go:239` `defaultSourceIndex` trả `0` khi không khớp model nào.
ADR-0013 §3 ghi rõ: *"There is no first-source-wins fallback"*. Với `GET
/v1/models` (không có body ⇒ không resolve được model) hub sẽ phục vụ bằng source
đầu tiên thay vì từ chối.

### 2.7 SEV3 — Hub buffer 64 MB cho mọi POST, kể cả khi không cần

`hub/server.go:130` đọc toàn bộ body vào RAM cho mọi request POST, rồi
`rewriteModel` (`:207`) `json.Unmarshal` vào `map[string]interface{}` và marshal
lại khi có mapping. Marshal lại qua `interface{}` biến mọi số thành `float64`
⇒ số nguyên lớn trong body request có thể đổi giá trị. `PoolServer` cũng buffer
tương tự (`pool_server.go:97`) và buffer đó được replay cho từng member
(`:125-129`).

### 2.8 SEV3 — Những cái khác

| Vị trí | Vấn đề |
| --- | --- |
| `proxy/server.go:298` | `proxyLogSink` global không khoá, test gán lại |
| `gemini_serve.go:31` | `codeAssistProjects` cache vô hạn, key là credential sống ⇒ process sống lâu sẽ phình |
| `gemini_serve.go:355` | `geminiCooldowns.until` không bao giờ dọn key |
| `context_budget.go:121` | `regexp.MustCompile` trong đường lỗi context-window |
| `pool_server.go:168` | `ResolveUpstream(context.Background(), …)` — bỏ mất cancellation của client |
| `lease_store.go:154` | Không có heartbeat/TTL: lease của process sống nhưng bị kẹt không bao giờ bị thu hồi |
| `pool_server.go:69-78` | Pool request **không hề** được ghi access log — 3 trong 4 đường đi mù hoàn toàn |

### 2.9 Những thứ thực sự ổn — đừng đụng vào

- **Không có leak goroutine.** Chỉ 4 chỗ `go` trong các package này, tất cả đều
  có đường kết thúc rõ ràng. Đây là phần khoẻ nhất của tầng này.
- **Không set `WriteTimeout` là đúng.** Giá trị nào đủ thấp để có nghĩa cũng sẽ
  cắt SSE hợp lệ. Đừng "sửa" cái này.
- Registry per-instance đều có mutex đúng (`server.go:421`, `service_pool.go:71`).
- Ownership chống PID-reuse bằng instance-id echo + verify argv — thiết kế tốt.

---

## 3. Thiết kế đích

**Nguyên lý: một pipeline, bốn cửa trước.**

```
  ┌──────────────────────────────────────────────────────────┐
  │  Cửa trước (mỏng, mỗi kiểu triển khai một cái)            │
  │  · authN: route token (hub) | instance token (proxy/pool) │
  │  · chọn source: theo model (hub) | cố định (proxy, pool)  │
  │  · body policy: passthrough | buffered | rewriting        │
  └───────────────────────────┬──────────────────────────────┘
                              │  Plan{ctx, body, candidates, rewrite}
                              ▼
  ┌──────────────────────────────────────────────────────────┐
  │  Lõi pipeline (đúng một bản hiện thực)                    │
  │   1. resolve    → Upstream cho candidate                  │
  │   2. egress     → transport chia sẻ + deadline policy     │
  │   3. classify   → ok | quota-class | transient            │
  │   4. failover   → ring có thứ tự + cooldown single-flight │
  │   5. translate  → adapter protocol (không biết provider)  │
  │   6. observe    → in-flight, latency, status, failover    │
  └──────────────────────────────────────────────────────────┘
```

### D1 — Một transport egress, một package sở hữu: `internal/egress`

Tên này cố ý không đụng `internal/transport` (package đó là capability pairing
theo ADR-0016). `internal/egress` sở hữu:

- Một `*http.Transport` clone duy nhất:
  `MaxIdleConnsPerHost: 64`, `MaxIdleConns: 512`, `IdleConnTimeout: 90s`,
  `TLSHandshakeTimeout: 10s`, `ResponseHeaderTimeout: 60s`,
  `ExpectContinueTimeout: 1s`, `ForceAttemptHTTP2: true`, `DialContext`
  10s dial / 30s keep-alive.
- **Chính sách deadline thay vì timeout tổng:** `ResponseHeaderTimeout` xử lý
  "vendor không chịu trả header"; thêm *idle-stream timeout* (không có byte nào
  trong N giây ⇒ huỷ). Không đặt tổng thời gian — một stream hợp lệ có thể chạy
  10 phút, và `Client.Timeout = 10m` hiện tại là một cái bẫy đang chờ nổ.

Mọi `&http.Client{Timeout: 10*time.Minute}` và mọi `ReverseProxy{Transport: nil}`
phải lấy transport từ đây.

### D2 — Body policy tường minh, stream là mặc định

Ba policy, khai báo ở cửa trước:
- `passthrough` — không buffer; dùng khi đúng một candidate và không rewrite.
- `buffered(maxBytes)` — chỉ khi cần replay cho failover.
- `rewriting(maxBytes)` — rewrite JSON theo stream, **không** unmarshal toàn bộ
  vào `map[string]interface{}` rồi marshal lại (tránh đổi số nguyên, §2.7).

Vượt `maxBytes` trả **413**, không phải truncate rồi 400 như hiện tại.

### D3 — Failover: single-flight + persist bất đồng bộ

- `Guard` là chủ sở hữu duy nhất của cooldown state. `load()` trả **deep copy**
  (hoặc mọi truy cập nằm dưới `mu`). **Không bao giờ** đưa map sống ra ngoài.
  Gộp `writeMu` vào `mu`.
- `Exhausted` trở thành idempotent + single-flight theo khoá `(provider, from)`:
  trong một đợt stampede chỉ một request thực sự ghi cooldown, số còn lại đọc
  kết quả đó ở lần `Ordered()` tiếp theo.
- Persist: ghi vào bộ nhớ, đặt dirty flag, writer debounce (1s hoặc khi idle);
  `Close()` flush. Không `atomicfile.Write` trên đường nóng (§2.4).

### D4 — Một hợp đồng lifecycle: `Runner`

```go
type Runner interface {
    Endpoint() string
    WaitReady(timeout time.Duration) error
    Err() <-chan error
    Close() error
}
```

Một implement dùng chung. `hub.Server` adopt (hiện thiếu `Err()`, ghi field
không khoá, nuốt lỗi serve — §2.5). Ba bản sao của cùng một race biến mất.

### D5 — Observability nằm trong pipeline, không phải trang trí

Hiện tại: **không có metric nào** — không Prometheus, không expvar, không atomic
counter; tín hiệu duy nhất là một dòng `proxy.log`, và đường pool/hub không ghi
gì cả. Observer trong lõi ghi: in-flight gauge, latency theo (route, class),
đếm status upstream, số lần failover, stream duration.

Hai đích: (a) dòng `proxy.log` theo format design ngJKM hiện tại; (b) snapshot
in-process mà desktop helper/TUI **đang poll sẵn** — không cần daemon mới, không
cần dependency mới. Khi có cái này, câu "tôi có số liệu" mới trả lời được câu
"đường nào đang ốm".

### D6 — Giới hạn concurrency ở cửa trước

Semaphore per listener (mặc định 256 in-flight, queue depth 0), vượt ⇒ 503 +
`Retry-After`. Thêm `MaxHeaderBytes` và `IdleTimeout` (hiện tại idle connection
không bao giờ bị thu hồi trên listener proxy). Một client bắn 500 stream song
song phải suy giảm có thể đoán trước, không phải cạn FD rồi chết.

### D7 — Những gì thiết kế này không được làm

- Không dùng `switch` theo provider ID trong lõi (ADR-0016: provider tự xây
  backend).
- Không cho pipeline sửa activation state (ADR-0003).
- Observer không bao giờ ghi token (ADR-0006) — cần gate chặn.
- Cooldown writer mới vẫn đi qua coordinator (ADR-0009/0011).

---

## 4. Kế hoạch di trú

Mỗi bước land được độc lập; không bước nào yêu cầu bước sau tồn tại.

| # | Việc | Tiêu chí xong | Gate/test phải thêm |
| --- | --- | --- | --- |
| **0** | Sửa `quota_guard.go` (deep-copy, gộp khoá, giữ lock qua `change`). Thêm `internal/egress` transport, inject vào 5 chỗ tạo client | `go test -race ./internal/upstream/... ./internal/proxy/...` xanh | `TestGuardConcurrentOrderedExhaustedActivate` (chạy `-race`). **Mutation-check:** bỏ deep-copy ⇒ test phải fail |
| **1** | Định nghĩa `Runner`, `hub.Server` adopt (thêm `Err()`, khoá field, không nuốt lỗi serve) | `go test -race ./internal/hub/...` xanh | `TestHubStartStopIsRaceFree`. Sửa `defaultSourceIndex` (§2.6) cùng commit |
| **2** | Tách lõi pipeline; `proxy.Server` và `PoolServer` thành hai `Plan` trên cùng lõi | Mọi test proxy/pool hiện có chạy y hệt, không sửa expectation, coverage không giảm | Giữ nguyên 801 + 385 dòng test; thêm `TestPipelineBodyPolicy` |
| **3** | Hub đi qua lõi pipeline; bỏ buffer 64 MB vô điều kiện; body policy `rewriting` chỉ khi route có mapping | Hub không còn tự `net/http` egress | `TestHubDoesNotRewriteWhenNoMapping` (bắt đúng bug §2.7) |
| **4** | `Exhausted` single-flight + persist debounce | Stampede 50 request vào 1 member 429 ⇒ đúng **1** lần ghi cooldown | `TestFailoverStampedeCollapses`; xoá `context.Background()` ở `pool_server.go:168` |
| **5** | Observer + semaphore + `IdleTimeout`/`MaxHeaderBytes` | Có in-flight/latency/failover trên **cả 4** đường | `TestObserverNeverRecordsToken` (ADR-0006); cơ hội hạ budget `s.Deps.Store` 32 → 30 |

### Đừng làm

- **Đừng thêm `WriteTimeout` / `ReadTimeout`** trên listener — sẽ cắt SSE hợp lệ.
- **Đừng thêm queue, broker, Redis.** Đây là proxy loopback một người dùng;
  thêm hàng đợi chỉ thêm chỗ hỏng chứ không thêm khả năng chịu tải.
- **Đừng tách thành process/service riêng.** ADR-0020 đã chốt một binary.
- **Đừng đổi semantics model ID hay route** khi refactor (ADR-0013).

---

## 5. Đã thực thi (2026-09-07)

| Hạng mục | Kết quả |
| --- | --- |
| **Sửa crash `QuotaGuard`** (§2.1) | Xong. `loadLocked()` trả deep copy, gỡ `writeMu`, ghi file **ngoài** khoá. Test `TestGuardConcurrentUseIsRaceFree` + `TestGuardLoadDoesNotShareLiveState`, đã mutation-check |
| **Chống stampede** (§2.3) | Xong. `Exhausted` bỏ qua khi member đang cooling ⇒ burst 32 request ghi **1** audit event thay vì 27. Test `TestGuardExhaustedCollapsesConcurrentReports`, đã mutation-check |
| **`internal/egress`** (§2.2) | Xong. Một transport: `MaxIdleConnsPerHost=64`, `ResponseHeaderTimeout=60s`, `MaxIdleConns=512`. Gắn vào 4 `ReverseProxy` và 8 chỗ tạo client. Test `TestTransportIsTuned` chặn quay về `DefaultTransport` |
| **Hub lifecycle** (§2.5) | Xong. `Endpoint()`, `Err()`, `config()` đều qua `mu`; lỗi serve loop không còn bị nuốt. Test `-race` `TestStartPublishEndpointRaceFree` |
| **Số nguyên trong hub** (§2.7) | Xong. `rewriteModel` decode bằng `UseNumber`. Mutation-check đo được: `9007199254740993` → `...92` khi bỏ fix |
| **Cancellation ở pool** | Xong. `ResolveUpstream` dùng `r.Context()` thay vì `context.Background()` |
| **Access log cho pool** (§2.8) | Xong. Đường pool giờ ghi `proxy.log` như ba đường kia |
| **`proxyLogSink`** | Xong. Có mutex, không còn ghi xen kẽ từ nhiều handler |
| **Cache Gemini vô hạn** (§2.8) | Xong. `codeAssistProjects` giới hạn 64; `geminiCooldowns` quét entry hết hạn khi vượt 512 |
| **Deadlock khi `hub.Stop()`** (mới) | Xong. `Stop()` giữ `mu` qua `Shutdown()`, mà `Shutdown()` chờ request đang bay, và mọi request gọi `config()` dưới chính `mu` đó ⇒ kẹt 5 s theo shutdown context, `Endpoint()`/`Err()` kẹt theo. Giờ lấy `srv` dưới khoá rồi nhả trước khi shutdown. Test `TestStopDoesNotHoldTheLockAcrossShutdown` (mutation: kẹt đúng 2 s) |
| **Body > 64 MB bị cắt lén** (mới) | Xong ở **cả 8 chỗ**. `io.LimitReader` cắt im lặng ⇒ hub chuyển tiếp JSON **gãy** lên upstream rồi đổ lỗi cho vendor. Gom thành một helper `proxy.ReadCappedBody` + `proxy.MaxBodyBytes`, vượt ⇒ 413; hub gọi chung thay vì giữ bản copy riêng. Test ở cả hai package; một mutation duy nhất giết được cả hai (mutation: `err = <nil>`, tức cắt xong không báo) |
| **Nghẽn resolve trong pool** (mới) | Xong. `upstreamFor` giữ **một** mutex của cả pool qua `ResolveUpstream` — mà resolver đọc store và có thể refresh credential qua mạng ⇒ mọi member xếp hàng sau lần resolve chậm nhất. Đổi sang khoá **per-account**: member khác resolve song song, cùng member gộp thành một lần. Test `TestPoolResolvesDifferentMembersInParallel` (mutation: 2 s mà member thứ hai chưa vào được resolver) + `TestPoolCachesResolvedMember` |
| **Ring cố gắng vô ích** (mới) | Xong. Client cúp máy giữa chừng ⇒ vẫn đi hết các member còn lại, mỗi lần mở kết nối rồi chết vì context đã huỷ. Giờ thoát sớm khi `r.Context().Err() != nil` |
| **Comment sai trên `PoolServer.Members`** (mới) | Xong. Comment nói "resolve lazily per request" trong khi thực tế cache lại sau lần đầu. Sửa thành mô tả đúng hợp đồng: server được dựng lại ở mỗi `StartPoolProxy`, nên cache không sống sót qua đổi membership |
| **Không có tổng deadline cho ring** (§6 cũ) | Xong. `DefaultPoolRingDeadline = 2m` là ngân sách **pre-commit** cho cả ring; mỗi attempt nhận ctx có cùng deadline tuyệt đối, `captureWriter.onCommit` dừng timer nên stream đã commit không bị cắt. Attempt chết vì hết ngân sách trả `504 upstream_timeout` thay vì giả làm lỗi upstream. 4 mutation bị giết |
| **Lease heartbeat + TTL** (§6 cũ) | Xong. `HeartbeatInterval=15s` / `LeaseExpiry=2m`; child tách ra tự giữ lease bằng `HeartbeatInstanceContext` (nó không biết lease id). `ReapStaleContext` scan NULL-safe (bug thật: scan `instance_id` NULL vào `string` ⇒ đúng hàng cần reap bị bỏ mất), và **PID bị giáng cấp thành quyền phủ quyết, không phải bản án**: lease hết hạn mà instance echo vẫn khớp ⇒ giữ |
| **Observer + semaphore + giới hạn listener** (bước 5, D5/D6) | Xong. `internal/observe` đếm in-flight/latency/failover/byClass/byStatus trên **cả 4 đường** (hàng riêng cho ring do hub uỷ quyền); `Limiter` 256 slot không hàng đợi, vượt ⇒ 503 + `Retry-After: 1`. Counter render lên `/health`. `proxy.ApplyServingLimits` gắn `ReadHeaderTimeout` 10s / `IdleTimeout` 5m / `MaxHeaderBytes` 64 KiB cho mọi listener, **không** có `WriteTimeout`. `Begin(route string)` không nhận `*http.Request` ⇒ token không thể chạm counter theo cấu trúc (ADR-0006), giữ bằng gate AST. 22 mutation bị giết, một trong số đó là bug thật (`defer req.End(rec.Status(), …)` đánh giá đối số ngay ⇒ mọi request ghi thành 200) |
| **Một bảng dispatch egress** (bước 2, lát 1) | Xong. `Serve` và `serveMember` từng mỗi bên giữ **một bản sao** bảng quyết định (gemini → native Anthropic → translated → OpenAI-path → passthrough), và hai bản đã lệch nhau ở ba chỗ: (1) ring **bỏ qua** `rewriteContextWindowResponse`, (2) nhánh translated của ring **bỏ qua** chuyển đổi context-window ⇒ member chạm trần context trả nguyên envelope vendor, client không bao giờ compact, (3) nhánh native-Anthropic của ring tự merge auth và **không default header** ⇒ header rỗng, vendor trả 401 không phân biệt được với token chết. Giờ chỉ còn một hàm `egressKindFor`; classifier lỗi translated gom thành `classifyTranslatedFailure` (dùng chung cho đường commit và đường capture); merge target/parent gom thành `flattenedNativeUpstream` (dùng chung cho `serveNativeAnthropic` và leaf capture của pool). Xoá `resolveTargetAuth`. Test `TestEgressKindForCoversEveryUpstreamShape`, `TestPoolTranslatedMemberContextWindowIsConverted`, `TestPoolNativeAnthropicMemberUsesTheSharedMerge`, `TestFlattenedNativeUpstreamMergeRules`, `TestPoolPassthroughMemberContextWindowIsRewritten`, và gate AST `TestOneEgressDispatchTable` (fail nếu `Serve`/`serveMember` không switch trên `egressKindFor`, hoặc `isGemini`/`isMessages` bị suy luận lại ở nơi khác). 7 mutation bị giết |
| **Hub đọc bảng pin hai lần** (bước 3, §2.7) | Xong. `serveRoute` chọn source bằng `routeSourceIndex` rồi đổi model bằng `rewriteModel` — **hai lượt duyệt trên cùng một bảng**, và hai lượt không cùng quy tắc: source lấy entry khớp **đầu tiên**, model lấy entry khớp đầu tiên **có `UpstreamModel`**. Với pin trùng model ⇒ request bay sang source của entry 0 nhưng mang model id của entry 1. Gom thành một lookup `routeModelEntry` (xoá `routeSourceIndex`, chỉ còn test gọi). Thêm `rewriteModelField` và **chỉ re-encode khi pin thực sự remap** (trước đó mọi POST đều bị `json.Marshal` lại dù không có mapping). Test `TestHubDoesNotMixTwoPinsForOneModel` (fail ở code cũ), `TestHubDoesNotRewriteWhenNoMapping`, `TestHubStillRewritesWhenThePinSaysSo`, `TestRouteModelEntry`, gate AST `TestPinTableIsResolvedOnce` (fail nếu có hàm nào khác tự duyệt `route.Models`, hoặc có hàm resolve mà không đi qua `routeModelEntry`). 6 mutation bị giết |

### Một sửa đổi so với bản nháp

§2.6 đề nghị bỏ `defaultSourceIndex` vì mâu thuẫn với ADR-0013 §3. **Đề nghị đó sai và đã bị rút lại.** `route.Models` là **bảng ghim**, không phải allowlist: model không có trong manifest rơi về source mặc định theo thiết kế. ADR-0013 §3 cấm first-source-wins khi **hai source cùng claim một model** (xung đột), không cấm fallback. Đã thử sửa, hai test fail, đọc lại ADR, revert. Chỉ giữ lại phần `#N` ngoài phạm vi giờ trả lỗi thay vì rơi về source 0.

## 6. Chưa làm — và lý do

- **Bước 2–3: gom lõi pipeline.** Lát 1 + 2 đã xong — xem §5: bảng dispatch egress, classifier/merge dùng chung, và lookup bảng pin của hub. Còn lại: chính sách body thống nhất (`TestPipelineBodyPolicy`). Đó là viết lại bốn đường đi, không phải sửa lỗi — cần commit và review riêng.
- **Hub vẫn buffer body, có giới hạn.** `serveRoute` đọc toàn bộ body (capped `proxy.MaxBodyBytes`, vượt ⇒ 413) vì chọn source theo model và `#N` pin **buộc phải đọc `model` trong body**. Chưa có cách stream qua được mà vẫn giữ hai tính năng đó. Phần lãng phí đã bỏ là `string(body)` copy + re-encode vô điều kiện; phần còn lại là chi phí của model-based routing, không phải sơ suất.
- **`http.Transport` ở `internal/provider` và vài probe ngắn** (`cmd/anypick`, `internal/desktop`): giữ nguyên vì chúng có timeout tổng rất chặt (300 ms – 5 s) và không nằm trên đường request. Ghi chú trong `internal/egress` để người sau không bắt chước nhầm.
- **Cache regex trong `matchesQuotaErrorPattern`.** Compile lại mỗi lần quyết định failover. **Cố ý không làm:** vài microsecond so với một round trip mạng là nhiễu, và thêm cache lại sinh ra một map vô hạn khác cần chặn — đúng cái mình vừa phải sửa ở Gemini.

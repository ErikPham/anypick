# Kiến trúc tổng thể AnyPick — đánh giá và lộ trình dài hạn (2026-09-07)

Phạm vi: **toàn hệ thống** — Go engine (CLI / TUI / desktop service), macOS app,
Tauri Windows/Linux, kênh phân phối, release machinery, quy trình review.
Người review: Software Architect.

Tài liệu nằm trên hai tài liệu chuyên sâu đã có, không lặp lại chúng:

- `BACKEND_ARCHITECTURE_2026-09-07.md` — tầng serving loopback; findings SEV1–3
  và kế hoạch sáu bước. Phần lớn đã land cùng ngày (xem §5 của nó).
- `NATIVE_APP_CUTOVER.md` — lộ trình cutover NATIVE-M0…M9 theo ADR-0020.

---

## 0. Tóm tắt cho người bận rộn

1. **Kiến trúc nền là đúng, đừng viết lại.** Một binary cho mọi surface
   (ADR-0020), bất biến có răng (21 ADR, gate đã mutation-check), persistence
   đúng chuẩn ở chỗ không được phép sai (credential của người dùng thật).
2. **Ba khoản nợ cấu trúc quyết định tốc độ 12 tháng tới:** (a) bốn pipeline
   request song song ở tầng serving — crash đã sửa, phần gom lõi chưa làm;
   (b) `App` god object, store lọt thẳng ra outer layer, type system không chặn
   — chỉ có một test đếm số; (c) observability gần bằng 0 nên mọi ngưỡng vận
   hành đều là đoán.
3. **"Scale" ở đây không phải RPS.** Là bốn trục: số client/provider hỗ trợ,
   số platform phân phối, số ngày process sống không hỏng, số người contribute
   được mà không phá bất biến. Thiết kế và roadmap bên dưới đo theo bốn trục đó.
4. **Nút cổ chai lớn nhất cho tăng trưởng là phân phối, không phải code:**
   unsigned, release dispatch-only macOS-only, npm adapter chưa tồn tại, và
   `anypick update` đang hứa một package không có thật (`misc.go:213` in
   `pnpm install -g anypick@latest`).
5. **Roadmap bốn giai đoạn G0→G3 ở §5**, mỗi giai đoạn có tiêu chí xong và gate
   tương ứng. G0 đang chạy. Bảy quyết định cần Erik chốt ở §6 — trong đó có hai
   cái chỉ anh quyết được (signing credentials, publication approval) và chúng
   đang chặn toàn bộ G2.

---

## 1. AnyPick là loại hệ thống nào — và "scale" nghĩa là gì ở đây

AnyPick **không phải dịch vụ cloud**. Nó là local infrastructure: một credential
broker + loopback proxy + desktop app chạy trên máy một người dùng, process sống
nhiều ngày, nắm quyền ghi lên file credential của các tool khác. Hỏng ở đây
nghĩa là **phá login thật của developer** — không phải mất một request.

Bốn trục scale thật, và hệ quả thiết kế của từng trục:

| Trục scale | Câu hỏi đo | Hệ quả thiết kế |
| --- | --- | --- |
| Client/provider | Thêm một client mới tốn bao nhiêu công? | Phải là O(1): một adapter row + một registry entry; `adapter_test.go` và TUI reachability test ép điều này |
| Process longevity | Sống 30 ngày có leak/treo/crash không? | Cache phải có chặn, lock phải đúng, không goroutine mồ côi — tầng serving là nơi quyết định |
| Platform phân phối | Cài được trên macOS/Win/Linux bằng một lệnh? | Một artifact logic, nhiều kênh; signing/notarize là một phần của kiến trúc, không phải việc vặt cuối dự án |
| Con người | Contributor thứ hai có phá được bất biến không? | Bất biến phải nằm trong gate chạy được, không nằm trong đầu một người |

---

## 2. Kiến trúc hiện tại

```
┌─ Surfaces ─────────────────────────────────────────────────────┐
│  CLI (cmd/anypick)   TUI (internal/tui)   desktop-service       │
│  ── stdio protocol v2, bounded frames, version handshake ──     │
│  AnyPick.app (Swift, owns lifecycle)   Tauri (Win/Linux)        │
└──────────────────────────────┬────────────────────────────────┘
                               ▼
┌─ Composition root: internal/app (App.compose) ─────────────────┐
│  services: accounts · proxy · config · plugins · runtime       │
│  activation (plan → journal → execute) · desktop.Server        │
└──────────────────────────────┬────────────────────────────────┘
                               ▼
┌─ Domain ───────────────────────────────────────────────────────┐
│  transport (capability pairing) · client/provider registries   │
│  hub (model-routed) · pools (failover ring) · upstream/egress  │
└──────────────────────────────┬────────────────────────────────┘
                               ▼
┌─ Persistence (một bảng đúng một chủ, internal/db/tables.go) ───┐
│  store · journal · pools · hub · bindings · presets · profile  │
│  · plugins · modelcache — mọi mutation qua coordinator (0009)  │
└──────────────────────────────┬────────────────────────────────┘
                               ▼
        File credential của client khác (atomic write, ADR-0004)
        Vendor upstreams (qua internal/egress, một transport)
```

Quyền lực nằm ở: 21 ADR trong `adr/`; `make review` (fmt + vet + test + gates);
CI sáu job + `release.yml` (dispatch-only, mặc định `macos-only`).

---

## 3. Đánh giá

### 3.1 Những thứ thực sự ổn — đừng đụng vào

- **Kỷ luật bất biến có răng.** Coordinator cho mọi mutation
  (`coordinator_test.go`), một bảng một chủ (`tables_test.go`), capability
  pairing phải khai tường minh (`adapter_test.go`), mutation-check là quy ước
  review bắt buộc. CONTRIBUTING từng thừa nhận một invariant "untrue and
  enforced by nobody" và viết lại cho đúng — mức trung thực này hiếm, giữ lấy.
- **Correctness ở chỗ không được sai.** `writeMeta` là commit point nguyên tử
  (ADR-0004), activation planned → journalled → executed (ADR-0003/0008),
  backup/import không bao giờ phá account đang sống.
- **Tầng serving không leak goroutine** — chỉ 4 chỗ `go`, tất cả có đường kết
  thúc (đã chứng minh ở tài liệu backend). Crash `QuotaGuard` và stampede đã
  được sửa và mutation-check cùng ngày.
- **Process ownership chống PID-reuse** bằng instance-id echo + verify argv —
  thiết kế tốt, cần thiết vì app sở hữu lifecycle của core (ADR-0019→0020).
- **Protocol app↔core bounded, versioned, không daemon/localhost API** — đúng
  hướng cho cả Swift lẫn Tauri.

### 3.2 Nợ cấu trúc, xếp theo mức độ nghiêm trọng

1. **Bốn pipeline request song song** (`proxy.Server`, `PoolServer`,
   `hub.Server`, đường `ServeAuthorized`). Crash SEV1 đã sửa; nhưng nguyên nhân
   gốc — mọi yêu cầu mới phải thêm ở bốn chỗ — vẫn còn. Kế hoạch gom lõi: bước
   2–5 của `BACKEND_ARCHITECTURE_2026-09-07.md` §4. **Đây là nợ SEV1-về-bản-chất
   dù triệu chứng đã hết.**
2. **`App` god object + store lọt ra outer layer.** ~20 field exported;
   `facade_test.go` đếm `a.Store:20`, `s.Deps.Store:32`, `a.Hub:15`,
   `a.Bindings:11`, `a.Runtime.Profiles:9`. Ratchet này **chỉ chặn tăng, không
   đẩy giảm**, và type system hoàn toàn im lặng — "CLI, TUI và desktop protocol
   call services" là quy ước văn hoá, không phải kiến trúc. Desktop layer rò
   nhất (~30 call sites); hai read trong `desktop/hub_snapshot.go:132,241`
   đáng lẽ qua accounts service. Có contributor thứ hai là nợ này thối ngay.
3. **Observability gần bằng 0.** Không metric nào — không counter, không gauge,
   không expvar; tín hiệu duy nhất là `proxy.log` (đường pool mới được nối log
   hôm nay). ADR-0010 đã đặt port nhưng chưa có consumer thật. Hệ quả trực
   tiếp: tài liệu backend phải **hoãn bước observer+semaphore vì không có số
   liệu để đặt ngưỡng** — nợ này đang chặn cả việc sửa nợ khác.
4. **`internal/bindings` không lock** — đúng nhờ caller giữ; fix đúng cần ADR
   và route qua coordinator (CODE_REVIEW.md §10).
5. **Sáu ctx-less read wrapper** trong `internal/store` (`account_store.go:91,
   114,439,473,597`, `proxy_config.go:14`) — mất cancellation.
6. **Release machinery thiếu phần đắt nhất.** M8 (signing, notarize, SBOM)
   mostly missing; `release.yml` dispatch-only; và **lời hứa gãy với user**:
   `anypick update` bảo `pnpm install -g anypick@latest` trong khi không có
   package nào — `docs/reference/cli.mdx` cũng ghi hành vi npm-channel. User
   đầu tiên chạy lệnh này sẽ mất niềm tin ngay.

### 3.3 Rủi ro con người / quy trình

- **Bus factor = 1 và các chốt an toàn nằm ngoài repo.** CODEOWNERS chưa cần
  (chưa có ai để route), nhưng branch ruleset bắt buộc review job + build/test
  chưa bật — file trong repo không enforce được setting hosting; phải ghi lại
  cấu hình đã bật ở đâu đó kiểm chứng được.
- **Nhiều session song song sửa cùng một tree** (đã xảy ra ở `src/macos`) —
  cần thói quen `stat` trước khi sửa Swift/pbxproj, và CI đủ mạnh để bắt xung
  đột.

---

## 4. Kiến trúc đích (12–18 tháng)

Năm nguyên tắc. Mỗi nguyên tắc kèm cách đo "đã tới chưa".

**T1 — Một lõi pipeline, bốn cửa trước.** Theo thiết kế §3 của tài liệu
backend: cửa trước mỏng (authN, chọn source, body policy), lõi đúng một bản
hiện thực (resolve → egress → classify → failover → translate → observe).
*Đo:* thêm timeout/metric/limit mới chỉ sửa một chỗ.

**T2 — Outer layer chỉ nói chuyện với services.** Lộ trình hạ `facadeBudget`
có lịch, không phải nguyện vọng: `s.Deps.Store` 32 → 30 khi accounts service
có readers → tiếp tục về 0; `App` thu còn composition, không còn kho handle.
Mỗi lần hạ budget là một commit khoá lại (test đã hỗ trợ sẵn nhánh "shrank —
lower the budget"). *Đo:* `facadeBudget["s.Deps.Store"] == 0`, xoá key khỏi map.

**T3 — Observability là một port, không phải trang trí.** Observer trong lõi
pipeline (in-flight, latency theo route+class, status, failover, stream
duration); hai đích: `proxy.log` và snapshot in-process mà desktop/TUI **đang
poll sẵn** — không dependency mới, không daemon mới. Không bao giờ ghi token
(ADR-0006, kèm gate). *Đo:* câu "đường nào đang ốm" trả lời bằng số, và bước 5
của tài liệu backend un-block.

**T4 — Một artifact logic, nhiều kênh phân phối.** Source checkout → GitHub
Release (signed, notarized, SBOM) → npm adapter thin (optionalDependencies,
zero domain behaviour, ADR-0020 §4) → Win/Linux qua Tauri. Mọi kênh cùng một
`VERSION`, cùng `release-plan` từ Conventional Commits, cùng `artifact-check`
Node-free. *Đo:* `npm install -g anypick && anypick providers` chạy được trên
ba OS từ artifact đã ký.

**T5 — Bất biến mới đi kèm gate mutation-checked.** Quy trình bốn nấc của
CODE_REVIEW.md áp cho mọi finding mới; ADR mới cho mọi quyết định dài hạn
(bindings lock, lease TTL, deadline policy). *Đo:* không invariant nào chỉ sống
trong văn bản.

---

## 5. Roadmap

Mỗi giai đoạn land độc lập; không giai đoạn nào yêu cầu giai đoạn sau tồn tại.

### G0 — Ổn định serving + xoá lời hứa gãy (đang chạy; 1–2 tuần)

| Việc | Tiêu chí xong | Gate |
| --- | --- | --- |
| Bước 2–3 tài liệu backend: gom lõi pipeline, proxy/pool/hub thành `Plan` | Mọi test proxy/pool/hub chạy y hệt, coverage không giảm; hub không còn tự egress | `TestPipelineBodyPolicy`, `TestHubDoesNotRewriteWhenNoMapping` |
| Bước 4–5: single-flight đã có; observer + semaphore + deadline policy | Metric tối thiểu trên **cả bốn** đường; 503 + Retry-After khi quá in-flight | `TestObserverNeverRecordsToken` (ADR-0006); `TestFailoverStampedeCollapses` |
| Sửa `misc.go:213` + `cli.mdx`: bỏ lời hứa npm, trỏ về kênh thật | `anypick update` in hướng dẫn tồn tại thật | Review thủ công (một dòng, đừng biến thành tính năng) |
| Chính sách lease heartbeat/TTL (`lease_store.go:154`) | ADR ngắn + hiện thực | Test thu hồi lease của process kẹt |
| Tổng deadline cho một request qua ring | Chính sách bằng văn bản (N×60s hiện tại là vô lý) + hiện thực | Test deadline tổng |

### G1 — Đóng nợ cấu trúc Go (1–2 tháng)

| Việc | Tiêu chí xong | Gate |
| --- | --- | --- |
| accounts service có readers; route `hub_snapshot.go:132,241` | `facadeBudget["s.Deps.Store"]` hạ 32 → 30 và khoá lại | facade_test nhánh "shrank" |
| ADR + hiện thực bindings lock qua coordinator | `bindings.Store.Root` không phụ thuộc caller lock | Mở rộng coordinator_test |
| Sáu ctx-less read wrapper có ctx | Không còn `context.Background()` sản xuất trong store | testgate ctx-first đã có sẵn răng |
| Branch ruleset: require review job + build/test | Cấu hình ghi lại trong repo (docs/SECURITY) kiểm chứng được | — |
| CODEOWNERS | Thêm khi có người thứ hai; trước đó để chỗ trống có chủ đích | — |

### G2 — Cutover native + release thật (2–4 tháng; **bị chặn bởi §6 mục 1–2**)

| Việc | Tiêu chí xong | Gate |
| --- | --- | --- |
| Đóng NATIVE M1–M4 (live-parity, contract harness, archive/install, perf budgets) | Tracker M1–M4 xanh | Theo ma trận NATIVE_APP_CUTOVER.md |
| M5: Tauri installed smokes trên Win/Linux | Artifact staged → chạy được sau cài | CI tauri job mở rộng |
| M7: xoá `src/tui` sau khi Go TUI v1 đủ dùng | Không còn hai TUI | `make paths` |
| M8: signing + notarize + SBOM | Release artifact ký được, `spctl` pass | release.yml job mới |
| M9: final cutover, cleanup, audit | NATIVE_APP_CUTOVER.md đóng | Toàn bộ CI |

### G3 — Mở rộng (4–18 tháng)

| Việc | Tiêu chí xong | Gate |
| --- | --- | --- |
| npm adapter published (ADR-0020 §4) | `npm install -g anypick` + `npm exec` smoke xanh trên 3 OS | Smoke per-platform trong release.yml |
| Win/Linux published (bỏ `macos-only` default) | release.yml publish cả ba | — |
| Thêm client/provider theo quy trình O(1) | Một client mới = 1 adapter row + registry entry + phân loại trong adapter_test | adapter_test + TUI reachability (đã có) |
| Observability consumer thứ hai (desktop UI hiển thị số) | App đọc snapshot metric, không poll log | app-test |
| Plugin ecosystem trong giới hạn ADR-0021 | Native build install nhưng không execute plugin | artifact-check + test hiện có |

---

## 6. Quyết định cần chốt

| # | Câu hỏi | Chặn | Khuyến nghị | Ai quyết |
| --- | --- | --- | --- | --- |
| 1 | Apple signing identity + notarization credentials | Toàn bộ G2 | Làm sớm nhất có thể; đây là blocker duy nhất không sửa được bằng code | Erik |
| 2 | Publication approval (release.yml đang dispatch-only) | G2/G3 | Chốt tiêu chí "đủ tự tin để publish" bằng văn bản, gắn vào M9 | Erik |
| 3 | Tên/scope npm package | G3 | **Đăng ký giữ tên ngay** (placeholder rỗng), kẻo squat; quyết định rẻ, làm sớm | Erik |
| 4 | Chính sách lease TTL/heartbeat | G0 | TTL + heartbeat, thu hồi qua coordinator; viết ADR ngắn | Kiến trúc |
| 5 | Tổng deadline cho ring (N×60s hiện tại) | G0 | Deadline tổng cho phần chờ header; stream đã bắt đầu thì không cắt | Kiến trúc |
| 6 | Bindings lock: route qua coordinator thế nào | G1 | ADR mới, không vá caller | Kiến trúc |
| 7 | Lịch hạ facadeBudget về 0 | G1/G2 | Cam kết mỗi release hạ ít nhất một nấc hoặc ghi lý do vào CODE_REVIEW.md §10 | Kiến trúc |

---

## 7. Đừng làm

- **Đừng cloud-hoá, microservice-hoá, thêm queue/broker/Redis.** Đây là proxy
  loopback một người dùng; mỗi thành phần mới là một chỗ hỏng mới, không thêm
  khả năng chịu tải nào.
- **Đừng viết lại.** Mọi thay đổi lớn đều theo kiểu "gom dần, mỗi bước land
  độc lập" như kế hoạch sáu bước của tầng backend.
- **Đừng phá các bất biến đã ký** — đọc lại trước khi chạm: 0002 (trust
  boundary import/export), 0003 (process & lease ownership), 0004 (writeMeta
  atomic), 0006 (proxy secret không rời ranh giới), 0007 (bindings chỉ metadata
  project-scope), 0008 (crash backup owner-only), 0009+0011 (coordinator,
  provider-scope lock), 0013 (một hub, model ID chính xác), 0016 (capability
  routing), 0018 (một Swift tree), 0020 (Node-free artifact), 0021 (native
  build không execute plugin).
- **Đừng mở kênh phân phối mới khi kênh hiện tại còn gãy.** `anypick update`
  hứa npm trước khi npm tồn tại là bài học: kênh nào cũng phải kín
  (cài → chạy → update) trước khi quảng bá.
- **Đừng nới gate khi có contributor mới.** Ngược lại: CODEOWNERS + branch
  ruleset *trước*, rồi mới mở quyền push.

---

## 8. Đã thực thi cùng ngày (2026-09-07)

Các mục dưới đây đã land trong working tree cùng ngày tài liệu này được viết.
Mỗi mục ghi rõ gate/test đi kèm — không có mục nào chỉ sửa comment.

### 8.1 Kênh phân phối: xoá lời hứa npm (G0)

`anypick update` truy vấn GitHub Releases API thay vì npm registry, và in
`git pull && make build` thay vì `pnpm install -g anypick@latest`
(`cmd/anypick/misc.go`). 404 (chưa publish release nào) là kết quả hợp lệ, không
phải lỗi. Tài liệu `docs/reference/cli.mdx` bỏ kênh npm và `--check` không tồn
tại. **Lý do:** ADR-0020 — artifact Node-free, kênh thật là GitHub Release asset
và source checkout.

### 8.2 `AccountStore` read surface nhận `ctx` (G1)

Sáu wrapper đọc không có `ctx` bị xoá; tên hàm giữ nguyên, `ctx` là tham số đầu
tiên (`Get`, `Require`, `ListAccounts`, `GetActive`, `ReadProxyState`,
`ListProxyStates`, `GetProxyConfig`). ~40 caller và test được migrate.
Mọi đọc giờ có cancellation + deadline xuống `database/sql`.

### 8.3 Facade: hai lối tắt bị đóng thật (G1)

`desktop/hub_snapshot.go` đọc qua `accounts.Service.List/Get` thay vì chọc thẳng
store; model cache nhận `*discovery.Service` được inject thay vì tự dựng từ
`s.Deps.Store.DB`. `facadeBudget` cho `s.Deps.Store` hạ **32 → 30** — đây là
giảm thật nhờ added injection boundary, không phải chỉnh số.

### 8.4 ADR-0022: bindings tự khoá qua coordinator (G1)

`internal/bindings.Store` mang `Root` và tự lấy khoá trong mọi method ghi
(`client/<clientID>`, `project/<root>`, scope của ref). Re-entrancy giữ nguyên
nhờ scope trùng với activation. Test mới: ghi không lock từ caller vẫn an toàn;
nested mutation vào scope đang giữ không deadlock. Quyết định số 6 (§6) coi như
đã chốt: ADR mới, không vá từng caller.

### 8.5 Lease heartbeat + TTL (G0, quyết định số 4)

- `HeartbeatInterval = 15s`, `LeaseExpiry = 2m` (interval luôn ngắn hơn expiry,
  có test chặn ai đó đổi ngược lại).
- `HeartbeatContext(leaseID, instanceID, ownerPID)` — UPDATE có điều kiện, trả
  `sql.ErrNoRows` nếu không đúng chủ.
- `HeartbeatInstanceContext(instanceID, ownerPID)` — detached child tự giữ lease
  sống; trước đây lease của child hỏng ngay khi parent thoát.
- `ReapStaleContext` đổi luật: **PID chỉ là veto, không bao giờ là án.** Row cũ
  (không instance/endpoint) giữ rule PID-only; row mới cần heartbeat quá hạn
  **và** echo instance thất bại. Echo thắng cả heartbeat — proxy khoẻ mà mất
  quyền ghi DB không bị thu hồi.
- 5 mutation bị giết: heartbeat không check chủ; quay lại rule PID-only; bỏ echo
  override; bỏ kiểm tra freshness; instance heartbeat bỏ `owner_pid`.

### 8.6 Deadline tổng cho pool ring (G0, quyết định số 5)

`DefaultPoolRingDeadline = 2m` là ngân sách **pre-commit** cho cả ring, không
phải per-member. Mỗi attempt nhận ctx có cùng deadline tuyệt đối; khi member
commit header, timer bị dừng (`captureWriter.onCommit`) nên stream đã bắt đầu
không bao giờ bị cắt. Attempt chết vì hết ngân sách trả `504 upstream_timeout`
thay vì giả làm lỗi upstream.
4 mutation bị giết: bỏ escape lúc commit; bỏ ngân sách; bỏ cancel per-attempt
(bị bắt nhờ assertion thời gian phản hồi); bỏ kiểm tra ngân sách giữa các attempt.

### 8.7 Observer + semaphore (G0, bước 5)

Package mới `internal/observe`: registry counter in-process (in-flight, started,
completed, failovers, latency, stream time, `byClass`, `byStatus`) và `Limiter`
— semaphore **không có hàng đợi**, trần 256 concurrent/process; vượt trần trả
503 + `Retry-After: 1` ngay lập tức thay vì xếp hàng (hàng đợi không giới hạn
chỉ là bản chậm hơn của cú crash mà nó định tránh).

Cả bốn đường đều được đo, trên bốn hàng riêng: `proxy.single`, `proxy.pool`,
`proxy.pool.hub` (ring do hub uỷ quyền) và `hub`. Ring qua hub tách riêng vì
gộp vào `proxy.pool` sẽ làm con số phụ thuộc vào cách client tình cờ tới — và
failover là số duy nhất báo trước một member đang cháy.

`Begin(route string)` **không nhận `*http.Request`**: token không thể chạm tới
counter theo cấu trúc, không phải theo review (ADR 0006). Snapshot được render
thẳng lên `/health` không cần auth — chỉ chấp nhận được vì endpoint đó chỉ bind
loopback và chỉ mang số đếm với milli-giây. Hai gate giữ invariant này:
`TestNoRequestTypeReachesThisPackage` (AST: không exported func nào nhận
`*http.Request`, không struct nào chứa field kiểu đó) và
`TestObserverNeverRecordsToken` (gửi token qua header/query/path/body rồi tìm
lại trong snapshot).

Mọi listener giờ đi qua `proxy.ApplyServingLimits`: `ReadHeaderTimeout` 10s,
`IdleTimeout` 5m, `MaxHeaderBytes` 64 KiB. Cố ý **không** đặt `WriteTimeout` —
server này tồn tại để stream, và write deadline sẽ cắt một stream đang chạy
đúng nghĩa.

22 mutation bị giết (9 ở `observe`, 13 ở wiring). Đáng ghi nhớ: mutation
`defer req.End(rec.status, ...)` — `defer` đánh giá đối số ngay tại chỗ — từng
là **bug thật** ở hub, khiến mọi request bị ghi thành 200 bất kể route trả gì;
bắt được nhờ assertion phân loại class, không phải nhờ đọc code.

### 8.8 Còn lại, chưa làm

- Gom bốn pipeline request ở tầng serving (bước 2–3 của
  `BACKEND_ARCHITECTURE_2026-09-07.md`) — việc nhiều ngày, chưa đụng vào.
- `.github/CODEOWNERS`.
- Ba quyết định chỉ Erik chốt được (§6 mục 1–3): signing credentials,
  publication approval, giữ tên npm. Hai cái đầu chặn toàn bộ G2.

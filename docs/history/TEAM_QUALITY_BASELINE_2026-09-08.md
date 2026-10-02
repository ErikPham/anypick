# Đánh giá chất lượng & lộ trình nâng cấp kỹ năng kỹ thuật

Ngày: 2026-09-08 · Phạm vi: `src/go` (Go engine), tham chiếu `src/macos`, `src/tauri`
Người lập: senior review · Nguồn: đo trực tiếp trên working tree, không ước lượng

---

## 0. Baseline đo được

| Chỉ số | Giá trị | Nhận xét |
| --- | --- | --- |
| LOC không tính test | 46.570 | 174 file |
| LOC test | 35.510 | 171 file, 855 hàm test |
| Tỷ lệ test / nguồn | **0,76** | Tốt (ngưỡng lành mạnh ≥ 0,6) |
| Mật độ assertion | **5,0 / hàm test** | Tốt — test có assert thật, không phải smoke |
| `make review` | **xanh** | 46/46 package, gate OK, version sync 1.0.0 |
| Thời gian `make review` | **10 phút 02 giây** | Đã tăng so với mốc 4 phút đã ghi |
| Trùng lặp khối 8 dòng | **6 nhóm** trong 46.570 dòng | Rất tốt |
| `TODO`/`FIXME`/`HACK`/`XXX` | **0** | Xem F4 — đây không hẳn là tin tốt |
| `panic()` ngoài test | 2 (`internal/proxy/server.go`) | Chấp nhận được |
| Hàm ≥ 70 dòng | **65 / 1.788 (3,6%)** | Xem F3 |
| Commit 3 tháng gần nhất | 75 | xem F5 |

**Kết luận ngắn:** về mặt cơ học, codebase này ở mức *trên trung bình ngành*. Test
ratio, mật độ assertion và mức trùng lặp đều tốt; gate tự động và ADR đã có.
Vấn đề của team không nằm ở "viết code chưa sạch" — nó nằm ở **cấu trúc ở một vài
điểm trọng yếu** và **thói quen kiểm soát chất lượng**. Đừng đi dọn style.

## 0.1 Trạng thái xử lý (cập nhật 2026-09-08)

| # | Mức | Việc | Kết quả |
| --- | --- | --- | --- |
| F1 | P1 | Gate xanh giả tạo trong `make paths` | **Xong**, commit `2a15692`. 0 process substitution còn lại; mutation: thêm binary → FAIL + exit 1 |
| F2 | P1 | 99 điểm rò rỉ persistence | **Một phần.** `a.Store` 20 → 3, `Deps.Store` 30 → 8, `a.Hub` 15 → 2, **`a.Bindings` 11 → 0** (tạo `bindings.Service`, đổi field `App.Bindings` → `App.BindingSvc` để thoát bẫy substring; xoá key khỏi ratchet — đã verify `TestFacadeBudgetDoesNotGrow` xanh), **`a.Presets` 3 → 0** (tạo `presets.Service`, đổi field `App.Presets` → `App.PresetSvc` — cùng bẫy substring; `doctor.Deps.Presets` và `tui` route qua `presets.Service`). Tổng **99 → 33**. 33 còn lại đã có bảng điểm gọi chuẩn (§2, "33 điểm còn lại") — cụm tiếp `a.Pools` (7) hoặc 3 cụm `proxy` (4) |
| F3 | P2 | Hàm dài / file lớn | **Ratchet xong** (51) **+ `buildSnapshot` 532 → 78 + `handleMutate` 275 → 68**. Còn 51 hàm khác (`BuiltinDefinitions` 387 bị loại khỏi split — thuần dữ liệu, xem §3) |
| F4 | P2 | Nợ vô hình | **Xong.** §10 sai 3/4 mục (đã xong trong code) → sửa; thêm 2 gate + 2 TODO có tham chiếu |
| F5 | P2 | Tỷ lệ fix/refactor | Không sửa được bằng code — cần quyết định quy trình |
| F6 | P3 | Tên test `cover*`/`_extra*` | **Xong.** 18 file đổi tên theo hành vi; 12 package xanh |

---

## 1. Điểm mạnh — đừng đụng vào

Năm thứ này đang hoạt động. Bất kỳ "cải tiến" nào phá chúng là thoái lui.

1. **AST gate giữ bất biến kiến trúc** (`TestMutatingMethodsTakeContext`,
   `TestNoRequestTypeReachesThisPackage`, `TestNoHandRolledAtomicWrite`). Kiến trúc
   được máy kiểm tra, không phải bằng lời hứa.
2. **Ratchet thay vì refactor lớn** (`facadeBudget`). Chặn được việc xấu đi ngay
   hôm nay trong khi chưa có thời gian sửa tận gốc — đúng tư duy.
3. **Trùng lặp gần bằng không.** 6 khối trên 46.570 dòng nghĩa là abstractions đã
   được rút ra đúng chỗ. Rất ít codebase đạt được.
4. **ADR + `CODE_REVIEW.md` có thật và được dùng.** Có chỗ ghi "tại sao", có nấc
   severity cho finding.
5. **Test có assertion thật** (5,0/hàm, nhóm `cover_extra` thậm chí 5,4 — cao hơn
   trung bình). Không phải test cảnh.

---

## 2. Finding

### F1 · P1 · Gate có thể xanh giả tạo — và đã xanh giả tạo ngay lúc đánh giá

Trong output `make review` hôm nay:

```
checking no build artifact is tracked
grep: Bad '/dev/fd/63': Operation not permitted  OK   every tracked file under src/ is text, ...
```

`Makefile:280`:

```make
nontext="$$(git ls-files src/ | grep -vFxf <(git grep -Il . -- src/ 2>/dev/null) || true)"; \
```

`|| true` gộp **hai trạng thái khác nhau thành một**: `grep` exit 1 = "không có dòng
nào khớp" (kết quả hợp lệ, rỗng) và exit ≥ 2 = **"lệnh bị lỗi"** (kết quả rỗng vì
không chạy được). Cả hai đều thành chuỗi rỗng ⇒ `bad=""` ⇒ in `OK`.

Đã kiểm chứng: cùng lệnh đó chạy bằng `bash` trả về 9 dòng (đúng), chạy trong môi
trường chặn `/dev/fd` trả về rỗng + in `OK`. Nghĩa là gate này ** pass một cách
vô hiệu bất cứ khi nào môi trường không cấp `/dev/fd`** — sandbox, container CI
bị siết, một số image Docker. Đây chính là kiểu lỗi từng đánh gục `make paths`
bằng comment `#` trong recipe: **in FAIL nhưng exit 0**. Cùng một nấc, lệnh khác.

Đây không phải bug lẻ. Đó là **mẫu hình thất bại lặp lại**: gate báo trạng thái
dựa trên kết quả thay vì dựa trên *mã thoát của lệnh kiểm tra*.

**Sửa ngay (3 việc):**
1. Phân biệt exit: `rc=0; out=$$(cmd) || rc=$$?; if [ $$rc -gt 1 ]; then echo "FAIL gate itself errored"; fail=1; fi`
2. Bỏ `|| true` ở mọi chỗ nó đang che exit ≥ 2. Rà toàn `Makefile`.
3. Thêm **mutation-check cho chính gate này**: tạm `git add` một file binary vào
   `src/` (ví dụ `cp /bin/ls src/_probe.bin`), chạy `make paths`, phải thấy FAIL,
   rồi gỡ ra. Nếu không FAIL, gate vô dụng. Quy trình này phải là bắt buộc (§3.2).

#### F1 — đã sửa ngày 2026-09-08 (chưa commit)

Thay đổi trong `Makefile`, target `paths`:

| Chỗ | Trước | Sau |
| --- | --- | --- |
| `no build artifact is tracked` | `grep -vFxf <(...)` + `\|\| true` | ghi danh sách text ra `mktemp`, kiểm `git grep` rc, kiểm `grep` rc, kiểm `tracked` rỗng |
| `no Go source is git-ignored` | `git check-ignore … \|\| true` | kiểm `find` rỗng, kiểm `git check-ignore` rc |
| 4 check tham chiếu (SRCROOT, asset, Tauri icon, working-directory) | `done < <(...)` | ghi ra `mktemp` với `set -o pipefail`, kiểm rc, kiểm **file kết quả rỗng** |

Không còn process substitution nào trong `Makefile` (0 chỗ).

**Kiểm chứng bằng mutation (đã chạy, không phải giả định):**
- `git add -f src/_gate_probe.bin` (file có NUL) → `FAIL tracked non-text file(s) … src/_gate_probe.bin`, `path check FAILED`, **exit 1**. Gỡ ra → OK trở lại.
- `make paths MACOS_DIR=src/zz` (không có pbxproj) → SRCROOT check in
  `FAIL no SRCROOT reference extracted` (bắt bởi lớp **rỗng**), asset check in
  `FAIL could not extract … (pipeline exited 2)` (bắt bởi lớp **rc**).
- `make paths` bình thường: output không đổi, `path check OK`.

**Bài học mới, quan trọng hơn cả bản vá:** sửa lần đầu của tôi vẫn hỏng. Tôi chỉ
kiểm `rc > 1`, nhưng **BSD `sed` thoát 1** khi thiếu file input — trong khi
`grep` thoát 1 lại nghĩa là "không có dòng nào khớp". Cùng một mã, hai nghĩa,
tuỳ công cụ. Lần nữa: **mã thoát không phải là hợp đồng ổn định giữa các công
cụ.** Thứ thật sự bắt được lỗi là lớp thứ hai — *"kết quả rỗng ⇒ check này chưa
chứng minh được gì ⇒ FAIL"*. Lớp rc chỉ là phòng tuyến thứ hai.

Hệ quả cho §3.2: **đừng bao giờ chỉ dựa vào ngưỡng mã thoát.** Luôn kèm một
khẳng định về *kết quả*: bao nhiêu mục đã được kiểm tra, và có đúng không.

### F2 · P1 · 99 điểm rò rỉ persistence — ratchet là băng cầm máu, không phải thuốc

`internal/app/facade_test.go` — ratchet đã bị **thay bằng AST gate** (2026-09-08):

```go
// 7 pattern đã route xong qua service → cấm cứng (bất kỳ xuất hiện nào là build fail)
var facadeForbidden = []string{
	"a.Bindings", "a.Presets", "a.Pools", "a.Runtime.Profiles",
	"Deps.Proxy.Pools", "a.Leases", "Deps.Proxy.Jobs",
}

// 3 pattern còn lại là irreducible (transfer/inspection/modelcache là peer của store,
// activation.Deps cầm raw hub store) → budget co lại, không cho tăng
var facadeBudget = map[string]int{
	"a.Store":   3, // transfer.ExportEnvelope + wiring desktop.Deps (accounts.go, main.go)
	"Deps.Store": 8, // transfer/inspections + nil guard + modelcache *sql.DB (handlers.go, server.go)
	"a.Hub":     2, // wiring desktop.Deps (main.go, misc.go)
}
```

Gate duyệt `cmd/anypick` + `internal/desktop` (bỏ `_test.go`), parse mỗi file bằng
`go/ast` và tái dựng selector chain (`s.Deps.Proxy.Pools`) từ cây phân tích — đếm
theo **cấu trúc**, không phải substring nguồn. Nên:
- Mẹo rename `b.srv.Deps.Store` (đã từng làm ratchet thụt từ 30 → 25) bị bắt.
- Mẹo substring trong comment / string literal không lọt.
- 7 pattern về 0 bị **cấm cứng** — không thể creep back (khác với ratchet cũ chỉ
  cảnh báo "xóa key").

**Tổng còn 13 chỗ irreducible** (từ 99 ngày 2026-09-08): 7 đã route xong bị cấm
cứng + 3 budget (a.Store 3, Deps.Store 8, a.Hub 2). Comment trong file tự nói rõ:
*`"App exposes every store, so the CLI and the desktop protocol can reach
persistence directly instead of going through a service… AGENTS.md's 'CLI/TUI call
services, never stores' has never been enforceable: the type system offers no
objection."`* Nhưng giờ ít nhất việc xấu đi bị chặn bằng loại, và 7 nhánh đã xong
không bao giờ lọt lại.

Nói thẳng: **quy tắc layering đang được giữ bằng cách đếm, không phải bằng kiểu.**
Ratchet này ngăn nợ *tăng*, nhưng 99 điểm rò rỉ có nghĩa là mọi thay đổi schema,
mọi thay đổi locking, mọi thay đổi transaction boundary đều có 99 chỗ để quên. Đây
là nguồn gốc hợp lý nhất của tỷ lệ `fix` 33% ở F5.

**Sửa:** đừng refactor 99 chỗ một phát. Chọn đúng thứ tự:
- Bước 1: `internal/accounts` đã có `Service` và đã hấp thụ được 2 read của Proxy
  Hub. Tiếp tục route `a.Store` (20) qua accounts/activation service — đây là nhóm
  có semantics rõ nhất.
- Bước 2: Khi một pattern về 0, **xóa key khỏi map** (gate đã làm). Đừng để key
  chết nằm đó.
- Bước 3: Sau khi `a.Hub` (15) và `a.Bindings` (11) về 0, thay ratchet bằng gate
  thật: forbid import `internal/store` từ `cmd/anypick` và `internal/desktop`
  (AST gate kiểu `TestNoRequestTypeReachesThisPackage` đã chứng minh cách làm).
  Khi đó xóa hẳn `facadeBudget`.

Mục tiêu công khai: **99 → ≤ 40 trong 30 ngày, kiến trúc gate thay ratchet trong 90 ngày.**

#### Đã làm — 99 → 85

Đo chính xác trước: `a.Store` = 20 và **tất cả nằm trong `cmd/anypick`**
(`internal/desktop` = 0), trong đó 14 route được qua `accounts.Service` ngay:

| Call site | Số | Đích |
| --- | --- | --- |
| `a.Store.ListAccounts` | 9 | `a.Accounts.List` |
| `a.Store.GetActive` | 3 | `a.Accounts.Active` (method mới, chỉ đọc — không dùng `Current`, vì `Current` kéo theo probe live-auth) |
| `a.Store.Get` | 2 | `a.Accounts.Get` |

6 còn lại không phải accounts: 2 wiring trường struct (`Store: a.Store`),
2 `GetProxyConfig`, 1 `ReadProxyState`, 1 `transfer.ExportEnvelope`. Ba cái liên
quan proxy cần một read surface trên proxy service — mở rộng `accounts.Service`
để nuốt chúng sẽ là sai ranh giới.

Budget hạ `a.Store` 20 → 6 trong cùng commit (ratchet không nói dối), tổng
**99 → 85**. Build + vet + `TestFacadeBudgetDoesNotGrow` xanh.

**Còn 60. `Deps.Store` đã xử lý: 30 → 8.** Tổng **99 → 60**.

22/30 là đọc account hoặc proxy mà một service đã có hoặc nên có:

| Call site | Số | Đích |
| --- | --- | --- |
| `ListAccounts` | 9 | `accounts.Service.List` |
| `GetActive` | 4 | `accounts.Service.Active` |
| `GetProxyConfig` | 4 | `proxy.Service.ProxyConfig` |
| `Get` | 2 | `accounts.Service.Get` |
| `PatchMetaContext` | 2 | `accounts.Service.PatchMeta` (method mới) |
| `ReadProxyState` | 1 | `proxy.Service.RuntimeState` |

`accounts.Service.PatchMeta` là method mới. Mutator của store đã tự lấy
coordinator lock, nên caller đang giữ cùng provider scope sẽ re-enter thay vì
deadlock — đúng trường hợp của cả hai call site, đều nằm trong closure
`WithMutationLock`.

8 còn lại là không thể giảm, và giờ đã ghi rõ lý do trong comment để không ai
mất cả buổi chiều đi dò lại: `transfer.ExportEnvelope`,
`transfer.ExportSelectedBackup`, `inspections.Inspect`, `inspections.Restore` —
các subsystem transfer/inspection là *ngang hàng* với store chứ không phải
caller của nó; 1 wiring trường struct; 1 nil guard; 1 `s.Deps.Store.DB` đưa cho
modelcache (cần `*sql.DB`, không phải account).

`hub_snapshot.go` không đụng tới — session khác đang mở.

**`a.Hub` đã xử lý: 15 → 2. Tổng 60 → 47.** `internal/hub` chưa có service nào,
nên CLI cầm thẳng store. Tạo `hub.Service` với 4 method ctx-first:

| Call site | Số | Đích |
| --- | --- | --- |
| `GetConfigContext` | 11 | `hub.Service.GetConfig` |
| `UpsertConfigContext` | 2 | `hub.Service.UpsertConfig` |
| `SetModelOwnerContext` | 1 | `hub.Service.SetModelOwner` |
| `PreviewManifest` | 1 | `hub.Service.PreviewManifest` |

`GetConfig` đáng chú ý: nó thay thế `store.GetConfig` — **method đọc không ctx
cuối cùng còn với tới được từ CLI**.

2 còn lại là wiring struct (`Store: a.Hub`, `Hub: a.Hub`), chính đáng.

#### Bẫy substring, lần hai — và lần này là ở tên mới

Tôi đặt field là `a.HubService`. Gate đếm chuỗi `"a.Hub"` — và **`"a.HubService"`
chứa `"a.Hub"`**, nên 13 call site đã chuyển sang service mà con số vẫn đứng
im ở 15. Đổi tên field thành `ProxyHub` (không bắt đầu bằng `Hub`) thì pattern
không ăn nhầm, đếm đúng 2.

Cùng một bẫy với `"s.Deps.Store"` ở trên, chiều ngược lại. **Quy tắc đầy đủ:**
pattern của gate đếm chuỗi vừa phải độc lập receiver, vừa phải không phải tiền
tố của tên mới mà refactor sẽ tạo ra. Đã ghi vào comment ngay tại field.

#### Tiếp: `a.Store` 6 → 3

Thêm `proxy.Service.ProxyConfig` và `proxy.Service.RuntimeState`. Đây là ranh giới
đúng — `proxy.Service` **đã sở hữu** `Store *store.AccountStore`, nên proxy config
và proxy runtime state là việc của nó, không phải của `accounts.Service`. Route
2 `GetProxyConfig` (`misc.go`, `lifecycle.go`) và 1 `ReadProxyState` (`pool.go`).

3 còn lại là thật, không phải lười: 2 wiring trường struct (`Store: a.Store`) và
1 `transfer.ExportEnvelope` — cả ba cần chính store, không phải một service đọc.

#### Bẫy: ratchet đếm chuỗi có thể nói dối — và nó đã nói dối

Sau khi tách `buildSnapshot` và `handleMutate`, `TestFacadeBudgetDoesNotGrow` báo:

```
s.Deps.Store shrank from 30 to 25
```

**Không có sự ghép nối nào biến mất.** Các call site trong `snapshot.go` giờ viết
`b.srv.Deps.Store`, nên chuỗi `"s.Deps.Store"` không còn khớp — 5 lần. Ratchet
đang đo *cách tôi đặt tên biến*, không phải kiến trúc. Nếu hạ budget xuống 25 lúc
đó, tôi đã ghi nhận một tiến bộ chưa từng xảy ra.

Đo lại cho thấy rõ:

| Pattern | Đếm | Nhận xét |
| --- | --- | --- |
| `s.Deps.Store` | 25 | phụ thuộc receiver — sai |
| `Deps.Store` | **30** | độc lập receiver — đúng, và không đổi |

Sửa: đổi key của map sang `"Deps.Store"` (và `"Deps.Proxy.Pools"`,
`"Deps.Proxy.Jobs"`), giữ nguyên 30.

**Quy tắc rút ra, áp dụng cho mọi gate đếm chuỗi:** khớp vào *sự ghép nối*, đừng
khớp vào *receiver*. Bất kỳ refactor nào đổi tên biến tiếp nhận đều làm gate mù
theo cách rất khó thấy — vì gate vẫn xanh, chỉ là nó đang đếm thứ khác.

#### Đã làm — 47 → 33

**`a.Bindings` đã xử lý: 11 → 0. Tổng 47 → 36.** Tạo `bindings.Service` (bản
song song với `hub.Service`): `type Service struct{ Store *Store }` + `NewService`,
forward 10 method ctx-first. Đổi field `App.Bindings` → `App.BindingSvc` — **không
đặt tên `a.BindingsSvc`** vì chuỗi `"a.Bindings"` là tiền tố, sẽ vẫn bị đếm. CLI
(`bindings.go`, `accounts.go`, `main.go`), TUI (`data.go`, `model.go`) và desktop
(`server.go`) đều qua `bindings.Service`; `activation.Deps.Bindings` giữ raw
`*bindings.Store` (inner layer). Xoá key `"a.Bindings"` khỏi `facadeBudget`.

**`a.Presets` đã xử lý: 3 → 0. Tổng 36 → 33.** Tương tự: tạo `presets.Service`,
đổi `App.Presets` → `App.PresetSvc` (cùng bẫy prefix `"a.Presets"`). `cmd/anypick`
(`bindings.go` 3 call site), `internal/tui` (`data.go` 2, `model.go` 1) và
`doctor.Deps.Presets` đều qua `presets.Service`. Lưu ý `UpsertContext` mang tham số
`bindings.Spec` nên `service.go` phải import `internal/bindings`. `activation.Deps.Presets`
dùng `*presetsView` riêng — không động. Xoá key `"a.Presets"` khỏi `facadeBudget`.
Build + vet + `TestFacadeBudgetDoesNotGrow` xanh; e2e `cmd/anypick` xanh (~31s).

#### 33 điểm còn lại — bảng điểm gọi chuẩn (đo 2026-09-08)

Ratchet chỉ quét `cmd/anypick` + `internal/desktop`. `internal/tui` cũng cầm
`a.Bindings` / `a.Presets` / `a.Runtime.Profiles` trực tiếp (`data.go`, `model.go`)
nhưng **không nằm trong `facadeLayers`**, nên không tính vào budget — và sẽ vỡ
compile nếu đổi tên field `App` mà không sửa tui cùng lúc. Điểm then chốt: chỉ
`hub` có service; bốn cụm kia (`bindings`, `pools`, `presets`, `profile`) **chưa
có**, nên mỗi cụm cần tạo service boundary trước khi route.

| Pattern | Số | File (call site) | Hàm gọi | Cần service |
| --- | --- | --- | --- | --- |
| `a.Bindings` | 11 | `bindings.go` (8), `accounts.go` (1), `main.go` (2: 603, 1108) | `GetGlobalContext`, `CountProjectsContext`, `GetProjectContext`×2, `DeleteAllProjectsContext`, `DeleteProjectContext`, `DeleteGlobalContext`, `RecordSourceResumeContext` | ✅ `bindings.Service` (đã xong, key ratchet xoá) |
| `a.Pools` | 7 | `pool.go` (7) | `GetContext`×4, `Set`×2, `DisableContext` | `pools.Service` (mới) — `Set`×2 là method **không ctx** |
| `a.Presets` | 3 | `bindings.go` (3) | `ListContext`, `GetByNameContext`, `DeleteByNameContext` | ✅ `presets.Service` (đã xong, key ratchet xoá) |
| `a.Runtime.Profiles` | 9 | `main.go` (4), `misc.go` (1), `accounts.go` (4) | `Upsert`/`List`/`Get`/`DeleteContext` | `profile.Service` (mới) |
| `a.Leases` | 1 | `pool.go:376` | `ListPoolLeasesContext` | thuộc `proxy` → mở rộng `proxy.Service` |
| `Deps.Proxy.Pools` | 2 | `handlers.go` (132 nil-check, 137) | `GetContext` | thuộc `proxy` → mở rộng `proxy.Service` |
| `Deps.Proxy.Jobs` | 1 | `handlers.go:176` | `ListPoolLeasesContext` | thuộc `proxy` → mở rộng `proxy.Service` |

**Thứ tự route rẻ nhất:** 3 cụm `proxy` (`a.Leases` + `Deps.Proxy.Pools` +
`Deps.Proxy.Jobs` = 4 điểm) thuộc `proxy.Service` đã sở hữu store → mở rộng
`proxy.Service` lấy chúng trước, không tạo package mới. `a.Presets` (3) đã xong
(route qua `presets.Service`). Rồi `a.Pools` (7) — read-heavy, semantics rõ. `a.Bindings` (11) đã xong (route qua `bindings.Service`). Còn
`a.Runtime.Profiles` (9): hai cụm có mutator và quyết định lock riêng, nên cần
service riêng thật sự.

**Bẫy substring áp dụng lại:** khi tạo service và đổi tên field `App`, tên mới
phải **không chứa** pattern đang đếm (cùng bài học `a.Hub` / `Deps.Store`). Đổi
`a.Bindings` → `a.BindingSvc` (không có chuỗi `"a.Bindings"`), `a.Pools` →
`a.PoolSvc`, v.v. Nếu giữ tiền tố (`a.BindingsSvc`) gate vẫn đếm và budget không
xuống — đã ghi vào comment ngay tại mỗi field `App` (xem `app.go:51` cho
`ProxyHub`).

### F3 · P2 · Trọng lượng cấu trúc dồn vào rất ít điểm

Hàm dài nhất (không tính test):

| Dòng | Hàm | Vị trí |
| --- | --- | --- |
| 532 | `buildSnapshot` | `internal/desktop/snapshot.go:206` |
| 387 | `BuiltinDefinitions` | `internal/provider/compatible.go:316` |
| 275 | `handleMutate` | `internal/desktop/server.go:671` |
| 268 | `PlanActivation` | `internal/activation/planner.go:305` |
| 259 | `cmdHub` | `cmd/anypick/hubcmd.go:21` |

File lớn nhất: `desktop/server.go` 1.466, `proxy/service_plugin.go` 1.248,
`cmd/anypick/main.go` 1.148, `desktop/snapshot.go` 1.124, `tui/model.go` 1.072.
Package: `proxy` 6.841 · `provider` 5.525 · `desktop` 4.747 · `cmd/anypick` 4.466.

65 hàm ≥70 dòng chỉ là 3,6% — nhưng chúng nằm đúng ở những chỗ có churn cao nhất
6 tháng qua (`pool_server.go` 9 lần, `upstream_serve.go` 8, `server.go` 8,
`service.go` 7). **Độ dài × tần suất sửa = xác suất hỏng.** Đây là danh sách cần
giảm, không phải toàn bộ codebase.

`buildSnapshot` đặc biệt đáng xử lý: nó vừa dựng state vừa đăng ký action handler
vào một `registry` trong cùng một hàm 532 dòng. Tách theo miền
(`snapshotAccounts`, `snapshotProxies`, `snapshotHub`, …) mỗi hàm trả về rows +
registry con. Đây là refactor an toàn nhất để bắt đầu vì có test snapshot bao phủ.

**Chuẩn đề xuất:** hàm > 80 dòng cần lý do trong comment; > 150 dòng không được
merge mới. Hiện tại 65 hàm vi phạm mức 80 — đưa vào ratchet (§3.3) đếm *số lượng*,
không sửa hết ngay.

#### Đã làm

**1. Ratchet** `TestLongFunctionCountDoesNotGrow` trong `internal/app/debtgate_test.go`:
đếm bằng `go/ast` (không phải đếm dòng thủ công — hai cách cho hai kết quả khác
nhau: 49 vs **53**, AST là chuẩn). Mutation-check: thêm một hàm 96 dòng →
`54 functions exceed 80 lines` → FAIL; xóa → PASS.

**2. `buildSnapshot` đã tách: 532 → 78 dòng.** Đây là hàm dài nhất cây, nằm trong
file có churn cao nhất, và nó vừa dựng state vừa đăng ký handler vào một registry.

Cách tách: đưa bốn biến địa phương (`snap`, `registry`, `actionID`, `rev`) vào một
`snapshotBuilder`, rồi mỗi miền thành một method. Thứ tự gọi trong
`buildSnapshot` giờ là danh sách đọc được:

| Method | Dòng | Method | Dòng |
| --- | --- | --- | --- |
| `buildSnapshot` | **78** | `gatewayProviders` | 75 |
| `accountsAndProxies` | 65 | `currentRouteActions` | 73 |
| `addProxyRow` | 43 | `crossProviderActions` | 59 |
| `accountProviders` | 48 | `gatewayActions` | 41 |
| `settings` | 23 | `routes` | 38 / `gateways` | 20 |

Không có method nào vượt 80. Ratchet hạ 53 → **52**.

Bốn lỗi compile hiện ra khi làm, tất cả do refactor cơ học chứ không phải do
thiết kế — đáng ghi lại vì bất kỳ ai tách hàm lớn sẽ gặp y như vậy:
- `shortNameOf(s, pv)` không bị regex `\bs\.` bắt (tham số đứng riêng, không có
  dấu chấm) ⇒ phải sửa tay;
- xóa dòng đã transform khỏi văn bản chưa transform ⇒ biến `cfg` thành "declared
  and not used";
- method trả `error` tách từ thân hàm có `return nil, err` ⇒ thiếu `return nil`
  cuối;
- một chỗ dùng `registry` còn sót ở phần đuôi giữ nguyên.

Verify: `go build ./...`, `go vet ./...`, `go test ./internal/desktop/` (28,2s) xanh.

**3. `handleMutate` đã tách: 275 → 68 dòng** (`desktop/server.go`). Cùng ngày.

Nó là một `switch` gồm 24 nhãn; phần lớn đã uỷ quyền sang `handleXxx`, nhưng 11
nhãn còn giữ thân inline. Mỗi nhãn thành một method nhận và trả `Result`:

| Method | Dòng | Method | Dòng |
| --- | --- | --- | --- |
| `handleMutate` | **68** | `mutateGatewayCreate` | 43 |
| `mutateProxyStopAll` | 49 | `mutateGatewayEdit` | 33 |
| `mutateLaunchAtLogin` | 25 | `mutateAutoStartProxies` | 20 |
| `mutateQuotaGuard` | 20 | các method còn lại | 10–14 |

Quy tắc chuyển đổi: **`break` trong case ≡ `return result` trong method.** Đó là
lý do ca này an toàn — case kết thúc bằng rơi xuống `s.write(FrameResult(result))`,
đúng như method trả về `result`.

Một case không theo quy tắc và phải xử lý riêng: `setting-launch-at-login` có
`return nil` ở giữa để bỏ qua việc ghi frame (kết quả đến sau qua host effect).
Nó thành method trả `(Result, bool)`, chỗ gọi là
`if result, terminal = s.mutateLaunchAtLogin(...); terminal { return nil }`.

Hai lỗi compile hiện ra, cả hai từ chính case ngoại lệ đó — một `break` ở
indentation khác nên regex không đổi được, và một `return result` thiếu `false`.
Build bắt cả hai. Verify: build + vet + `go test ./internal/desktop/` (27,5s) xanh.

Ratchet: 52 → **51**.

#### Đã loại — `BuiltinDefinitions` (387) không tách

`provider/compatible.go:316` là hàm dài nhất cây (387 dòng, một slice literal
chứa 23 vendor). **Quyết định giữ nguyên** (2026-09-08): nó là bảng dữ liệu thuần
— không rẽ nhánh, không logic, mọi caller chỉ đọc, và có test validate từng
entry. Chặt nó thành vài khối theo miền chỉ thoả mãn metric ratchet trong khi
làm code tệ đi (khó nhảy tới một vendor, mất cái nhìn toàn cảnh 23 vendor liền
kề). Ratchet vẫn đếm nó — **chấp nhận**, vì tiêu chí F3 là "độ dài × churn", và
`BuiltinDefinitions` hầu như không đổi (dữ liệu tĩnh, không nằm trong top churn
6 tháng).

**Tiếp theo (còn 51 hàm > 80 dòng): nhắm theo churn, không theo độ dài thuần.**
Hai lớn nhất nằm đúng chỗ F3 cảnh báo (file có churn cao nhất: `pool_server.go` 9
sửa, `upstream_serve.go` 8, `server.go` 8): `PlanActivation` (268,
`activation/planner.go:305`) và `cmdHub` (259, `cmd/anypick/hubcmd.go:21`). Đây
là ứng cử viên split thực sự tiếp theo — có logic rẽ nhánh, không phải dữ liệu.

### F4 · P2 · Nợ kỹ thuật vô hình trong code

**0 TODO / FIXME / HACK trên toàn bộ 46.570 dòng.** Nghe có vẻ lý tưởng; thực tế là
tín hiệu xấu, vì cùng lúc đó `CODE_REVIEW.md` §10 đang mở **7 quyết định chưa chốt**
(signing creds & publication, giữ tên npm, lease TTL, ring deadline, ADR cho
bindings, lịch hạ `facadeBudget`), cộng `internal/bindings` chưa lock,
`internal/store` còn 6 ctx-less read wrapper.

Hai hệ quả: (1) người mới đọc code không thấy gì phải lo, (2) hai nguồn sự thật
(code vs `CODE_REVIEW.md`) có thể lệch nhau mà không ai bị nhắc.

**Sửa (rẻ, làm ngay):** mọi hạng mục nợ trong `CODE_REVIEW.md` §10 phải có một
`TODO(adr-00xx)` hoặc `TODO(gate:...)` ở đúng vị trí code liên quan. Nợ nằm trong
code thì mới bị người sửa file đó nhìn thấy. Đồng thời gate: **không được xóa
`TODO` nếu chưa có commit tham chiếu giải quyết nó.**

#### Đã làm — và phát hiện §10 đang nói dối 3/4

Kiểm tra từng mục thay vì tin vào danh sách:

| Mục §10 ghi là mở | Thực tế |
| --- | --- |
| `internal/store` còn 6 ctx-less read wrapper | **Đã xong.** Production `internal/store` có **0** `context.Background()`/`TODO()`. Ba method không nhận `ctx` (`rowToAccount`, `PrepareSnapshot`, `DiscardPreparedSnapshot`) đều không tự tạo context. Số dòng trong §10 đã lệch |
| `internal/bindings` chưa khoá coordinator | **Đã xong.** ADR-0022 accepted; `bindings.Store.mutate` gọi `lock.WithMutationLock` (`bindings.go:80`) |
| Thêm `.github/CODEOWNERS` | **Đã có** từ 2026-09-07 |
| Bắt buộc job `Review metadata` trong branch ruleset | **Vẫn mở** — thiết lập hosting, không phải file repo |

Ba mục đầu xong trong code nhưng vẫn hiện "open" trên giấy. Đó chính là drift mà
F4 cảnh báo, và nó đã xảy ra. Số dòng cite trong danh sách nợ là thứ hỏng trước
tiên: `account_store.go:91` trỏ vào một hàm **đã có ctx**.

Hai gate mới trong `internal/app/debtgate_test.go` (đều đã mutation-check):
- Mọi marker trong `src/go` phải có tham chiếu `TODO(adr-NNNN)` hoặc
  `TODO(gate:...)`. Marker không tham chiếu là nợ vô chủ.
- Số mục mở trong §10 không được vượt `openDebtBudget` (hiện 1). Thêm mục mới →
  build fail. Khi đóng mục thì hạ budget, không để key chết.

Gate đầu tự bắt chính nó khi viết: `\bTODO\b` bắt cả prose trong file gate. Sửa
bằng cách khớp `TODO(`/`TODO: ` và lắp pattern từ `const wordTODO = "TODO"` để
file gate không chứa marker literal — theo đúng cách codebase này đã giải quyết
với `proxy.StatusRecorder`, thay vì thêm exemption "bỏ qua file này".

### F5 · P2 · Tỷ lệ commit: 33% fix / 25% feat / 7% refactor / 1% test

75 commit trong 3 tháng: `fix` 25 (33%), `feat` 19 (25%), `chore` 11 (15%),
`docs` 8 (11%), `other` 6 (8%), `refactor` 5 (7%), `test` 1 (1%).

Cần đọc đúng: nhiều commit `fix` có thể là tự sửa ngay trong cùng phiên làm việc
(cây này có nhiều session sửa song song), nên không thể kết luận "33% là bug thoát
ra production". Nhưng tỷ lệ **fix gấp ~4,7 lần refactor** vẫn là tín hiệu: team
đang trả giá cho cấu trúc hiện tại lặp đi lặp lại, trong khi hầu như không dành
thời gian giảm nguyên nhân. `test` 1% cũng thấp so với việc codebase sống nhờ gate.

**Sửa:** đặt ngân sách cố định, không phải ý tốt: **mỗi 4 commit tính năng có 1
commit refactor hoặc test.** Với F2 là nguyên nhân lớn nhất, refactor nên ưu tiên
route call site qua service.

### F6 · P3 · Tên test nói sai về mục đích của nó

18 file (`cover_extra_test.go`, `cover2_test.go`, `cover3_test.go`,
`*_extra_test.go`) chiếm 4.984 LOC = **14% toàn bộ code test**.

Đo kiểm chứng: mật độ assertion của nhóm này **5,4/hàm, cao hơn trung bình 5,0**.
Chất lượng thực tế **ổn** — tôi đã định kết luận đây là test rượt coverage và số
liệu không ủng hộ kết luận đó. Vấn đề chỉ là **tên**: `cover*` nói với người đến
sau rằng "file này tồn tại để nâng coverage", tức là nơi an toàn để vứt test vô.
Đó là lời mời suy thoái, chưa phải suy thoái.

**Sửa:** đổi tên theo hành vi được kiểm chứng (`account_row_sync_test.go`,
`ring_deadline_test.go`…). Rẻ, không rủi ro, làm dần mỗi khi đụng vào file.

#### Đã làm

18 file đổi tên bằng `git mv` theo nội dung thật của chúng, ví dụ
`internal/proxy/cover_extra_test.go` → `context_window_test.go` (toàn test về
`IsContextWindowError` và SSE frame), `internal/desktop/protocol_extra_test.go` →
`decode_line_test.go`, `internal/store/cover_extra_test.go` →
`proxy_config_and_paths_test.go`.

Verify: `go build ./...` và `go vet ./...` OK; 12 package bị ảnh hưởng chạy test
xanh (accounts 8,6s · desktop 30,6s · proxy 19,5s · provider 9,9s …). Còn đúng
**0** file `cover*`/`*_extra*` — kết quả 1 ban đầu là do `dis**cover**y`.

---

## 3. Chuẩn kỹ thuật đề nghị ban hành

### 3.1 Definition of Done (áp dụng cho mọi PR)

- [ ] `make review` xanh (không được chỉ đọc dòng cuối — xem §3.2)
- [ ] Test mới **có assert kiểm chứng hành vi**, không chỉ gọi hàm cho đủ coverage
- [ ] Nếu sửa bug: có test fail trước khi sửa, pass sau khi sửa
- [ ] Hàm mới ≤ 80 dòng; nếu dài hơn, có comment nêu lý do
- [ ] Không thêm call site `a.Store` / `s.Deps.Store` / `a.Hub` từ `cmd/anypick`
      hay `internal/desktop` — đi qua service
- [ ] Nợ mới để lại: có `TODO(adr-00xx)` hoặc `TODO(gate:...)` tại chỗ
- [ ] Mọi quyết định kiến trúc mới: ADR, không sửa ADR đã accepted

### 3.2 Quy tắc viết gate (bắt buộc — F1 là bằng chứng)

1. **Không `|| true` trên lệnh kiểm tra.** Phân biệt exit 0/1 (kết quả) và ≥ 2 (lỗi).
   **Và đừng tin ngưỡng đó một mình** — BSD `sed` thoát 1 khi thiếu file, `grep`
   thoát 1 khi không khớp. Luôn kèm khẳng định về kết quả (F1).
2. **Mutation-check mọi gate mới:** phá code → thấy FAIL → khôi phục → thấy PASS.
   Gate chưa từng FAIL là gate chưa được chứng minh.
3. Gate **không có exemption**. Gate có exemption sẽ thối.
4. **Đừng `tail` output gate** — quy tắc đã có trong memory, từng làm mất 5 finding.
   Đọc toàn bộ output hoặc grep `FAIL`.
5. Kiểm tra chính gate, không chỉ kiểm tra code bị gate.

### 3.3 Ratchet: dùng đúng cách

Ratchet tốt để **chặn xấu đi**, không dùng để **né sửa**. Mọi ratchet đang có phải
ghi rõ: giá trị hiện tại, mục tiêu, ngày hạ, và điều kiện để xóa nó hẳn.
`facadeBudget` hiện thiếu ngày mục tiêu — thêm vào.

### 3.4 Review

- Self-review ≠ independent review. Guard fail ngay lần đầu là guard đang hoạt động.
- Reviewer phải trả lời được: *nếu đoạn này sai, cái gì bắt được nó?* Nếu câu trả
  lời là "không có gì", cần test hoặc gate, không cần comment.

---

## 4. Kế hoạch 30 ngày

| Tuần | Việc | Kết quả kiểm chứng được |
| --- | --- | --- |
| 1 | F1: sửa `Makefile:280`, rà hết `|| true`, thêm mutation-check cho `make paths` | `make paths` FAIL khi cố tình `git add` binary vào `src/` |
| 1 | F4: gắn `TODO(adr-…)` cho 7 mục §10 tại đúng vị trí code | grep thấy 7 TODO mới |
| 2 | F2 bước 1: route `a.Store` (20) qua accounts/activation service | budget 99 → ≤ 79 |
| 2 | F3: thêm ratchet đếm số hàm > 80 dòng (hiện 65), không cho phép tăng | gate mới, đã mutation-check |
| 3 | F2 tiếp: `a.Hub` (15) + `a.Bindings` (11) | budget → ≤ 53 |
| 3 | F3: tách `buildSnapshot` (532) theo miền | hàm ≤ 150 dòng, test snapshot vẫn xanh |
| 4 | F2: `a.Pools` (7) + `a.Presets` (3) + `a.Runtime.Profiles` (9) | budget → ≤ 40 |
| 4 | F6: đổi tên 18 file `cover*`/`*_extra*`; đo lại thời gian `make review` | tên theo hành vi; review < 10 phút |

Bổ sung sau khi đo: F3 `BuiltinDefinitions` (387) **quyết định không tách** —
bảng dữ liệu thuần, ratchet vẫn đếm, chấp nhận (xem §3). Nhắm split tiếp theo
theo churn: `PlanActivation` (268), `cmdHub` (259).

**F2 — HOÀN THÀNH 2026-09-08** (vượt mục tiêu ≤ 40):
- 7 cụm đã route xong qua service và bị **cấm cứng** bằng AST gate:
  `a.Bindings`, `a.Presets`, `a.Pools`, `a.Runtime.Profiles`, `Deps.Proxy.Pools`,
  `a.Leases`, `Deps.Proxy.Jobs`.
- 3 budget còn lại là irreducible (transfer/inspection/modelcache là peer của
  store, activation.Deps cầm raw hub store): `a.Store` 3, `Deps.Store` 8, `a.Hub` 2.
- Ratchet substring (`facadeBudget`) đã bị **thay thế** bằng AST gate duyệt
  cấu trúc (`go/ast`), không thể creep back. Chi tiết code: `internal/app/facade_test.go`.
- Cách làm: mỗi cụm → tạo `Xxx.Service` (type `Service struct{ Store *Store }` +
  `NewService`), đổi field App thành `XxxSvc` (tránh substring trap), route mọi
  caller ngoài qua service, xoá key khỏi budget. `proxy.Service` đã có sẵn
  `Pools`/`Jobs` nên cụm proxy chỉ thêm 4 method delegate.

Còn lại (không thuộc F2, cần refactor riêng): route `transfer`/`inspections`/
`modelcache` qua service thay vì cầm raw store — khi đó 3 budget cuối mới về 0
và xoá hẳn.

---

## 5. Một câu để nhớ

Cơ chế của team đã tốt hơn đa số: gate xanh, test có assert, trùng lặp ~0, có ADR.
Thứ đang làm chậm không phải là code bẩn — mà là **99 điểm rò rỉ persistence chỉ
được giữ bằng cách đếm**, và **thói quen tin vào gate mà không kiểm chứng gate có
thật sự chạy**. Sửa hai thứ đó trước, mọi thứ khác thành dễ.

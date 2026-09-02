# Audit: cơ chế review của AnyPick

*Ghi chú nội bộ, cập nhật 2026-09-01. Không phải chuẩn của repo — chuẩn là
`CODE_REVIEW.md` (tiếng Anh, cùng `AGENTS.md` và PR template). Tài liệu này là báo cáo
đọc cho người, viết tiếng Việt theo thoả thuận; muốn commit thì phải dịch sang tiếng Anh
cho đồng nhất. Xoá đi cũng được — giá trị của nó đã được chuyển vào `CODE_REVIEW.md` §10.*

---

## 1. Kết luận

**AnyPick không thiếu chuẩn review. Nó thiếu khả năng thi hành chuẩn đó.**

`CODE_REVIEW.md` là tài liệu review tốt hiếm thấy ở dự án cá nhân. Viết thêm một tài liệu
chuẩn nữa sẽ là **P1**: tạo authority thứ hai, vi phạm §3.

Bốn nguyên nhân làm chất lượng không đồng đều, theo thứ tự tác động:

| # | Nguyên nhân | Trạng thái |
| --- | --- | --- |
| 0 | `.gitignore` nuốt `src/go/internal/refs` → **`main` không compile được** | **Đã sửa** |
| 1 | Chuẩn review chưa bao giờ được commit | **Đã sửa** |
| 2 | Gate bắt buộc không chạy được (`make review` exit 127) | **Đã sửa** |
| 3 | Không có nơi ghi nhận independent review | **Đã sửa** |

---

## 2. 🔴 P0-0 — `.gitignore` làm `main` không build được *(nghiêm trọng nhất)*

**Bằng chứng**

```
$ git check-ignore -v src/go/internal/refs
.gitignore:33:refs/	src/go/internal/refs
```

`.gitignore` dòng 33 có pattern trần `refs/`, viết cho thư mục screenshot ở gốc. Vì
không có dấu gạch chéo đầu, git match nó ở **mọi độ sâu** — nên nó nuốt luôn
`src/go/internal/refs`, một package Go production thật.

**Phạm vi:** 45 file đã commit import package này, trong đó có
`cmd/anypick/main.go`, `activation/planner.go`, `activation/executor.go`,
`store/account_store.go`, `bindings/bindings.go`, `proxy/serve_child.go`.

**Ba sự im lặng cùng lúc — đây là điểm làm nó đắt:**

1. `main` không compile: `internal/proxy/serve_child.go` import một package không có
   trong tree.
2. `git status` **không nói gì**. File bị ignore là vô hình, không chỉ là untracked —
   nên câu hỏi "mọi thứ đã commit chưa?" vẫn trả lời là "rồi".
3. `refs.ProviderScope` — primitive khoá scope đứng sau ADR 0011 và invariant #5 —
   không hề nằm trong repository. Code quyết định writes nào được serialise đã mất.

**Đã sửa:** neo `/refs/` và `/design/` vào thư mục gốc (hai thư mục gốc mà rule viết cho
vẫn bị ignore như cũ), commit kèm `src/go/internal/refs`. Clean checkout ở `HEAD` giờ:
`build=0`, `vet=0`, `test=0`, **0 FAIL**.

**Gate mới trong `make paths`** (chặn cả lớp lỗi, không chỉ instance này):

- Không `.go` nào dưới `src/go` được phép bị git-ignore.
- Bốn tài liệu authority (`AGENTS.md`, `CODE_REVIEW.md`, `CONTRIBUTING.md`,
  `src/macos/README.md`) phải được track.

**Bẫy tôi tự mắc, đáng nhớ:** `git check-ignore` mặc định **bỏ qua path đã track**, nên
phiên bản gate đầu tiên **không bắt được lỗi** dù mutation trông như "pass". Phải dùng
`--no-index` để hỏi *pattern loại trừ cái gì* thay vì *git đang có cái gì*. Nếu mutation
không làm gate fail, hãy nghi ngờ gate — không phải nghi ngờ lỗi.

---

## 3. 🔴 P0-1 — Chuẩn review chưa bao giờ nằm trên `main` *(đã sửa)*

`?? CODE_REVIEW.md` — vắng mặt trên `HEAD`, trong khi `AGENTS.md`, `CONTRIBUTING.md` và
PR template đều trỏ vào nó. Checkout sạch, CI, và mọi agent bắt đầu từ `main` đều review
dựa trên một chuẩn họ không đọc được.

**Đã sửa:** landed thành commit riêng (`3367543`), tách khỏi worktree 100+ file mà nó
đang kẹt trong đó — chính §4 "one logical change" là lý do nó chưa từng lên được.

**Kèm theo:** toàn bộ bộ máy cưỡng chế cũng chưa được commit —

| Gate được §5/§8 trích dẫn | Trên `main`? |
| --- | --- |
| `internal/db/tables.go` + test (invariant #6) | Đã commit |
| `internal/pubgate/gate_test.go`, `internal/modelcache/store_test.go` | Đã commit |
| `internal/store/coordinator_test.go` (invariant #5) | **Chưa — không land được** |
| `internal/bindings/locking_test.go` (2 ctx guard) | **Chưa — không land được** |
| `cmd/anypick-release/version.go` | **Chưa — không land được** |

---

## 4. 🔴 P0-2 — Gate bắt buộc không chạy được *(đã sửa)*

`~/.zshrc` trỏ `go` vào `/opt/homebrew/opt/go@1.21/bin` — thư mục không tồn tại. Mọi Go
target thoát **127** với `command not found`, trông y hệt "thay đổi làm gãy build", nên
phản ứng tự nhiên là bỏ gate. Và **gate bỏ = gate xanh**.

**CI xanh suốt** vì `actions/setup-go` tự cài Go → CI đã che khuất gate chết ở local.

**Đã sửa:** Makefile tự resolve toolchain (`GO_SEARCH` → `GO` → `GOFMT`), guard
`go-toolchain` fail kèm hướng dẫn cài thay vì 127 (pattern giống `STATICCHECK`).
Mutation-checked cả hai chiều.

---

## 5. 🟡 P1 — Hai lỗ hổng quy trình *(đã sửa)*

- **§1 "self-review ≠ independent review"** — quy tắc quan trọng nhất, nhưng PR template
  không có mục nào hỏi về nó. Đã thêm mục *Independent review*.
- **§8 có tín hiệu sức khoẻ nhưng không ai được phân công đọc.** Đã thêm §11 *Cadence*:
  mỗi tháng, chạy `make review` từ **checkout sạch**; cần export PATH là gate unrunnable.

---

## 6. 🟡 P2 — Ba guard đúng nhưng chưa land được

Đây là kết quả bất ngờ nhất. Ba guard **hoạt động đúng**, nhưng fail — hoặc không
compile — khi thử ở `HEAD` sạch, vì code mà chúng bảo vệ chỉ được sửa trong work chưa
commit của bạn:

| Guard | Lý do fail ở `HEAD` |
| --- | --- |
| `store/coordinator_test.go` | 16 mutator vẫn bypass `s.mutate` (invariant #5) |
| `bindings/locking_test.go` | 6 mutator không nhận `ctx`; 8 `context.Background()` trong production code |
| `cmd/anypick-release/version.go` | thiếu `cargoToml` / `tauriConf` từ thay đổi pending |

Đúng nguyên tắc §5: *guard fail ngay lần đầu là guard đang hoạt động* — đừng nới lỏng
test cho xanh. Ở đây thứ tự phải là **code fix trước, guard sau**. Đã ghi chi tiết vào
`CODE_REVIEW.md` §10.

**Phương pháp** (đã đưa vào §11): dựng worktree sạch ở `HEAD` rồi build/vet, vì "pass
trên máy tôi" vô nghĩa khi fix chưa được commit.

```bash
git worktree add --detach /tmp/head-check HEAD
cd /tmp/head-check/src/go && go build ./... && go vet ./...
git worktree remove --force /tmp/head-check
```

---

## 7. Điểm làm tốt — giữ nguyên

- **38/40 package có test**; hai cái thiếu là thin `main` và hằng số version — hợp lý.
- Gate `node-free` có test cả hai phía (`internal/artifact/nodefree_test.go`).
- CI pin action SHA, pin staticcheck `v0.8.1` có ghi lý do, có
  `concurrency.cancel-in-progress`.
- `CODE_REVIEW.md` §10 tự theo dõi chính nó; §5 "finding thành gate" là thứ làm review
  sinh lãi thay vì lặp lại.

---

## 8. Việc còn nợ

1. **Sửa code để ba guard có thể land** (§6) — `internal/store`, `internal/bindings`,
   `cmd/anypick-release`. Sau đó commit guard cùng đợt.
2. **Commit `Makefile` + `AGENTS.md` + PR template.** Tôi *cố ý không commit*: 164 dòng
   thay đổi pending của bạn đan xen với sửa của tôi, commit sẽ sweep theo work của bạn
   (vi phạm §4). Gate `make paths` sẽ lên `main` cùng đợt đó.
3. **Tách worktree còn lại** (~100 file) thành các logical change.
4. `.github/CODEOWNERS` — §10 đã ghi: làm khi có người thứ hai. Hiện chưa ai để route.

## 9. Cách tự kiểm chứng

```bash
# Từ checkout sạch, không export PATH gì:
make review     # fmt-check + vet + test + gates
make lint       # staticcheck
```

Cả hai phải pass mà **không** cần chạm vào shell profile. Nếu không, gate đang unrunnable
và §1 áp dụng.

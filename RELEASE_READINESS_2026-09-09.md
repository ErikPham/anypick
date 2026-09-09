# Đánh giá sẵn sàng release — 2026-09-09

Mục tiêu: trả lời *"có thể cắt release từ trạng thái hiện tại không?"*
Nguồn: chạy gate thật, đo trên `HEAD` (`2347c00`) và trên working tree, không ước lượng.

> Tài liệu này là **báo cáo release**, không phải chuẩn. Chuẩn là `CODE_REVIEW.md`.
> Phân tích cơ chế thi hành review: `CODE_REVIEW_ENFORCEMENT_2026-09-09.md`.

---

## 0. Kết luận

# ❌ Chưa release được.

Ba blocker độc lập — ba cái đều phải xử lý xong mới có thể bấm `workflow_dispatch`.

| # | Blocker | Severity | Đo được |
| --- | --- | --- | --- |
| **B1** | `HEAD` **không compile được test** — package `internal/desktop` | 🔴 P0 | `make vet` đỏ |
| **B2** | `make fmt-check` đỏ — 1 file đã commit chưa gofmt | 🔴 P0 → ✅ đã fix, chờ commit | `make fmt-check` đỏ |
| **B3** | **79 commit chưa push** → CI chưa từng chạy trên code này | 🔴 P0 | `origin/main...main` = 0/79 |
| **B4** | Thiếu **signing/notarization + docs secrets**; release **fail theo thiết kế** (ADR 0020) | 🔴 P0 — chỉ Erik | `release.yml:289-305`, `389` |

Điều đáng nói nhất: **code bạn định release (HEAD) không build được, còn code bạn đang có
trong tay (working tree) thì pass 43/43 package, race sạch.** Hai sự thật đó cùng lúc
đúng, và chính khoảng cách giữa chúng là vấn đề.

**Sau khi tôi sửa hai lỗi cơ học** (`gofmt` 1 file, `S1030` 1 dòng), working tree còn:
`fmt-check` ✅ · `test` ✅ 43/43 · `test-race` ✅ 43/43 · `gates` ✅ · `lint` 🟡 1 finding
một dòng. **Thứ còn thiếu để release không phải là code — là một commit và một lần push.**

---

## 1. B1 (P0) — Tại `HEAD`, package test không compile được

```
$ cd /tmp/rel-head && git rev-parse --short HEAD
2347c00
$ make vet
vet: internal/desktop/hub_snapshot_test.go:219:6:
     srv.warmOpenCodeCatalogs undefined (type *Server has no field or method warmOpenCodeCatalogs)
```

Nói cho chính xác — hai mức khác nhau, đừng nhầm:

```
$ go build ./...      # rc=0  → BINARY VẪN BUILD ĐƯỢC
$ go vet   ./...      # rc=1  → PACKAGE TEST KHÔNG BUILD ĐƯỢC
```

Tức binary production không hỏng; thứ hỏng là **khả năng chứng minh nó đúng**:
`make test` tại `HEAD` báo FAIL cho `internal/desktop` (build error), và `make vet`
— gate bắt buộc trong `make review` — đỏ. Không có test nào chạy qua package đó.

Truy nguyên:

| File | Commit cuối | Ngày |
| --- | --- | --- |
| `internal/desktop/hub_snapshot_test.go` (nơi **gọi**) | `112ce14` | 2026-09-08 |
| `internal/desktop/hub_snapshot.go` (nơi **định nghĩa**) | `bb1ce7e` | 2026-09-07 |

`warmOpenCodeCatalogs` **chỉ tồn tại trong bản `hub_snapshot.go` chưa commit** (+174 dòng
trong working tree). Commit `112ce14` đã đưa phần test lên, còn phần cài ở lại working
tree. Đã **3 commit trôi qua** kể từ lúc đó (`e92e780`, `6aa0452`, `2347c00`) mà không ai
phát hiện — vì không có gate nào chạy ở local trước commit (xem `CODE_REVIEW_ENFORCEMENT
_2026-09-09.md` F4).

Hệ quả: tại `HEAD`, `make vet` đỏ **và `make test` không chạy được**. Mọi con số test
"xanh" gần đây đều đo trên working tree, tức đo trên thứ chưa tồn tại trên `main`.

---

## 2. B2 (P0) — `make fmt-check` đỏ

```
$ make fmt-check
internal/proxy/upstream_serve.go
```

```diff
-}// flattenedNativeUpstream flattens a native Anthropic target and its parent
+} // flattenedNativeUpstream flattens a native Anthropic target and its parent
```

File đến từ `e92e780` (2026-09-08, *"unify pool serving pipelines behind one egress core"*)
— đã commit, không phải WIP. Sửa mất 10 giây, nhưng **`main` đang đỏ ở gate đầu tiên của
`make ci`**, nên toàn bộ chuỗi `review + lint + test-race` chết từ bước một.

**✅ Đã sửa ở working tree** (chưa commit): `gofmt -w` đúng 1 file này — không dùng
`make fmt`, vì target đó format toàn bộ `src/go` và sẽ đè lên WIP của session khác.
`make fmt-check` hiện xanh. Cần một commit riêng cho file này.

---

## 3. B3 (P0) — 79 commit chưa push; CI chưa từng thấy code này

```
$ git rev-list --left-right --count origin/main...main
0	79          # behind=0, ahead=79
```

`release.yml` chỉ chạy trên `workflow_dispatch`, tức là chạy **trên GitHub**, từ
`origin/main`. Push 79 commit này lên trước, nếu không bản release sẽ được build từ code
cách đây 79 commit.

Kèm theo: vì chưa push, **GitHub Actions chưa bao giờ chạy trên trạng thái này**. Mọi
bằng chứng gate hiện tại là local. Với một dự án mà CI là lớp thi hành duy nhất, điều đó
nghĩa là 79 commit vừa qua **không có bằng chứng độc lập nào**.

---

## 4. Baseline đo được

| Gate | Tại `HEAD` (`2347c00`, tree sạch) | Working tree (có WIP) |
| --- | --- | --- |
| `make fmt-check` | 🔴 **đỏ** — `proxy/upstream_serve.go` | ✅ **xanh** (đã sửa, chưa commit) |
| `make vet` | 🔴 **đỏ** — `warmOpenCodeCatalogs` undefined | ✅ (WIP có method) |
| `make test` | ⛔ không chạy được | ✅ **43/43 package, 0 FAIL** |
| `make test-race` | ⛔ không chạy được | ✅ **43/43 package, 0 data race** |
| `make gates` (paths/protocol/release) | ✅ **xanh** — protocol 2, version 1.0.0 | — |
| `make lint` | 🔴 đỏ — 5 finding | ✅ **0 finding** (đã sửa `S1030` + `S1011`) |

### `make lint` tại HEAD — 5 finding

```
internal/desktop/hub_snapshot_test.go:255  warmOpenCodeCatalogs undefined            (compile)
internal/desktop/protocol.go:182           func decodeB64Body is unused              (U1000)
internal/desktop/snapshot.go:1079          func providerNameOf is unused             (U1000)
internal/desktop/snapshot.go:1110          func ternary is unused                    (U1000)
internal/proxy/pool_server_test.go:552     should use cw.body.String() instead of
                                           string(cw.body.Bytes())                  (S1030)
```

Ba `U1000` **đã biến mất khi đo trên working tree** — đúng như dự đoán: chúng được dùng ở
bản `hub_snapshot.go` chưa commit. `S1030` đã sửa tay.

Finding cuối cùng — `S1011` tại `internal/desktop/hub_snapshot.go:163` — **đã sửa tay**
(gộp vòng lặp thành một `append(...)`). `make lint` hiện **0 finding** trên working tree.
Diff đã áp, để đó phòng khi cần khôi phục:

```diff
-			for _, row := range s.hubCatalogRows(ctx, names, pinned, providerID, name, acc.SnapshotDir) {
-				snap.HubCatalogs = append(snap.HubCatalogs, row)
-			}
+			snap.HubCatalogs = append(snap.HubCatalogs, s.hubCatalogRows(ctx, names, pinned, providerID, name, acc.SnapshotDir)...)
```

Một dòng. Nằm chung commit (1) là xong.

---

## 5. Phiên bản sẽ cắt

```
$ make release-plan
mode=release   bump=minor   version=1.1.0   tag=v1.1.0

$ cat VERSION
1.0.0
$ git tag
v1.0.0
```

Conventional Commits trong 79 commit chưa push suy ra **minor → v1.1.0**. Đồng bộ version
hiện tại: `make release-check` xanh (4 nguồn cùng 1.0.0).

Lưu ý: 6 subject không parse được (`F2: …`, `engine: …` — xem
`CODE_REVIEW_ENFORCEMENT_2026-09-09.md` F3) **đã bị rơi khỏi release note**. Changelog của
v1.1.0 sẽ thiếu phần việc đó. Không sửa được bằng cách rewrite lịch sử (P0), chỉ ghi nhận.

---

## 6. Đường đi đề xuất — làm đúng thứ tự này

**0. Trước tiên: kiểm tra B4 (§7).** Nếu 6 secret Apple / repo chưa public chưa sẵn
sàng, dừng lại — mọi bước bên dưới vô nghĩa vì `build-macos` sẽ fail có chủ ý.

**1. Commit phần Go WIP thành một change riêng (7 file, dùng `git add` từng file)**

Đây là bước bắt buộc để `HEAD` compile được. Đúng 7 file:

```
src/go/internal/desktop/hub_snapshot.go          (M)
src/go/internal/catalog/models.go                (M)
src/go/internal/provider/opencode_credential.go  (M)
src/go/internal/catalog/account.go               (??)
src/go/internal/catalog/account_test.go          (??)
src/go/internal/provider/opencode_catalog.go     (??)
src/go/internal/provider/opencode_catalog_test.go(??)
```

🚫 **Không `git add -A`.** Working tree đang có ~20 file `src/macos` (cutover của session
khác) + `Makefile` + `CODE_REVIEW.md` + `SECURITY.md`. Gộp vào là vừa vi phạm
§4 "one logical change", vừa cướp work của session khác.

**2. Commit riêng 1 file format:** `src/go/internal/proxy/upstream_serve.go` (đã sửa).
Đừng chạy `make fmt` — nó format toàn bộ `src/go` và sẽ đè WIP của session khác.

**3. Sửa lint thật:** ✅ `S1030` đã sửa (`pool_server_test.go:552` → `cw.body.String()`,
cùng commit với (2) hoặc riêng). `S1011` ở `hub_snapshot.go:163` nằm trong commit (1);
ba `U1000` đo lại sau (1) — đừng đoán là chúng biến mất.

**4. Chạy lại `make ci` từ worktree sạch tại HEAD mới** — không phải working tree:

```bash
git worktree add --detach /tmp/rel-verify HEAD
cd /tmp/rel-verify && make ci
git worktree remove --force /tmp/rel-verify
```

**5. Push 79 commit**, chờ CI xanh. Đây là bằng chứng duy nhất có giá trị để release.

**6. Cắt version** — `make release-bump PART=minor` (hoặc `release-set VERSION=1.1.0`),
commit, tag, rồi `workflow_dispatch` `release.yml` với scope `macos-only`.

**7. Build app từ worktree tại commit đã xanh** — đừng build từ working tree (quy tắc đã
chốt sáng nay: app build từ WIP gãy vì protocol lệch).

---

## 7. B4 (P0) — Những thứ release cần mà **không gate nào ở local kiểm tra**

Đây là blocker lớn nhất, và không phải lỗi code. `release.yml` fail theo thiết kế:

**a. 6 secret signing/notarization bắt buộc** (`release.yml:289-305`)

```
APPLE_DEVELOPER_ID_APPLICATION   APPLE_CERT_P12_BASE64   APPLE_CERT_PASSWORD
APPLE_API_KEY_P8                 APPLE_API_KEY_ID        APPLE_API_ISSUER_ID
```

```yaml
- name: Require macOS signing material
  run: |
    ...
    echo "::error::macOS signing/notarization secrets are required for a public
          release (ADR 0020). Configure them, or narrow the release scope —
          do not ship unsigned."
    exit 1
```

Chưa cấu hình ⇒ job `build-macos` đỏ. **Không có đường tắt**: ADR 0020 cấm ship bản
unsigned, và chính workflow từ chối `CODE_SIGNING_ALLOWED=NO`.

**b. Repo phải public** (`release.yml:389`) — `test "$REPOSITORY_VISIBILITY" = public ||
exit 1`.

**c. Workflow phải chạy trên `refs/heads/main`** (`release.yml:34`) — điều này biến B3
thành điều kiện **cứng**, không phải "nên làm": chưa push 79 commit thì không có gì để
release.

**d. 2 secret Cloudflare cho `Deploy the docs`** (`CLOUDFLARE_ACCOUNT_ID`,
`CLOUDFLARE_API_TOKEN`, `release.yml:509`) — step này dường như **không có `if:` guard**,
nên thiếu token thì job `publish` fail *sau khi* artifact đã build xong.

👉 Bốn mục này không ai sửa thay Erik được — chúng nằm trong Settings và tài khoản
Apple. **Kiểm tra trước khi bấm dispatch**, vì mỗi lần fail tốn cả chuỗi build.

---

## 8. Hai việc nên làm ngay sau release

- **Cài `pre-push`** (`.githooks/install.sh`). B1 và B2 đều là lỗi mà `make review` bắt
  được trong 10 phút — nhưng không ai chạy, vì không có gì tự động. Đây không phải lý
  thuyết: đây là bằng chứng.
- **Đừng để `main` đỏ quá một commit.** Hiện tại nó đỏ 3 commit mà không ai biết, vì
  không có gate nào chạy trước khi commit và không push tức là không có CI.

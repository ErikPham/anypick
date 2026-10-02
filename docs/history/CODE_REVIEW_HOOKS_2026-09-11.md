# Review lớp thi hành — `.githooks/` — 2026-09-11

Ngày: 2026-09-11 · Phạm vi: `.githooks/` (4 file), `Makefile` target `paths`,
`CODE_REVIEW.md` §1/§7 · Người lập: code review
Nguồn: **đo trực tiếp**, mọi kết luận dưới đây đều có lệnh tái lập

> **Tài liệu này không phải chuẩn thứ hai.** Chuẩn là `CODE_REVIEW.md`. Đây là
> bản review lớp thi hành — thứ `CODE_REVIEW_ENFORCEMENT_2026-09-09.md` đã thiết
> kế và `9c6ef40` đã commit.

---

## 0. Kết luận

Hai câu, và câu thứ nhất quan trọng hơn:

1. **Cơ chế chưa bao giờ chạy.** `.githooks/` đã được commit đủ 4 file, đã
   `chmod +x`, mutation suite 17/17 xanh — nhưng `core.hooksPath` **chưa được
   set**:
   ```
   $ git config --get core.hooksPath
   (exit 1 -- không có giá trị)
   ```
   Nghĩa là gate đã sẵn sàng và chưa từng bắt được gì. Đây đúng là căn bệnh mà
   `CODE_REVIEW_ENFORCEMENT_2026-09-09.md` §4.A đã gọi tên: *"Chưa chạy thì toàn
   bộ phần trên là tài liệu."*

2. **Và khi đọc kỹ code thì gate có 3 lỗi thật**, trong đó 1 lỗi P1 làm hỏng
   đúng thứ nó được viết ra để bảo vệ. Bốn file hook **chưa từng chạy trên một
   commit thật** nào — nên không lỗi nào trong số này từng lộ ra.

Đã sửa cả 6. Trong lúc viết bản sửa tôi **hạ F2 từ P2 xuống P3** — exit code của
nó vốn đã đúng, cái sai chỉ là thông báo. Lý do đầy đủ ở §1.F2, và nó là một
đính chính so với khuyến nghị ban đầu của chính tôi.

**Và bản review này tự nó đã được review độc lập — rồi bị bắt một lỗi P1 (§1.F7).**
Sửa một gate là `Risk: High` theo §7, nên nó cần một context khác đọc diff. Bản
độc lập tìm ra: **khoá trailer phân biệt hoa thường** ⇒ `RISK: High` qua được gate
mà **không cần `Reviewed-By`**. Tôi tự kiểm lại, xác nhận, rồi sửa. Đây là bằng
chứng đắt nhất cho luận điểm của chuẩn: **self-review không phải review độc lập** —
tôi viết F1 để bịt đúng lớp lỗi này và vẫn để lọt nó ở dạng chữ hoa.

---

## 1. Finding

### 🔴 F1 · P1 · Gate chấp nhận `Risk:` mà `git` không đọc được

`commit-msg` đọc trailer bằng cách **grep thẳng vào nội dung message**:

```sh
risk=$(grep -iE '^Risk:[[:space:]]*(Low|Normal|High|Architectural)[[:space:]]*$' "$msg_file" || true)
```

Nhưng một dòng chỉ là *trailer* khi nó nằm ở **đoạn cuối** của message. Đặt một
đoạn văn xuống dưới là hai dòng đó trở thành văn xuôi — mà grep vẫn thấy.

**Đo được** (message có `Risk: High` + `Reviewed-By:` rồi một đoạn kết):

| Kiểm tra | Kết quả |
| --- | --- |
| `commit-msg` | **exit 0** — cho qua |
| `git interpret-trailers --parse` | **rỗng** — không có trailer nào |
| `git log --format='%(trailers:key=Risk)'` | **rỗng** |

**Vì sao đây là P1, không phải P2.** `CODE_REVIEW_ENFORCEMENT_2026-09-09.md` §2
ghi rõ cách đọc phân loại rủi ro của repo là
`git log --format='%h %s%n%(trailers:key=Risk)'`. Gate nói "đã phân loại, đã
được review độc lập"; bản ghi lâu dài nói **không có gì**. Tệ hơn: `Risk: High`
đòi `Reviewed-By`, và ở đây `Reviewed-By` cũng không đọc được — tức yêu cầu
review độc lập (§1 của chuẩn, *"the single most important rule in this
document"*) thoả mãn được bằng một dòng mà **sau này không ai truy xuất được**.
Gate đang bảo vệ một bản ghi không tồn tại.

**Đã sửa.** Đọc trailer qua chính parser của git:
`trailers=$(git interpret-trailers --parse < "$msg_file")`, rồi grep trên
`$trailers` cho cả `Risk:`, `Reviewed-By:` và `ADR:`.

Đã kiểm `git interpret-trailers` là plumbing trên stdin, **không cần repo**
(chạy ngoài repo: exit 0, output đúng) — nên suite mutation vẫn chạy được từ
bất kỳ thư mục nào.

**Mutation-check bản sửa** — đây là phần chứng minh, không phải tuyên bố:

```
hook đã sửa         20 passed, 0 failed
hook trước khi sửa  18 passed, 2 failed
                    FAIL Risk demoted by a trailing paragraph -- exit 0, wanted 1
                    FAIL Reviewed-By demoted by a trailing paragraph -- exit 0, wanted 1
```

Hai ca fail đúng là hai ca mới thêm cho lỗi này. Bản sửa không hồi quy 17 ca cũ.

### 💭 F2 · P3 (hạ từ P2) · `git revert` / `--fixup` bị chặn, và thông báo chỉ sai đường

**Đo được:**
```
$ git revert --no-edit HEAD
subject: Revert "feat(cli): add models"
commit-msg: subject is not a Conventional Commit (CODE_REVIEW.md §4).
exit=1
```

`git revert` sinh subject `Revert "..."`, không khớp regex. `git commit --fixup`
(`fixup! feat(...)`) và `--squash` cũng bị chặn y như vậy.

**Tôi đã hạ mức finding này khi viết bản sửa, và phải nói rõ vì sao.** Ban đầu
tôi xếp P2 với lập luận "gate chặn một thao tác mà §7 khuyến nghị". Kiểm lại thì
lập luận đó **sai**: exit code vốn đã đúng. Một revert *có* land và *có* vào
changelog, nên subject của nó buộc phải parse được — và `revert:` **đã** là một
type hợp lệ, tức đường đúng đã tồn tại sẵn. Thứ duy nhất sai là **thông báo**: nó
liệt kê `revert` như một type hợp lệ trong khi từ chối đúng dạng mà `git revert`
sinh ra. Gate chặn đúng, nhưng chỉ sai đường. Đó là P3, không phải P2.

Hệ quả: tôi **không** miễn trừ revert — ngược với khuyến nghị ban đầu của chính
tôi ở bản trước. Miễn trừ là nới gate, và §5 cấm. Thay vào đó:

- `Revert "..."` → **vẫn từ chối**, kèm thông báo nói thẳng cách sửa
  (`git revert --edit <sha>`, rồi viết `revert: <subject gốc>`).
- `fixup!` / `squash!` → **miễn trừ như `Merge `**. Chúng là commit tạm, bị
  `git rebase --autosquash` viết lại trước khi nhánh được chia sẻ; chặn chúng là
  chặn một workflow bình thường mà không có escape hatch — đúng kiểu *"một quy tắc
  không thể tuân theo sẽ thối rữa"* mà §1 cảnh báo.

**Ghi chú trung thực về test:** ca `git's generated revert subject` chỉ khẳng
định exit code (1) — mà exit code đó **vốn đã là 1 trước khi sửa**. Nên ca này
không phân biệt được thông báo mới với thông báo cũ: nó khoá *hành vi*, không
khoá *chất lượng thông báo*. Muốn khoá phần thông báo thì phải mở rộng harness
để so khớp nội dung; tôi để lại như một việc nhỏ thay vì phình harness.

### 🟡 F3 · P2 · `Reviewed-By:` trùng tên tác giả vẫn qua

**Đo được:** với `git config user.name = Erik`, message
`Risk: High` + `Reviewed-By: Erik` → **exit 0**.

Hook chỉ kiểm tra "có một chuỗi không rỗng sau `Reviewed-By:`". Nó không kiểm
tra — và không thể kiểm tra hoàn hảo — rằng người ký *khác* người viết. Nhưng
ca **trivial nhất** thì đo được: `Reviewed-By` trùng `git var GIT_AUTHOR_IDENT`
là self-review, đúng thứ §1 cấm. Đây là gap đã biết (comment trong hook tự nhận
"a name is required"), nhưng nó đóng được thêm một nấc.

**Đã sửa.** Trong nhánh `high|architectural`, so `Reviewed-By` với **cả tên và
địa chỉ** của `git var GIT_AUTHOR_IDENT`, không phân biệt hoa thường. Identity
rỗng không khớp được với reviewer không rỗng, nên một máy chưa cấu hình git
không bị gate này chặn oan.

**Mutation-check:** vô hiệu hoá nhánh `case` ⇒
`FAIL Reviewed-By names the author -- exit 0, wanted 1` (24 passed, 1 failed).
Khôi phục ⇒ 25 passed, 0 failed.

Giới hạn còn lại, nói thẳng: gate chỉ bắt được ca *trùng định danh*. Nó không
bắt được một cái tên bịa. Đó là giới hạn của máy, không phải thiếu sót của gate.

### 🟡 F4 · P2 · `pre-push` chạy `make review` — 10 phút mỗi lần push

`make review` đo được **10m02s** (`TEAM_QUALITY_BASELINE_2026-09-08.md`), và
`pre-push` chạy đúng nó. Phần đắt nhất là test suite — thứ CI **đã chạy trên
cùng lần push đó**.

Đây là P2 theo đúng lập luận của chuẩn: một gate đắt hơn giá trị của nó là gate
người ta học cách bỏ qua, và `git push --no-verify` là **P0 lên người push**
(§5). Friction ở đây trực tiếp sản xuất ra P0.

**Đã sửa — Erik chốt 2026-09-11:** `pre-push` còn `make fmt-check vet gates`.
**Không xoá bước nào**: `make ci`, `make review` và CI giữ nguyên; chỉ bỏ việc
*nhân bản tại máy* một gate mà CI chạy vài phút sau.

### 🟡 F5 · P2 · Không có gì bảo vệ chính các hook

Repo này đã trả giá một lần cho đúng lớp lỗi này: `refs/` trong `.gitignore`
nuốt `internal/refs`, 45 file đã commit, `git status` im lặng. `make paths` từ
đó kiểm 4 tài liệu authority có được track không — nhưng **không kiểm
`.githooks/`**. Một hook bị ignore hoặc không được commit là một gate chỉ máy
này chạy, và không có gì báo.

`install.sh` chỉ *cảnh báo*, chỉ lúc cài, và chỉ với **3/4 file** — bỏ qua
`test.sh`, tức đúng file người ta sửa nhiều nhất khi đang đổi gate.

**Đã sửa:**
- `make paths`: thêm check "local review gates are tracked" — `ls .githooks/*`
  rồi `git ls-files --error-unmatch` từng file; danh sách rỗng ⇒ FAIL (kiểm tra
  trên 0 mục không phải là pass).
- `install.sh`: `chmod +x .githooks/*` và vòng `check-ignore` dùng **glob** thay
  vì danh sách 3 tên — hook thêm sau tự động được phủ.
- `Makefile`: thêm target `hooks`.

**Mutation-check:** `git rm --cached .githooks/test.sh` ⇒
`FAIL .githooks/test.sh is not tracked` (3 file kia vẫn `OK`); `git reset` ⇒
xanh lại.

### 💭 F6 · P3 · Suite mutation thiếu ca *vị trí* trailer

Suite có ca "Risk inside a comment does not count" nhưng **không có ca nào cho
vị trí trailer** — nên F1 sống sót qua 17 ca. Đã thêm 3 ca: hai ca âm (F1) và
một ca dương (`body prose then a real trailer block`) để bản sửa không bị chặt
quá tay.

**Đây là bài học đáng giữ:** suite test *giá trị* của trailer mà không test
*chỗ đứng* của nó. Gate và suite cùng nhìn vào một chỗ thì cùng mù một chỗ.

### 🔴 F7 · P1 · Khoá trailer phân biệt hoa thường — **do review độc lập tìm ra**

Không phải tôi tìm ra. Context review độc lập (§5) báo; tôi tự kiểm lại rồi xác
nhận trước khi tin.

Trailer của git **không phân biệt hoa thường ở khoá**: `RISK: High` là một
trailer thật và `%(trailers:key=Risk)` đọc được nó. Nhưng gate cũ kiểm *sự tồn
tại* bằng `grep -i` (nhận), rồi *trích giá trị* bằng `[Rr]isk:` (không nhận).

| Message | Trước | Sau |
| --- | --- | --- |
| `RISK: High`, không có `Reviewed-By` | **exit 0** | exit 1 |
| `Risk: High` + `REVIEWED-BY: <chính tác giả>` | **exit 0** | exit 1 |

Dòng đầu: nhánh `high` không bao giờ chạy ⇒ **yêu cầu review độc lập bị bỏ hoàn
toàn**. Dòng hai: `Reviewed-By` có tồn tại nên yêu cầu "phải có reviewer" thoả,
còn self-check bị bỏ qua vì `sed` không khớp.

Đây **đúng lớp lỗi mà F1 vừa bịt, chỉ khác một tầng chữ** — và nó có trước thay
đổi này. Tôi đã sửa mấy dòng đó nên phải sửa nó.

**Đã sửa:** chuẩn hoá hoa/thường **một lần** (`trailers_lc`), rồi khớp **một
lần** bằng pattern chữ thường. Không còn `-i` nào trên các grep trailer — chính
sự bất đối xứng giữa "kiểm tồn tại" và "trích giá trị" sinh ra lỗi này.

**Mutation-check mạnh nhất trong cả bản này:** chạy **suite mới** với **hook gốc
lấy từ `git show HEAD:.githooks/commit-msg`** ⇒ **7 ca đỏ, đúng 7 ca mới**:

```
FAIL Risk demoted by a trailing paragraph      (F1)
FAIL Reviewed-By demoted by a trailing paragraph (F1)
FAIL uppercase RISK routes to High             (F7)
FAIL uppercase REVIEWED-BY naming the author   (F7)
FAIL Reviewed-By names the author              (F3)
FAIL fixup! commit                             (F2)
FAIL squash! commit                            (F2)
21 passed, 7 failed
```

Không ca cũ nào đỏ ⇒ mọi ca mới đều có tải trọng, và bản sửa không hồi quy gì.
(Một ca mới — `git's generated revert subject` — **không** đỏ, đúng như đã ghi ở
F2: nó khoá hành vi vốn đã đúng.)

---

## 2. Hai nghi ngờ đã kiểm và **KHÔNG** phải bug

Ghi lại để lần sau không ai đi lại:

- **`git rev-parse --git-path hooks` có phản ánh `core.hooksPath` không?**
  Có. Sau `git config core.hooksPath .githooks`, lệnh trả `.githooks`. Dòng
  `install: core.hooksPath -> …` là **đúng**, không phải bug.
- **`risk: low` viết thường có lọt và có đọc được không?**
  Có lọt (hook dùng `-i`, cố ý) và `%(trailers:key=Risk)` **đọc được** nó. Hai
  bên nhất quán. Không phải bug.

---

## 3. Hai quyết định chính sách — đã xử lý

| # | Quyết định | Chọn | Vì sao |
| --- | --- | --- | --- |
| F2 | `git revert` / `--fixup` | **Chặn** revert, kèm thông báo dạy cách sửa; **miễn trừ** `fixup!`/`squash!` | Miễn trừ revert là nới gate (§5 cấm) trong khi đường đúng đã có sẵn; chặn `fixup!` là chặn một workflow bình thường mà không có escape hatch |
| F3 | `Reviewed-By` trùng tác giả | **Từ chối** khi trùng định danh tác giả | §1 gọi review độc lập là quy tắc quan trọng nhất; ca trivial này máy quyết được |

Cả hai đều có test + mutation trong `.githooks/test.sh`. Muốn đảo ngược thì mỗi
cái là một chỗ độc lập trong `commit-msg` và một ca test tương ứng.

---

## 4. Tự kiểm chứng

```bash
sh .githooks/test.sh                       # 28 passed, 0 failed
make paths                                 # có dòng "checking the local review gates are tracked"
git config --get core.hooksPath             # .githooks  (sau khi chạy .githooks/install.sh)
```

Và tái lập F1 trên bản **chưa** sửa — đây là cách chắc nhất để tin finding này:

```bash
printf 'fix(a): x\n\nRisk: High\nReviewed-By: n\n\nmot doan ket.\n' > /tmp/m
.githooks/commit-msg /tmp/m; echo "exit=$?"     # 1 (đã sửa) -- trước khi sửa là 0
git interpret-trailers --parse < /tmp/m          # rỗng
```

---

## 5. Biên bản review độc lập

§7 của chuẩn đòi một context độc lập cho thay đổi vào gate. Thay đổi này là
`Risk: High` (nó sửa một gate), nên nó đã được review độc lập: một agent context
riêng, không tham gia viết diff, được giao diff + các tài liệu authority + danh
sách câu hỏi đáng hỏi.

**Kết luận của bản độc lập:** bản sửa trailer **đúng**, và cả hai tuyên bố của
tác giả đều được nó kiểm lại độc lập (`25 passed, 0 failed`; `git
interpret-trailers` chạy được ngoài repository). Nó tìm ra **một P1** (F7) và
một số điểm nhỏ hơn:

| Mức | Finding | Xử lý |
| --- | --- | --- |
| **P1** | Khoá trailer phân biệt hoa thường ⇒ bỏ qua yêu cầu review độc lập | **Đã sửa** (F7) |
| P2 | `pre-push` không còn chạy test, mà ruleset bắt buộc CI vẫn là mục mở (§10) ⇒ test đỏ có thể lên `main` qua một push thường | **Giữ nguyên, đã báo Erik** — hệ quả của quyết định tách gate, không phải lỗi |
| P2 | §11 viết "PR template *is* the mechanism" trong khi §12 nói repo không có PR | **Đã sửa** §11 |
| P3 | `Reviewed-By: <tác giả> (2nd session)` lọt (so khớp chính xác, không phải tiền tố) | Chấp nhận — giới hạn của máy |
| P3 | `fix: x` rồi ngay `Risk: Low` không có dòng trống bị từ chối | Chấp nhận — git cũng cần dòng trống phân cách trailer |

Nó cũng xác nhận **không** có lỗi ở hai chỗ tôi nghi ngờ: `case "$author_lc"`
được quote nên không dính bẫy glob metacharacter trong pattern; và khối mới của
`make paths` không dính bẫy `#`-trong-recipe, vẫn giữ guard "0 mục ⇒ FAIL".

**Câu hỏi nó trả lời mà tôi không tự trả lời được:** *thay đổi này có cần một
invariant test mới thay vì chỉ một bản sửa không?* Trả lời: ca *vị trí trailer*
đã đúng nấc (§5 rung 3 + rung 4), nhưng **lỗ hổng chữ hoa cần ca mutation mới
trên gate đang có** — và đó là 3 ca đã thêm.

# Audit UI/UX macOS và kế hoạch chỉnh sửa

**Ngày audit:** 2026-09-24  
**Phạm vi:** cửa sổ chính, không gồm menu bar Quick Panel / tray  
**Trạng thái:** đã triển khai các sửa đổi source và build macOS thành công; cần đối chiếu fixture để xác nhận bằng mắt cảm giác giật/nháy đã hết.

Các phần **Bằng chứng / Ảnh hưởng / Hướng sửa** bên dưới ghi lại trạng thái trước khi triển khai để giữ cơ sở của audit. Trạng thái hiện tại và phần cần xác minh nằm ở cuối tài liệu.

## Kết luận thiết kế

Khung điều hướng và các pane đã đi theo cấu trúc System Settings: sidebar, drill-in, grouped Form và control hệ thống. Audit ban đầu tìm thấy ba vấn đề có thể làm thao tác trông giật/nháy: một snapshot phát nhiều lượt đồng bộ, account hiện hành làm thay đổi thứ tự hàng, và toast thành công phủ lên nội dung. Các sửa đổi source bên dưới đã xử lý cả ba; cần kiểm tra fixture để đánh giá cảm giác thực tế và layout khi resize/cuộn.

Giữ đúng ý đồ interaction của Accounts & APIs: rê lên hàng thì hiện Switch/Connect; bấm phần hàng thì vào chi tiết account. Đây là pattern người dùng đã chủ đích lấy từ Wi‑Fi trong System Settings. Không thay bằng nút Switch luôn hiện hoặc biến toàn bộ hàng thành một nút điều khiển.

## Bằng chứng và độ chắc chắn

| Nhận định | Trạng thái | Cơ sở |
| --- | --- | --- |
| Snapshot nền gây nhiều lượt đồng bộ và invalidation dư | Có căn cứ mạnh trong code; cần đo trên fixture để gắn với từng cú giật | Store phát nhiều @Published khi áp một snapshot; ProductionModel lắng nghe từng lần và đồng bộ lại các collection rộng. |
| Switch làm account hiện hành đổi vị trí | Đã sửa source | Danh sách giờ giữ thứ tự provider/tên; Switch chỉ đổi trạng thái hàng tại chỗ. |
| Start gửi thao tác đồng bộ chặn main thread | Không thấy dấu hiệu trong code | AppDelegate đưa lệnh sang GoServiceProcess bằng Task; đường này bất đồng bộ. |
| Dòng proxy có đổi kích thước khi Start | Không thấy trong cấu trúc hàng | Cột action có chiều rộng tối thiểu; subtitle một dòng; trạng thái dùng spinner tại chỗ. |
| Started… là modal dialog | Đã sửa source | Đây là toast capsule, không phải modal; toast thành công đã bỏ, còn lỗi dùng `.alert` native. |
| Form có thể giật khi nội dung đổi hoặc cuộn | Cần xác minh | Pane thường bọc grouped Form trong ScrollView ngoài và cố định chiều cao Form theo nội dung. |

Audit ban đầu dựa trên code có thay đổi chưa commit; các sửa đổi triển khai được áp trực tiếp trên worktree mà không hoàn tác phần khác. Build kiểm tra biên dịch đã chạy sau sửa và thành công. Chưa mở fixture, chạy test hoặc đo Instruments; khi xác minh trực quan, mở fixture mới theo src/macos/README.md, không dùng screenshot cũ.

## Đánh giá theo pane

| Pane | Đánh giá từ source | Cần xác minh trên fixture |
| --- | --- | --- |
| Overview | Bố cục có thứ bậc rõ: Needs Attention, checklist khi cần, Apps, Services và Activity; dùng section thay card. | Lượng nội dung trên màn đầu, trạng thái first-run và cửa sổ thấp. |
| Apps | Danh sách có search, trạng thái setup/route và drill-in thật bằng NavigationLink; cấu trúc đúng kiểu Settings. | Subtitle account/model dài, nhãn setup, danh sách dài và luồng đổi connection → review → apply. |
| Accounts & APIs | Pattern hover Switch + click row vào detail được giữ; thứ tự hàng ổn định. | Giữ pointer, focus và scroll khi Switch trên fixture. |
| Proxy Hub | Trạng thái Running/Stopped lấy từ snapshot; operation điều khiển nhãn Starting/Stopping. | Start/Stop/Restart, catalog rỗng/đầy, label accessibility và snapshot đến chậm. |
| Proxies | Nút Start/Stop tại hàng giữ hình dạng trong lúc chờ; Start All chạy theo queue tuần tự. | Nhiều hàng, action row, lỗi giữa batch và cuộn khi trạng thái đổi. |
| Activity | Có Events/Live Logs, lọc trên toolbar và vùng log cuộn riêng; nền/viền tự vẽ đã bỏ. | Scroll và vùng cao tối thiểu trên cửa sổ nhỏ. |
| Account Backups | Cảnh báo credential và luồng review/import đặt đúng ngữ cảnh; Existing không còn badge capsule. | Độ rõ của bước chọn, review, restore và lỗi backup. |
| Settings | Scene riêng, sidebar native và các nhóm General/Usage/Proxies/Security/Diagnostics. | So với app Settings khi resize, đặc biệt pane Diagnostics và focus sau khi đổi nhóm. |

## Các hành vi cần giữ

- Hàng account mở trang chi tiết khi bấm vào phần nội dung.
- Switch/Connect xuất hiện trong cột trailing khi rê chuột. Cột đó giữ nguyên chỗ khi action ẩn; trạng thái current và disclosure cũng có cột riêng.
- Context menu và accessibility action vẫn cung cấp lệnh cho bàn phím và VoiceOver khi hover không có.
- Proxy Start/Stop giữ nút tại chỗ và thay nội dung bằng spinner khi thao tác đang chạy.
- Main window tiếp tục dùng System Settings chrome. Tray/Quick Panel nằm ngoài phạm vi và giữ contract riêng.

## Findings ưu tiên

### P1 — Một snapshot đang gây nhiều lượt cập nhật cho cùng một pane (đã sửa source)

**Bằng chứng:** TrayStore.apply trong [Store.swift](Store.swift#L594) gán hàng loạt thuộc tính @Published kể cả khi giá trị không đổi. ProductionDesignModel đăng ký store.objectWillChange tại [ProductionModel.swift](ProductionModel.swift#L171), rồi mỗi thông báo xếp một lần synchronize() lên MainActor. Trong synchronize, các collection allSupportedApps, apps, accounts, proxies, hubSources và nhiều state khác được gán lại không qua so sánh, ví dụ [ProductionModel.swift](ProductionModel.swift#L1439) và [ProductionModel.swift](ProductionModel.swift#L1482). Store có chú thích rằng snapshot luôn gửi lại đầy đủ lists; chú thích 1,75 giây ở Store chỉ nói về poll của logs, không xác định chu kỳ snapshot. Store đã thêm so sánh cho logs và activity vì publish giá trị giống nhau làm pane nhấp nháy/đặt lại cuộn.

**Ảnh hưởng:** Một thay đổi nhỏ như Start hoặc Switch đi qua snapshot đầy đủ, rồi có thể khiến model phát lại những danh sách không đổi. SwiftUI phải tính lại nhiều vùng cùng lúc. Đây là nguyên nhân chung có căn cứ mạnh nhất để giải thích cảm giác cả khối UI giật/nháy; cần Instruments hoặc fixture để xác nhận đúng nhịp render thấy được.

**Hướng sửa:** Áp snapshot thành một trạng thái nhất quán, chỉ publish trường thực sự đổi. Coalesce observer để một snapshot được xử lý một lần, không một lần cho mỗi thuộc tính của snapshot. Trong ProductionModel, so sánh giá trị dẫn xuất trước khi gán các collection và state quan sát bởi pane. Giữ ID của mỗi row ổn định.

**Tiêu chí đạt:** Khi snapshot nền lặp lại nhưng dữ liệu nghiệp vụ không đổi, không có lượt cập nhật UI nhìn thấy được, không đổi vị trí cuộn và không dựng lại danh sách. Một thay đổi proxy chỉ cập nhật hàng proxy đó cùng summary nào thực sự phụ thuộc vào nó.

### P1 — Switch làm hàng account nhảy vị trí (đã sửa source)

**Bằng chứng:** savedAccounts sort bằng nativeAccountOrder trong [AccountsView.swift](AccountsView.swift#L33); comparator ưu tiên account hiện hành trước trong nhóm provider tại [AccountsView.swift](AccountsView.swift#L560). Khi Switch thành công, trạng thái current đổi, nên vị trí hàng được tính lại. Cột Switch, current marker và disclosure đã được giữ kích thước cố định tại [AccountsView.swift](AccountsView.swift#L379), vì vậy hover không làm hàng co giãn; phần đổi chỗ mới là chuyển động đáng chú ý.

**Ảnh hưởng:** Người dùng bấm Switch ở một hàng nhưng sau khi hoàn tất hàng đó có thể bị chuyển lên đầu nhóm. Mắt và con trỏ mất vị trí neo. Điều này có thể trông như khối account giật, dù mẫu hover + click để mở chi tiết vẫn đúng với ý đồ Wi‑Fi của bạn.

**Hướng sửa:** Giữ mẫu interaction hover và điều hướng hiện tại. Ưu tiên thứ tự hàng ổn định theo provider/tên; chuyển current marker sang account mới ngay tại chỗ. Nếu thứ tự current-first là yêu cầu sản phẩm, phải xác minh chuyển động có chủ đích và không bị phát nhiều lần do snapshot; không để SwiftUI tự tạo nhiều lần remove/insert.

**Tiêu chí đạt:** Switch đổi trạng thái của hai hàng liên quan, không làm các hàng khác đổi vị trí; focus, hover và điểm cuộn vẫn ổn định. Dùng đúng thao tác Switch và click-to-detail như hiện nay.

### P1 — Toast Started…/Switched… trùng trạng thái tại hàng và trông như UI web (đã sửa source)

**Bằng chứng:** MainWindow.statusFeedbackToast vẽ toast capsule/material phủ dưới cửa sổ tại [MainWindow.swift](MainWindow.swift#L220). showFeedback đồng thời gọi VoiceOver announcement và lưu feedback tại [MacOSDesignModel+Support.swift](MacOSDesignModel+Support.swift#L19). Kết quả service success gọi feedback trước khi snapshot xác nhận trạng thái; spinner hàng proxy vẫn chờ snapshot trong confirmServiceOperations tại [ProductionModel.swift](ProductionModel.swift#L1844). README quy định main window dùng alert cho lỗi, thành công im lặng kèm announcement; không có toast stack.

**Ảnh hưởng:** Started… vừa lặp với trạng thái Running/đang chạy ở hàng, vừa có thể xuất hiện trước khi UI xác nhận proxy thực sự Running. Switched… cũng lặp với current marker của account. Toast là overlay nên tự nó không đổi layout; nó vẫn thêm một lớp nổi kiểu web thay vì phản hồi inline của System Settings.

**Hướng sửa:** Bỏ toast thành công/thông tin khỏi cửa sổ chính cho các lệnh lifecycle thường ngày. Hàng là phản hồi chính: nút chuyển sang spinner trong lúc chờ, rồi cùng hàng hiện trạng thái đã xác nhận. Chỉ announcement sau khi snapshot xác nhận thành công. Giữ lỗi ở native .alert; không phát success trước xác nhận.

**Tiêu chí đạt:** Start thành công không mở dialog, banner hay toast; không có Started trước khi trạng thái đích xuất hiện. VoiceOver nhận đúng một announcement sau xác nhận; lỗi vẫn có nội dung và hành động đóng phù hợp.

### P1 — Proxy Hub đổi sang Stop ngay khi người dùng bấm Start (đã sửa source)

**Bằng chứng:** setProxyHubEnabled gán proxyHubEnabled theo giá trị đích trước khi gọi setProxyEnabled trong [ProductionModel.swift](ProductionModel.swift#L1077). ProxyHubView chọn nhánh Start hay Stop từ proxyHubEnabled, nhưng nhãn accessibility chọn Starting/Stopping từ cùng biến busy tại [ProxyHubView.swift](ProxyHubView.swift#L236). Sau một cú Start, state tạm thời vì vậy có thể vẽ nhánh Stop kèm nhãn “Stopping Proxy Hub”; hubStatus cũng xem giá trị optimistic đó là Running trước khi snapshot xác nhận.

**Ảnh hưởng:** Hành động và trạng thái tức thời mâu thuẫn; nút có thể đổi nhãn/role đúng lúc user đang chờ, góp phần tạo cảm giác nháy ở khối Hub.

**Hướng sửa:** Giữ trạng thái snapshot làm nguồn sự thật cho Running/Stopped. Dùng operation pending để hiện spinner và nhãn Starting/Stopping, giữ cùng một geometry và không chuyển sang nút Stop trong lúc Start. Chỉ đổi trạng thái sau snapshot xác nhận.

**Tiêu chí đạt:** Start đang chạy luôn được đọc là Starting, Stop đang chạy là Stopping; Running chỉ xuất hiện sau xác nhận; invocation bị từ chối không để lại trạng thái optimistic.

### P2 — Proxy Start bị từ chối không đưa lỗi về cửa sổ chính (đã sửa source)

**Bằng chứng:** [ProductionModel.swift](ProductionModel.swift#L1061) thoát khi store.invoke không nhận được request. Store ghi nguyên nhân vào store.message trong commandCanBeAccepted tại [Store.swift](Store.swift#L699); ProductionModel chỉ đọc store.message ở một số luồng account/route, không phải luồng proxy. MainWindow chỉ trình bày model.feedback. Vì vậy lỗi service chưa sẵn sàng hoặc request bị từ chối có thể được announce qua Store nhưng không hiện thành lỗi cạnh cửa sổ mà người dùng vừa thao tác.

**Ảnh hưởng:** Nút Start có thể trông như không phản hồi, nhất là khi helper vừa mất kết nối. Đây là vấn đề khác với thành công đang chờ snapshot: lỗi nhận request phải được nói ngay và rõ.

**Hướng sửa:** Khi invocation lifecycle bị từ chối, đưa thông điệp an toàn qua feedback model của main window và giữ trạng thái snapshot cũ. Không bắt đầu spinner nếu không có request ID; với Hub, bỏ giá trị optimistic nếu Start không được nhận.

**Tiêu chí đạt:** Request bị từ chối luôn để lại một lỗi nhìn thấy được, không tạo spinner, không đổi Running/Stopped; retry hoạt động sau khi service sẵn sàng.

### P2 — Kiểm tra layout negotiation của grouped Form khi trạng thái hàng đổi

**Bằng chứng:** Pane thông thường trong [SharedUI.swift](SharedUI.swift#L393) đặt Form bên trong ScrollView ngoài, rồi yêu cầu Form có chiều cao theo nội dung bằng fixedSize. Proxy row đổi operation subtitle và nút khi đang Start; pane action cũng đổi từ Start All sang Restart/Stop All khi có proxy chạy.

**Ảnh hưởng:** Cấu trúc này có thể làm vùng cuộn tính lại nội dung hoặc kích thước khi snapshot thay đổi. Chưa có runtime evidence để kết luận đây là nguyên nhân của hiện tượng bạn thấy.

**Hướng sửa:** Trên fixture, quay màn hình một lượt Start/Stop và scroll ở kích thước cửa sổ thấp/cao. Nếu offset hay cả Form nhảy, để một vùng cuộn duy nhất sở hữu chiều cao và dùng sizing phù hợp với macOS Form; tránh chữa bằng animation cho cả danh sách.

**Tiêu chí đạt:** Khi Start, Stop hoặc action row đổi, content không bật lên, không co giãn cả pane và không đổi scroll offset ngoài thao tác người dùng.

### P2 — Activity và import sheet còn chrome tự vẽ (đã sửa source)

**Bằng chứng:** [ActivityView.swift](ActivityView.swift#L233) tạo events well với nền, RoundedRectangle và viền vẽ bằng overlay. [BackupFlows.swift](BackupFlows.swift#L156) dựng Existing bằng Capsule. Hợp đồng trong README cấm rounded container, overlay stroke và chip/badge ở main window và sheets; vùng Activity được cuộn độc lập vẫn là yêu cầu sản phẩm.

**Ảnh hưởng:** Các bề mặt này trông như panel của app web bên trong cửa sổ System Settings, nên pane mất tính nhất quán dù cấu trúc còn lại dùng Form native.

**Hướng sửa:** Giữ vùng Events/Logs cuộn riêng nhưng bỏ lớp nền/bo góc/viền do app tự vẽ; dùng bề mặt và separators hệ thống. Đổi Existing thành secondary text hoặc SF Symbol cùng accessible label, không pill.

**Tiêu chí đạt:** Activity và backup sheet dùng cùng ngôn ngữ grouped Form của các pane khác; vẫn có thể cuộn, tìm và chọn log text; trạng thái Existing đọc được bằng VoiceOver.

## Đánh giá từng luồng như sản phẩm

### Accounts & APIs

- **Interaction:** đạt theo chủ ý sản phẩm: hover để lộ Switch, click hàng để mở chi tiết. Không đổi thành nút thường trực.
- **Phản hồi pending:** spinner nằm trong cùng nút Switch và cột đã được giữ chỗ, nên affordance không nên làm row reflow.
- **Vấn đề đáng sửa:** current-first sorting có thể chuyển hàng sau thao tác; trạng thái snapshot dư có thể cập nhật toàn danh sách.
- **Thành công:** không cần Switched toast. Checkmark current chuyển sang đúng account là phản hồi trực quan; announcement là đủ cho VoiceOver sau khi xác nhận.
- **Từ chối/thất bại:** cho biết lý do bằng alert hoặc trạng thái lỗi tại account, không để action mất hút.

### Proxies và Proxy Hub

- **Start/Stop:** dùng spinner tại nút đang thao tác; giữ kích thước nút và vị trí hàng. Running/Stopped chỉ đổi sau snapshot xác nhận.
- **Thông tin phụ:** subtitle Starting/Stopping hữu ích nếu service lâu; tránh thêm success toast lặp trạng thái.
- **Proxy Hub:** trạng thái đang chờ phải lấy từ operation, còn Running/Stopped từ snapshot. Không để giá trị optimistic quyết định nhãn Start/Stop hoặc hiện Running trước khi xác nhận.
- **Start All:** hiện gọi setProxyEnabled liên tiếp trong khi production chỉ chấp nhận một mutation một lúc. Cần một batch/queue tuần tự để nhãn Start All có nghĩa; trạng thái chờ của từng hàng phải lần lượt theo cùng một pipeline.

## Trạng thái triển khai và xác minh

1. [x] Coalesce observer và chỉ publish giá trị model thực sự đổi, gồm các collection và trạng thái onboarding.
2. [x] Giữ pattern hover Switch + click-to-detail; hàng account giữ thứ tự ổn định và cột trailing cố định.
3. [x] Bỏ success toast khỏi main window; lỗi tiếp tục dùng alert native, success không chiếm thêm bề mặt UI.
4. [x] Proxy Hub giữ trạng thái snapshot làm nguồn sự thật; request Start bị từ chối hiện lỗi.
5. [x] Start All xử lý proxy theo queue tuần tự; giữ nút pane ổn định trong lúc batch và khóa thao tác lifecycle cạnh tranh.
6. [ ] Mở fixture và so pane với System Settings trên cùng bản macOS; kiểm tra scroll/Form và focus ở kích thước cửa sổ khác nhau.
7. [ ] Nếu fixture vẫn giật, profile SwiftUI update/render khi Start và Switch để tìm view nào invalidates ngoài hàng thay đổi. Không thêm animation toàn pane để che cập nhật dư.

Build kiểm tra biên dịch: `xcodebuild -project src/macos/AnyPick.xcodeproj -scheme AnyPick -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO -derivedDataPath /private/tmp/anypick-ui-derived build` — **BUILD SUCCEEDED**. Đây không phải xác nhận runtime hay hiệu năng; fixture và Instruments còn chờ.

## Ma trận xác minh thủ công

| Luồng | Kết quả mong muốn |
| --- | --- |
| Snapshot không đổi | Không nháy, không mất hover/focus, không dịch scroll, không dựng lại toàn inventory. |
| Switch account | Hover hiện Switch; click nội dung vẫn vào detail; switch đổi current marker in place; các hàng khác không nhảy; từ chối có lỗi hữu ích. |
| Start/Stop proxy | Spinner nằm trong đúng nút; row giữ chiều cao/rộng; trạng thái Running/Stopped chỉ đổi theo snapshot; không có toast thành công. |
| Start All | Mỗi proxy được xử lý tuần tự; từng hàng vào pending đúng lượt; lỗi một proxy không làm mất trạng thái các hàng còn lại. |
| Proxy Hub | Start đang chạy vẫn đọc là Starting, Stop là Stopping; Running/Stopped theo snapshot; busy/refusal/error không hiển thị trạng thái sai. |
| Layout | Pane không co giãn khi operation subtitle/action row đổi; scroll offset ổn định ở danh sách dài, cửa sổ nhỏ và cửa sổ lớn. |
| Accessibility | Tab/Full Keyboard Access, context menu, VoiceOver, Reduce Motion và focus sau thao tác đều rõ; announcement thành công chỉ có một lần sau xác nhận. |

## Tham chiếu

- [Apple HIG — Settings](https://developer.apple.com/design/human-interface-guidelines/settings)
- [Apple HIG — Buttons](https://developer.apple.com/design/human-interface-guidelines/buttons)
- [Apple HIG — Progress indicators](https://developer.apple.com/design/human-interface-guidelines/progress-indicators)
- [Apple HIG — Sidebars](https://developer.apple.com/design/human-interface-guidelines/sidebars)
- [Mobbin](https://mobbin.com/) — gợi ý thêm cho flow và nội dung, không thay System Settings/HIG làm chuẩn cho control macOS.

# Telegram Desktop Compatibility Matrix

Client: Telegram Desktop dev 9caf32dffc (describe v6.8.4-15-g9caf32dffc, pinned baseline)
Server: telesrv
gotd/td: v0.144.0
Layer: 225
Date: 2026-06-02

status 取值：done(真实实现) / stub(兼容响应) / todo(已发现未实现) / blocked(需先补协议或数据结构)

> 连接层（mtprotoedge）M0–M5 已完成。**登录注册闭环已打通**：
> auth.sendCode/signIn/signUp/logOut + users.getUsers + updates.getState 真实实现，
> telegram.Client 端到端验证（sendCode→signIn→signUp→getUsers→启动后空账号 RPC，TestLoginRegisterFlow）。
> 启动必经的空账号 RPC 已从硬编码 stub 推进到 PG-backed 业务查询：
> app_configs / countries / account_passwords / temp_auth_key_bindings /
> update_states / contacts / dialogs / langpack，语言包 seed 来自导出的 TDesktop 数据。
> TDesktop 进入空账号主界面后的异步预取 RPC 已补第一阶段空响应，避免后台重试噪声。
> PG 集成测试覆盖 AuthKeyStore 与业务 store 往返（TestBusinessStoresRoundTrip）。
> active session 已缓存 raw auth_key_id、业务 auth_key_id、session_id、user_id；router 入口只做一次 temp→perm 与 auth_key→user_id 解析，业务服务按 user_id 查询；普通注册 user_id 从 1780243200 起递增。
> 二阶段私聊文本消息链路已落地：`messages.sendMessage` 事务内写 `private_messages` / 双端 `message_boxes` / `dialogs` / `user_update_events` / `dispatch_outbox`，`pts` 与 owner 视角 `box_id` 由 Redis 原子计数并可从 PG durable log 恢复；outbox worker 批量推送在线 session，链路指标覆盖发送、限流、outbox claim/deliver/fail。
> 联系人与 dialog owner 视角已补齐：`contacts.addContact/acceptContact/importContacts/deleteContacts/updateContactNote/getContactIDs/getStatuses/search`、dialog pinned/manual unread/peer settings、`messages.*DialogFilter*` 与 `folders.editPeerFolders` 均真实落库；`contacts` 保存每个 owner 对同一 user 的独立姓名/电话/备注，`dialogs` 保存置顶顺序、manual unread、action bar 隐藏态与 archive folder_id，自定义 filters 独立存 `dialog_filters`。
> username 生命周期已补齐：注册仍按 Telegram 语义只创建手机号/姓名账号，后续通过 `account.checkUsername/updateUsername` 设置或清除主 username；PG 以 `lower(username)` partial unique index 防并发占用，`contacts.resolveUsername/resolvePhone` 可用于 TDesktop 搜索/链接解析。
> 注册资料闭环已补齐：TDesktop 手机号验证码后进入 sign-up 页，`auth.signUp` 携带 first_name/last_name 完成建号；主界面 Settings/Profile 的姓名与 bio 变更走 `account.updateProfile`，写入 `users.first_name/last_name/about` 并向其它设备可靠推送 `updateUserName`。
> 联系人 reset、dialog pinned/order/manual unread/peer settings、dialog filters/order/reload、folder peers 已写入账号级 durable update log，并同事务写入 `dispatch_outbox`；离线设备可通过 `updates.getDifference` 恢复这些状态变化，在线其它端由 outbox worker 可靠投递且排除当前 session。
> PG 集成测试覆盖“两个 owner 给同一联系人不同备注、改备注 hash 变化、互相关系同步、删除反向 mutual 清理、dialog 列表按当前 owner 备注展示、置顶重排、manual unread、隐藏 peer settings bar、归档 folder 与自定义 filter”。
> 2026-05-31 TDesktop Debug 客户端已用独立 workdir 启动并连到当前 server，日志证明 key exchange、`auth.bindTempAuthKey`、启动配置 RPC、搜索用户、打开私聊、发送消息、`messages.getDialogFilters` 与自定义 filter 左侧分组展示均可走通；本轮 Settings/Folders 搜索页额外触发的 account/contacts/messages 预取 RPC 已补第一阶段兼容 stub。
> 2026-05-31 双 TDesktop 在线/离线复测已覆盖私聊 read/edit/delete：在线 read 触发 sender `Seen`，在线 edit/delete 两端同步；Bob 离线时 Alice send+edit，Bob 重启拿到最终文本并可回读，Bob 离线期间 Alice revoke delete，Bob 重启后会话和消息均清除。联调中补齐 `messages.getOutboxReadDate`，并修正 MTProto content-related service request 口径（`ping*` / `get_future_salts` / `msgs_state_req` / `msg_resend_req` / `destroy_*` 需要 odd seq_no 与 msgs_ack），23:51 后复跑日志无新增 bad_msg / NOT_IMPLEMENTED。
> 2026-06-01 双 TDesktop 在线/离线复测已覆盖超级群普通发送、目标会话 reply、forward 到同一超级群、频道消息编辑与离线恢复：Alice/Bob 双窗口均能实时看到对应 `UpdateNewChannelMessage` / `UpdateEditChannelMessage` 结果；Bob 关闭期间 Alice 发送 channel 消息，Bob 重启后 dialog 未读数=1，打开群后通过恢复路径看到离线消息；server 日志无新增 `NOT_IMPLEMENTED` / `Unhandled RPC` / `bad_msg` / panic。删除、清历史、踢/禁言属于破坏性 UI 操作，需行动时确认后再点客户端确认。
> 2026-06-02 Computer Use 双 TDesktop 复测已覆盖超级群双向在线发送、成员栏显示、全局搜索命中 channel/supergroup 消息与公开频道广播发帖：Alice/Bob 在 `E2E Super 0307` 中互发 `cu-round-alice-*` / `cu-round-bob-*` 并实时显示，Alice 搜索 Bob 消息返回 `Found 1 message`，`CU Public Search 44238` 频道发 `cu-channel-round-*` 后消息流与左侧预览同步更新；server 日志无新增 `NOT_IMPLEMENTED` / `Unhandled RPC` / `bad_msg` / panic，客户端本轮无新增 participant 告警。清空搜索框触发的 `SEARCH_QUERY_EMPTY` 保持可解释。
> 2026-06-02 09:55 Computer Use 双 TDesktop 复测补充覆盖 reaction/sticker 启动依赖：Debug/Alice 与 DebugBob/Bob 同时打开 `E2E Super 0307`，互发 `cu-stubfix-alice-*` / `cu-stubfix-bob-*` 并在双方消息列表与左侧 preview 可见；打开 emoji 面板触发 `messages.getAvailableReactions` / `messages.getStickerSet` / `messages.getAvailableEffects`，server 日志无新增 `NOT_IMPLEMENTED` / `Unhandled RPC` / `bad_msg` / panic，Debug 当前 `log.txt` 无新增 `Unexpected messages.stickerSetNotModified` / participant 告警。右键消息菜单可打开但未显示 reaction 快捷项，真实 reaction sticker animations/custom UI 留后续。
> 2026-06-02 10:10 Computer Use 双 TDesktop 复测再次覆盖当前主干频道/超级群路径：Debug/Alice 与 DebugBob/Bob 在 `E2E Super 0307` 中双向在线发送 `cu-continue-*`，消息流与左侧 preview 双端可见，`channels.readHistory` 清除未读；Alice 在 `CU Public Search 44238` 发布 `cu-channel-continue-*`，频道消息流和左侧 preview 同步更新。server 日志无新增 `NOT_IMPLEMENTED` / `Unhandled RPC` / `bad_msg` / panic，Debug 当前日志无新增 API error；DebugBob 主日志仅有 08:05 前旧 sticker/participant 噪声。
> 2026-06-02 11:20 Computer Use 双 TDesktop recent reactions 相关冒烟：新 server 应用 migration 0052 后，Debug/Alice 与 DebugBob/Bob 同时连接 `E2E Super 0307`；Bob 发送 `cu-recent-bob-1780370400982`，双方消息流与左侧 preview 可见；Bob 打开 emoji 面板，Emoji/Stickers/GIFs 面板正常渲染。server 日志无新增 `NOT_IMPLEMENTED` / `Unhandled RPC` / `bad_msg` / panic，Debug/DebugBob 最新日志无新增 API error/Unexpected；本轮 TDesktop 受客户端缓存影响未观察到重新发送 `messages.getRecentReactions`，get/clear 的 hash/notModified/clear 后空列表语义由 `internal/rpc` router 测试覆盖。
> 2026-06-02 11:36 Computer Use 双 TDesktop top reactions 回归：新 server 应用 migration 0053 后，Debug/Alice 与 DebugBob/Bob 同时连接 `E2E Super 0307`；Bob 发送 `cu-toprx-bob-1780371301984` 后双方消息流与左侧 preview 可见，emoji 面板保持 Emoji/Stickers/GIFs 正常渲染，右键消息菜单可打开。server 日志无新增 `NOT_IMPLEMENTED` / `Unhandled RPC` / `bad_msg` / panic，Debug 当前日志无新增 API error，DebugBob 仅有 08:05 前旧 sticker/participant 噪声；TDesktop 本轮命中本地 reaction 缓存未重新请求 `messages.getTopReactions`，账号 top 排序、catalog 兜底与 hash/notModified 语义由 `internal/rpc` router 测试覆盖。
> 2026-06-02 媒体闭环已落地：migration `0057_media`（upload_parts/file_blobs/documents/photos/sticker_sets/available_reactions/profile_photos + 消息表 media JSONB 列）/`0058`（appConfig reactions_default）/`0059`（channel photo 列）；新增 `upload.saveFilePart/saveBigFilePart/getFile/getFileHashes`、`photos.uploadProfilePhoto/updateProfilePhoto/getUserPhotos/deletePhotos`，本地磁盘 blob backend；`getAvailableReactions/getStickerSet/getAllStickers/getEmojiStickers/getCustomEmojiDocuments` 返回真实 document（启动从 `TELESRV_STICKER_SEED_DIR` 幂等 seed 74 reactions/24 sets/~1.5k docs/~3k blob 索引）；`messages.uploadMedia/sendMedia/sendMultiMedia` 支持 photo/document/sticker 主路径（私聊 + channel，复用 pts/box/outbox/在线推送/离线 difference）；User/Channel/Full 渲染真实头像。**自动化验证**：`go test ./...`（含 PG 集成测试 167s 全过，覆盖媒体列 + 频道 photo 列扫描）、server 启动 migrate+seed 无错误、`internal/rpc` 新增贴纸/图片 sendMedia 端到端单测全过。**待人工**：双 TDesktop 测私聊/超级群/频道的图片/文件/贴纸消息、reaction 面板真实动画、头像、在线 fanout 与离线 difference，确认日志无新增 `NOT_IMPLEMENTED`/`Unhandled RPC`/`bad_msg`/panic/API error。范围外 poll/todo/scheduled/paid/CDN/sticker 管理（install/uninstall/reorder）/grouped_id 相册聚合继续显式 stub/todo。
> 2026-06-03 媒体接手审计补充：全量 `go test ./...`（PG/Redis env-gated，169s）通过，`TestSeedMediaFromRealExport -count=1` 确认 `TELESRV_STICKER_SEED_DIR` seed 为 74 reactions / 11 sets（maxRegularSets=2）/ 1355 docs / 2682 blobs。参考 TDesktop `image_location_factory.cpp` 发现 `StickerSet.thumbs` 的可下载 `PhotoSize` 会触发 `inputStickerSetThumb`，而当前导出目录仅有 set_cover `PhotoPathSize` SVG、无可服务 raster blob；已在 seed 与 TL 转换层过滤 sticker set cover 的 downloadable thumb，只保留非下载占位，避免 TDesktop 生成不可满足的 cover 下载请求。另修复 channel/supergroup 仅媒体无 caption 被 `SendChannelMessage` 误判空消息的问题，并新增 PG 集成测试覆盖 channel media `updates.getChannelDifference` 恢复。**仍待人工**：双 TDesktop 按本轮目标完整验证图片/文件/贴纸、头像、reaction 动画下载与在线/离线同步日志。
> 2026-06-03 头像/资料路径审计补充：Alice 上传用户头像时 TDesktop 额外触发 `account.getDefaultGroupPhotoEmojis`、`account.getConnectedBots`、`stories.getAlbums`；群/频道头像编辑页继续触发 `messages.getEmojiProfilePhotoGroups`。参考 TDesktop 消费点、gotd Layer225 返回类型、参考实现 空集合 handler、参考实现 emoji-categories DAO/connected-bots/custom-emoji 空集合实现后，当前按有界兼容 stub 返回空列表，避免资料页/头像编辑后台 `NOT_IMPLEMENTED` 与 business/stories/custom-emoji 模型扩张。自动化验证：`go test ./internal/compat/tdesktop ./internal/rpc -run "Test(EmojiProfilePhotoGroups|TDesktopStartupRPCsEncode)" -count=1`、`go test ./...`、`TestSeedMediaFromRealExport -count=1` 均通过，migration 状态 `59|f`；双 TDesktop 群/频道头像 UI 回归仍待 Computer Use 恢复后补证据。
> 2026-06-03 reaction 全路径审计与双 TDesktop 回归：参考 TDesktop `api_reactions_notify_settings.cpp`、`data_message_reactions.cpp`、`api_updates.cpp`，参考实现 reaction 设置/聚合/更新语义，以及 gotd Layer225 `messages.sendReaction` / `updateMessageReactions` / paid/privacy/participant/stories 入口后，补齐私聊 reaction durable event 与账号设置/paid privacy/participant 删除/stories 兼容入口。自动化验证：`go test ./internal/app/updates ./internal/rpc -run "Test(GetState|GetDifference|Record|UpdatesGetState|UpdatesDifferenceIncludesReaction|MessagesSendReactionPrivate)" -count=1`、`go test ./... -count=1` 通过。Computer Use 双端实测仅使用 `patched Telegram.exe -workdir .tdata-alice`（Alice）与 `patched Telegram.exe -workdir .tdata-bob`（Bob）：私聊在线 reaction Alice 实时同步；Alice 离线后 Bob 改 reaction，Alice 重启经 `updates.getDifference` 恢复；超级群 `AAAA` 右键 reaction 快捷栏不再空白，在线同步正常；Alice 离线后 Bob 改群消息 reaction，Alice 重启打开群后经 `messages.getHistory` / `updates.getChannelDifference` / `messages.getMessagesReactions` 恢复。最终 server `bin\\telesrv.exe` 监听 2398，日志无新增 `NOT_IMPLEMENTED` / `Unhandled RPC` / `bad_msg` / panic / internal error。
> 2026-06-03 unread reaction / 打开历史卡顿修复回归：参考 TDesktop `UnreadThings::requestReactions`、`History::clearUnreadReactionsFor`、`SendAsPeers::request`，参考实现 `readMessageContents/readReactions` 重算 unread reaction 语义，参考实现 send-as/reaction read model 与 gotd Layer225 `channels.readMessageContents` / `messages.getUnreadReactions` / `channels.getSendAs` 后，修复 channel/supergroup 打开会话只读 visible messages、不清作者视角 unread reaction 的问题；并去掉 `channels.getSendAs` handler 内 access_hash 校验 + 后续取 channel 的重复 `GetChannel`。验证：Alice Debug 打开 `AAAA` 后 `channel_dialogs.unread_reactions_count` 从 1 变 0，`channel_message_reactions.unread` 从 true 变 false；重启 Alice 后 `AAAA` 会话列表不再出现红色 unread reaction 图标；DebugBob/Bob 同时启动并打开 `AAAA`，history 正常展开。最新 server 日志无新增 `NOT_IMPLEMENTED` / `Unhandled RPC` / `bad_msg`，Alice 打开 `AAAA` 的 `messages.getHistory` 为 10.8ms/5.3ms，Bob 为 34.0ms/15.9ms，`channels.getSendAs` 去重后主路径约 10-27ms（Alice 一条 72ms 属独立预取调用，未伴随 history 慢查询）。
> 2026-06-03 sticker/reaction 启动资源检查修复：参考 TDesktop `api_hash.cpp` / `data_stickers.cpp` 后，`messages.getAllStickers` / `messages.getEmojiStickers` 的 catalog hash 改为客户端同公式（按 set 顺序仅 `HashUpdate(set.hash)`，跳过 archived/default set），避免 Debug/DebugBob 日志反复出现 `received stickers hash ... while counted hash ...` 并导致重启后 full catalog 失效重拉；`messages.getAvailableReactions` 继续用服务端稳定 hash，但统一到同一 hash helper，命中时返回 `messages.availableReactionsNotModified`。自动化验证：`go test ./internal/rpc -run "TestSticker|TestMessagesGetAllStickers|TestMessagesGetAvailableReactions" -count=1`、`go test ./internal/rpc -count=1`、`go test ./internal/app/files -count=1`、`go test ./... -count=1` 均通过；双 TDesktop Debug/Alice + DebugBob/Bob 连新 server 重启两轮，server 日志出现 `messages.getAvailableReactions/getStickerSet/getStickers` 与一次成功 `upload.getFile`，无 `NOT_IMPLEMENTED` / `Unhandled RPC` / `bad_msg` / `LOCATION_INVALID`，客户端新日志无 `received stickers hash ... counted ...` / `Unexpected messages.stickerSetNotModified`。
> 2026-06-03 sticker/reaction 历史首开资源冷路径修复：参考 TDesktop `ApiWrap::requestStickerSets`、`Data::Stickers::somethingReceived`、`DocumentMedia::checkStickerSmall` 后确认，打开历史时若 sticker set 处于 `NotLoaded` 或文档缩略图/动画首次解码，客户端会拉完整 `messages.getStickerSet(hash=0)` 并依赖 128px document thumb 先显示。Files service 新增 `object_key→小 blob bytes` LRU（单项 ≤256KB，总 64MB）和完整 sticker set cache，server 启动 `WarmCaches` 从已 seed 的 sticker/reaction 元数据预热 set、document 与可下载缩略图。验证：`go test ./internal/app/files -count=1`、`go test ./internal/rpc -count=1`、`go test ./... -count=1` 均通过；`bin\\telesrv.exe` 启动日志预热 `sticker_sets=24 documents=1554 blobs=2809`；Computer Use 启动 Debug/Alice 与 DebugBob/Bob 并点击含 sticker 历史会话，`messages.getStickerSet` 从冷路径约 18-24ms 降到 0-1.7ms，点击历史 `messages.getHistory` 为 5-16ms，未再触发 `upload.getFile` 冷下载；server/client 日志无新增 `NOT_IMPLEMENTED` / `Unhandled RPC` / `bad_msg` / `LOCATION_INVALID` / `Unexpected messages.stickerSetNotModified`。
> 2026-06-03 sticker installed-cache 语义修复：继续对照 TDesktop `Storage::Account::writeInstalledStickers` / `Data::ParseStickersSetFlags` 后确认，`installed_date` 会让 set 进入 Installed；普通 installed stickers 集若仍处于 `NotLoaded`，客户端会中止本地 installed stickers 写入。telesrv seed 过去把 `InputStickerSetAnimatedEmoji` / dice / generic animations 等 system set 也标成 installed，导致系统资源混入普通 installed stickers 缓存路径。现 migration `0066_system_sticker_sets_not_installed` 修复既有库，并改 seed：`set_kind=system` 仅作为按 input 系统 key 解析的内置资源，不再声明为 installed。
> 2026-06-03 sticker 静态缩略图元数据修复：继续读 TDesktop `history_view_sticker.cpp` / `data_document_media.cpp` / `data_cloud_file.cpp` 后确认，历史页若看到 document `PhotoPathSize` 会把它当成 vector placeholder，`dataMediaCreated()` 因 `thumbnailPath()` 非空跳过 `thumbnailWanted()`，从而挡住同时存在的真实 raster thumb。seed 现将 ≤32KB 可下载 document thumb 转为 `PhotoCachedSize` 并写入 image cache 所需 bytes；当 document 已有 raster/default/cached/progressive thumb 时丢弃 `PhotoPathSize`，仅没有 raster 的 set cover 继续保留 path 占位；thumb blob MIME 由字节魔数写入，`upload.getFile` 也优先按魔数返回 `storage.fileWebp`，兼容旧库误标 `image/jpeg`。验证：`go test ./internal/app/files -run "TestSeedMediaFromRealExport|TestSeedPreferRasterDocumentThumbsDropsPathWhenRasterExists|TestDocumentsNeedInlineCachedThumbsDetectsPathWithRaster|TestDocumentsNeedInlineCachedThumbsDetectsStaleMime" -count=1`、`go test ./internal/rpc -run "TestStorageFileType" -count=1`、`go test ./... -count=1` 通过；新 server 启动 repair 后 PG `documents.thumbs` kind 分布为 `cached=2313`，两条样例 `5415908822013185960/5381935901284774004` 均仅 `cached/m`，`file_blobs` document thumb MIME 为 `image/webp=2313`；服务进程 PID 53892 监听 2398。
> 2026-06-03 sticker 文档身份修复：继续对比 TDesktop `data_session.cpp` / `data_document.cpp` / `history_view_sticker.cpp` 后确认，TDesktop 以 `document_id` 复用 `DocumentData`，且 `DocumentData::updateThumbnails()` 不会清掉旧的 inline/path thumbnail 状态；一旦服务端把外部导出 document id 当成本服资源主键，旧 Debug tdata 中同 id 的污染对象会持续影响历史页渲染。telesrv 现把导出 JSON/文件名中的 document id 只作为 seed source id，在 `internal/app/files` 导入阶段归一为 telesrv-owned storage id；RPC、`InputDocument`、`inputDocumentFileLocation`、`getCustomEmojiDocuments`、channel custom emoji status/reaction/color 均直接使用同一个服务端 document id，不再做边界映射。migration `0067_seed_document_id_namespace` 同步修复既有开发库中的 documents/file_blobs/sticker_sets/available_reactions/message media/channel appearance 引用。document thumb 在 store 中可保留 cached bytes，但 RPC 对 document 统一暴露 downloadable `photoSize m`，避免 `PhotoCachedSize` 与本地旧 cache 组合出不可替换状态。验证：`go test ./internal/app/files -run TestSeedMediaFromRealExport -count=1 -v`、`go test ./... -count=1` 通过；server `bin\\telesrv.exe` 监听 2398，migration 状态 `67|false`，PG `documents/file_blobs/message media/available_reactions/sticker_sets` 均无 `>4e18` 外部 document id；Computer Use 重启 Debug/Alice 与 DebugBob/Bob 后分别打开 Bob B/Alice A，250ms 截图已显示 sticker，server 与两端客户端日志无 `NOT_IMPLEMENTED` / `Unhandled RPC` / `bad_msg` / `LOCATION_INVALID` / `API Error` / sticker hash 异常。
> 2026-06-07 sticker 静态图慢复盘 / 撤销 a.2 的 path 丢弃：再读 TDesktop `history_view_sticker.cpp`（`draw→paintPixmap→paintPath` 占位优先级，`thumbnailInline` 内联分支被源码注释禁用）与 `data_document_media.cpp`（`checkStickerLarge→automaticLoad` 下载完整 `.tgs`，与 path/thumbnail 无关）后确认：上方 2026-06-03「sticker 静态缩略图元数据修复」丢弃 `PhotoPathSize` 是误判（真根因是同日 document id 污染，已由 `0067` 修复）；id 修复后再丢 path 反而使 animated sticker 失去唯一即时占位 ⇒ 打开会话先空白，并因 `thumbnailPath()` 变空多触发一次缩略图 `getFile`，与完整 `.tgs` 争用下载通道 ⇒「静态图特别慢、不像本地加载」。现 `internal/app/files/seed.go` 恢复 `PhotoPathSize` 占位（删 `seedPreferRasterDocumentThumbs` 及 path+raster stale 判定），document.thumbs = path + downloadable `photoSize m`。已实测排除磁盘 IO/缓存容量（blob 4330 文件 71.3MB，几乎全 ≤64KB）与 `document.Size` 不匹配（28/28 一致）。验证：`go test ./internal/app/files ./internal/rpc -count=1`、`TestSeedMediaFromRealExport -count=1`（真实导出 74 reactions/11 sets/1355 docs/2710 blobs，断言 seed 后 document 保留 path）、`go vet` 通过。**现有库需重新 seed 一次**：`DELETE FROM sticker_sets; DELETE FROM available_reactions;` 重启触发全量 reseed（`PutDocument`/`PutFileBlob` upsert，不删用户上传媒体）。待双 TDesktop 人工验证渲染体感。

## Transport / MTProto 服务消息

| 消息 | status | behavior | note |
|---|---|---|---|
| transport codec 协商 | done | real | intermediate/abridged/full 自动探测 |
| TCP obfuscation | done | real | TDesktop tcpo_only：先解 64 字节 obfuscated 前缀，再探测 abridged codec |
| TDesktop reconnect fake req_pq | done | real | 既有 auth key 重连时先 fake req_pq 再发加密帧；server 回 resPQ 后 replay 后续加密帧，不断开 session |
| 密钥交换 (req_pq…dh_gen_ok) | done | real | exchange.Server，auth key 落 AuthKeyStore |
| 加密消息收发 | done | real | crypto.ServerCipher + container/gzip |
| new_session_created | done | real | 连接首个加密消息后发送 |
| msgs_ack | done | real | content-related 消息统一确认；口径对齐 TDesktop `SerializedRequest::needAck()`，`ping*` / `get_future_salts` / `msgs_state_req` / `msg_resend_req` / `destroy_*` 都按 content-related 处理 |
| msgs_state_req | done | real-minimal | 连接层按当前连接已见 client msg_id 返回 msgs_state_info，并作为 content-related request 回 ack，避免 TDesktop 重连状态探测落到 RPC fallback；完整 out queue 状态留后续 session 状态机扩展 |
| msg_resend_req | done | real-minimal | 连接层按当前连接已见 client msg_id 兜底回复 msgs_state_info，并作为 content-related request 回 ack；后续完整 outgoing queue 后再补真实重发 |
| rpc_drop_answer | done | real-minimal | 连接层以 rpc_result 包装 rpc_answer_unknown，避免清理请求落到业务 RPC fallback |
| destroy_session | done | real-minimal | 返回 destroy_session_ok/none；命中非当前活跃 session 时清理运行态索引与 SessionStore 记录 |
| http_wait | done | real-minimal | TCP/container 场景下解码后吞掉，不加入 msgs_ack；HTTP long-poll 语义第一阶段不启用 |
| destroy_auth_key | done | real-minimal | TDesktop 清理旧 auth key 时连接层直接返回 destroy_auth_key_ok，避免落到业务 RPC fallback；物理删除 auth key 留后续安全清理任务 |
| active session update gate | done | real | 在线 session 先注册但不接收业务 updates；`updates.getState/getDifference` 后才放开，期间主动推送暂存，语义对齐 参考实现 session 的 canSync |
| ping / ping_delay_disconnect | done | real | 回 pong，并作为 content-related request 回 msgs_ack；TDesktop 保活 odd seq_no 不再触发 bad_msg code 34 |
| get_future_salts | done | real | 返回当前 auth key 的权威 server_salt 有效窗口，并作为 content-related request 回 ack |
| bad_msg_notification | done | real | msg_id==0 等非法情形 |
| bad_server_salt | done | real | 客户端带错 salt 时返回 error_code=48 与当前权威 salt |

## Wrapper

| 方法 | status | behavior | note |
|---|---|---|---|
| invokeWithLayer | done | unwrap | 注入 layer 到 RPC ctx |
| initConnection | done | unwrap | 注入设备/应用信息到 RPC ctx |
| invokeWithoutUpdates | done | unwrap | 透传内层 query |

## Boot / Config

| method | status | behavior | note |
|---|---|---|---|
| help.getConfig | done | real | 返回含自建 DC 的 DCOptions |
| help.getNearestDc | done | real | 返回自建 DC |
| help.getAppConfig | done | real | PG-backed app_configs；默认 seed 包含 TDesktop read mark config：`chat_read_mark_size_threshold=50`、`chat_read_mark_expire_period=604800`、quote reply config：`quote_length_max=1024`，以及 anti-spam UI config：`telegram_antispam_group_size_min=200`、`telegram_antispam_user_id=5434988373` |
| help.getCountriesList | done | real | PG-backed countries/country_codes；默认 seed US/CN |
| help.getTimezonesList | stub | small-list | TDesktop Business hours 预加载入口；返回非空常用时区列表和 hash/notModified，避免客户端周期性 `NOT_IMPLEMENTED` |
| help.getPeerColors | stub | notModified | TDesktop 主界面加载 peer 色板；第一阶段无服务端色板 |
| help.getPeerProfileColors | stub | notModified | TDesktop 主界面加载 profile 色板；第一阶段无服务端色板 |
| help.getPromoData | stub | empty | 无 PSA/MTProxy 推广，返回空并设置短期 expires |
| help.getTermsOfServiceUpdate | stub | empty | 第一阶段无 TOS 更新 |
| help.getPremiumPromo | stub | empty | 第一阶段不做 Premium 展示数据 |

## Auth

| method | status | behavior | note |
|---|---|---|---|
| auth.bindTempAuthKey | done | real-validated | 校验 encrypted bind_auth_key_inner（nonce/temp key/perm key/session/expires_at）后记录 temp_auth_key_bindings；后续 temp auth_key RPC 会解析为 perm auth_key 身份，并刷新 active session 的业务 auth_key_id / user_id 缓存 |
| auth.exportLoginToken / auth.importLoginToken | stub | tdesktop-qr-placeholder | TDesktop QR 登录页轮询；当前单 DC 主路径只支持手机号登录，返回短期 auth.loginToken 避免未实现噪声；`auth.acceptLoginToken` 显式返回 `AUTH_TOKEN_INVALID`，不伪造跨设备 QR 授权 |
| auth.exportAuthorization / auth.importAuthorization / auth.dropTempAuthKeys | stub | single-dc-bounded | 单 DC 当前不做跨 DC authorization handoff；export/import 返回 `DC_ID_INVALID`，dropTempAuthKeys 返回成功避免旧 temp key 清理路径噪声 |
| auth.initPasskeyLogin | stub | empty-options | DrKLO Android 手机号页会后台预取 passkey 登录；当前不接 passkey，返回空 `auth.passkeyLoginOptions` 避免反复 NOT_IMPLEMENTED |
| auth.sendCode / auth.resendCode / auth.cancelCode | done | dev-code | 开发固定验证码，phone_code_hash 存 Redis CodeStore；手机号统一规范为纯数字；resend 会校验并失效旧 hash 后生成新 hash，cancel 会删除 pending code |
| auth.signIn | done | real+2fa-gate | 校验码；兼容 TDesktop sendCode 后 signIn 使用 DigitsOnly(phone)；无 2FA 时登录成功绑定在线 session 的 user_id，写入并延迟推送 777000 官方登录消息 updateNewMessage，同时向其它在线 session 推送新登录 updateServiceNotification；账号已启用 2FA 时返回 `SESSION_PASSWORD_NEEDED` 并等待 `auth.checkPassword` |
| auth.signUp | done | real | 建 user + 绑定 authorization；登录成功绑定在线 session 的 user_id，写入并延迟推送 777000 官方登录消息 updateNewMessage |
| auth.logOut | done | real | 解绑当前 auth_key 的授权，并清理同业务 auth_key 的活跃连接 user_id 缓存与 auth_key+user update state；账号级 user_update_events 不随设备退出删除，退出后同设备换号不会继承旧账号差分 |
| auth.resetAuthorizations | done | real | 删除当前账号除当前 auth_key 外的其它授权，清理对应 session/auth cache 与设备 update state |
| auth.checkPassword / auth.requestPasswordRecovery / auth.checkRecoveryPassword / auth.recoverPassword | done | real-srp-dev-email | 按 Telegram SRP KDF 校验 `inputCheckPasswordSRP`；恢复邮箱只保存/返回 pattern，开发环境使用固定恢复码 `12345`，未接真实邮件网关；recover 可清除或重设 2FA |
| account.checkUsername | done | real | username 规则为 ASCII 字母开头、后续字母/数字/下划线、5-32 字符；大小写不敏感检查占用，已被本人占用视为可用 |
| account.updateUsername | done | real | 设置或清除当前账号主 username；大小写不敏感唯一约束由 PG partial unique index 兜底，并向其它在线 session 推送 updateUserName |
| account.updateProfile | done | real | 更新当前账号 first_name/last_name/about；first_name 必填且姓名 64 字符内，about 70 字符内；资料变化后向其它在线 session 推送 updateUserName，`users.getFullUser` 返回 about |
| account.getPassword / account.getPasswordSettings / account.updatePasswordSettings | done | real-srp | PG-backed `account_passwords`；返回 Layer225 `passwordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow` 参数；支持设置/修改/清除 2FA、hint、恢复邮箱、SRP challenge 轮换与错误映射 |
| account.confirmPasswordEmail / account.resendPasswordEmail / account.cancelPasswordEmail | done | dev-email-code | 开发环境恢复邮箱码固定为 `12345`；confirm/resend/cancel 走持久 recovery code 状态，未接真实邮件网关 |
| account.resetPassword / account.declinePasswordReset | done | real-wait-state | 无恢复邮箱的 2FA 账号可进入 7 天 reset wait，等待期到期再次调用清除 2FA；decline 清除 pending reset；有恢复邮箱时返回 `PASSWORD_RECOVERY_NA` 让客户端走邮箱恢复 |
| account.getAccountTTL | stub | default-365d | TDesktop Settings/self-destruct 预取；当前不做账号自动销毁持久配置，返回正数默认 TTL 避免后台 NOT_IMPLEMENTED |
| account.setAccountTTL | stub | ack | 当前不持久化账号自动销毁 TTL，接受写入并返回成功 |
| account.getNotifySettings | stub | default | TDesktop 主界面读取通知设置；显式返回 show_previews=true、silent=false、mute_until=0 与 default sound，避免空 settings 被客户端按静默展示 |
| account.updateNotifySettings / account.resetNotifySettings / account.getNotifyExceptions | stub | ok-empty | 当前不持久化通知偏好/例外，但接受 TDesktop 设置写入并返回空 updates，避免本地通知状态 RPC 报错 |
| account.getPrivacy | done | real-privacy | 按 owner+key 读取 `account_privacy_rules`；无记录时默认 PhoneNumber=DisallowAll、Birthday=AllowContacts、其它=AllowAll；支持 Layer225 全部 privacy key/rule kind，users/chats 按规则回填 |
| account.setPrivacy | done | real-privacy/online-update | 校验 `InputPrivacyKey/InputPrivacyRule` 与 input user access_hash 后持久化；返回 `account.privacyRules` 并向当前账号在线 session 推 `updatePrivacy`；Stars/Premium/Bot 等高级产品只保存规则，不扩展商业模型。离线 `updates.getDifference` 恢复 `updatePrivacy` 仍需扩展 `user_update_events` privacy payload |
| account.getAuthorizations / account.resetAuthorization | done | real-devices | 从 `authorizations` 返回当前账号设备列表，带 current/hash/device/app/ip/date；resetAuthorization 按 hash 删除同账号授权并清理 session/auth cache |
| account.getWebAuthorizations / account.resetWebAuthorization / account.resetWebAuthorizations | stub | empty/ack | 当前无 Telegram Login Widget/Web 授权模型，返回空列表并接受清理请求 |
| account.registerDevice / account.unregisterDevice | stub | ack | 当前未接真实 APNS/FCM 推送网关，接受 TDesktop 设备 token 注册/注销避免后台噪声 |
| account.getDefaultEmojiStatuses / account.getRecentEmojiStatuses / account.clearRecentEmojiStatuses / account.updateEmojiStatus | stub | empty/notModified | 当前不提供云 emoji status；读取返回空或 notModified，写入/清理返回成功 |
| account.getCollectibleEmojiStatuses | stub | empty | TDesktop 启动/emoji status 面板会刷新 collectible gift emoji statuses；当前无 gift/status 模型，返回空 `account.emojiStatuses` |
| account.getDefaultProfilePhotoEmojis / account.getDefaultGroupPhotoEmojis / account.getDefaultBackgroundEmojis / account.getChannelDefaultEmojiStatuses / account.getChannelRestrictedStatusEmojis | stub | empty | TDesktop 头像/背景/频道状态编辑页默认 emoji 入口；当前无 custom-emoji 候选，返回空 `emojiList` 或空 statuses |
| account.getConnectedBots | stub | empty | TDesktop business chatbot 资料预取；当前无 business bot 模型，返回空 `account.connectedBots`，避免客户端 API error |
| account.getReactionsNotifySettings / account.setReactionsNotifySettings | done | real-account-settings | 持久化账号 reaction 通知范围（none/contacts/all）与 show_previews，返回默认提示音；无记录时默认 contacts + previews |
| account.getContactSignUpNotification / account.setContactSignUpNotification | stub | false/ack | 当前不推送联系人注册提醒，读取 false、写入接受 |
| account.getThemes | stub | notModified | Settings/theme 预取，第一阶段不提供云主题 |
| account.getContentSettings / account.setContentSettings | stub | default/ack | Settings 内容敏感项预取，当前不启用 NSFW 内容配置，写入接受 |
| account.getGlobalPrivacySettings / account.setGlobalPrivacySettings | stub | default/echo | Settings 全局隐私项预取，返回空默认设置；写入回显，不扩展全局 privacy 模型 |
| account.getPasskeys | stub | empty | Settings 安全项预取，第一阶段不提供 passkeys |
| account.getAutoDownloadSettings / account.saveAutoDownloadSettings | stub | defaults/ack | TDesktop 自动下载设置预取；返回保守默认下载阈值，写入接受但不持久化 |
| account.setAuthorizationTTL / account.changeAuthorizationSettings | stub | ack | Settings 设备管理长尾写入入口；当前只维护 active authorization 删除，不持久化授权 TTL/设备名设置 |
| account.getSavedMusicIds | stub | empty | TDesktop saved/profile music 预取；当前没有 profile music media store，返回空 id vector |
| account.updateStatus | done | persisted-presence | 记录 `auth_key/session/user` 维度运行时在线状态并持久化 `users.last_seen_at`；`offline=false` 与重连后 session 身份恢复都会写入 last_seen 并推送 `updateUserStatus(userStatusOnline expires=now+5m)`，`offline=true` 写入精确 `userStatusOffline.was_online`；最后一个 MTProto session 断开/destroy_session 也会写 last_seen 并推送 offline。状态推送给当前用户其它在线 session、在线联系人、以及已有私聊 dialog 的在线对端；session 恢复时还会向当前 session 补发在线联系人/私聊对端状态。users/dialog/history projection 已按 StatusTimestamp privacy 裁剪返回值；presence fanout 的精细 privacy 过滤仍为后续项 |

## Updates

| method | status | behavior | note |
|---|---|---|---|
| updates.getState | done | real | auth_key+user 维度持久化 update_states；账号级当前 pts 报告**最大连续已提交 pts**（MaxContiguousPts，非 allocator 最大已分配值），避免同设备换号/多账号串差分，也避免越过在途空洞 |
| updates.getDifference | done | real | 按 user_id 从 user_update_events 拉取 pts 后增量；只返回从客户端 pts 起**连续**的事件（遇在途空洞即截断），超 100 条置 differenceSlice；支持 new_message、read_history_inbox/read_history_outbox（私聊与 channel peer，channel read 映射 updateReadChannelInbox）、edit_message、message_reactions（输出带最新 `message.reactions` 的 affected message + `updateMessageReactions`，并按 viewer 重取最新聚合，避免 TDesktop 离线恢复时本地 message cache 不刷新）、read_message_contents、delete_messages、contacts_reset、dialog pinned/order/manual unread、peer_settings、dialog filters/folder peers 与 noop gap；消息事件携带 fwd/reply 所需 users/chats，payload 来自 durable log。若请求 date 后存在当前账号 active channel 的 channel durable events，会追加计算型 `updateChannelTooLong(channel_id,pts)` nudge，提示 TDesktop 再走 `updates.getChannelDifference`；该 nudge 不写 `user_update_events`，不消耗账号 pts。**账号级绝不返回 `differenceTooLong`**：已核对 TDesktop 基线 `api_updates.cpp:516`——对账号级 differenceTooLong 只打一行日志、不读 pts 且漏 `setRequesting(false)`，会永久锁死 update 引擎（`:689` 早退，重连/新 session 不可恢复）；落后客户端改用 `differenceSlice` 续传。因此 `user_update_events` **永久保留、不做 retention 裁剪**（对齐 参考实现），详见 docs/performance-audit.md 附录 C |
| updates.getChannelDifference | done | real-channel | 超级群/频道使用 channel 维度 durable log：`channel_update_events(channel_id, pts, pts_count, ...)`；按 channel pts 返回 `channelDifferenceEmpty/channelDifference/channelDifferenceTooLong`，limit cap=100；`pts < 0` 或 `pts > current_channel_pts` 返回 `PERSISTENT_TIMESTAMP_INVALID`；当前 member 的 `available_min_pts` 会抬高请求 pts，避免新加入/重新加入成员拉到入群前消息类 durable 事件；公共 username 频道允许非成员以只读预览身份拉可见差分并返回 synthetic read dialog，私有频道和禁看用户仍返回权限错误；当 `current_channel_pts-pts > cap` 时返回带当前 dialog pts 和最新有界消息快照的 `channelDifferenceTooLong`，避免大频道旧 pts 客户端循环拉大量页；普通 difference 优先使用事件 payload 中的 message 快照，连续编辑/删除后不会被当前消息状态污染；`updateChannelParticipant` 不进入 channel pts log，成员权限/封禁状态靠在线 `updateChannelParticipant/updateChannel` 与 `channels.getFullChannel/getParticipants/getParticipant` 刷新；本页消息的 sender/send_as/fwd_from/reply_to/action peers 会随 users/chats 返回，禁止复用 user_update_events |

### 兼容硬约束：pts / pts_count（跨所有产生 update 的 RPC）

> 已核对 TDesktop 基线源码（`data_pts_waiter.cpp` / `.h`、`api_updates.cpp`）。详细推导见 docs/message-module.md「客户端 pts 重排依赖 pts_count 准确」。

outbox 多 worker 并发 + 发送事务乱序提交 → **主动推送可能乱序到达客户端**（pts=6 先于 pts=5）。客户端 `PtsWaiter` 靠 `_count += pts_count` 累加判连续：乱序的先缓存、等空缺补齐再按序应用，1 秒补不齐才 `getDifference`。因此**任何产生 update 的 RPC 都必须遵守**：

1. **每条 update 的 `pts_count` 必须准确等于它推进的 pts 步数**。私聊文本、已读 inbox/outbox、文本编辑均为 1；批量删除为本次删除的 owner 视角 message_id 数量。
2. **每个分配出去的 pts 最终都要能被 getDifference 拿到**——事务回滚也要写 `noop` 占位（`recordPtsGaps`），否则连续水位永久卡死、客户端永久 gap。

当前私聊文本、转发、已读回执、文本编辑与删除消息满足两条（new_message/forward/read/edit pts_count=1，delete_messages pts_count=len(message_ids)，noop 补洞，连续水位兜底）。**新增任何其它 `pts_count ≠ 1` 的场景（批量 service action、频道 editMessage 等）时，必须回到此约束重新核对**，否则客户端 `_count` 永久错位。

## Dialogs / Messages

| method | status | behavior | note |
|---|---|---|---|
| messages.getDialogFilters | done | real | 返回 dialogFilterDefault + 当前账号持久化自定义 filters，并带 tags_enabled；离线设备启动可重拉完整分组状态 |
| messages.getSuggestedDialogFilters | stub | empty | 第一阶段不提供推荐文件夹 |
| messages.updateDialogFilter | done | real | 持久化/删除当前账号自定义 filter，校验 filter_id/title/peer 数量；channel peer 必须校验非零 access_hash；写 durable updateDialogFilter + dispatch_outbox |
| messages.updateDialogFiltersOrder | done | real | 持久化自定义 filter 顺序，去重并忽略默认/归档保留 ID，写 durable updateDialogFilterOrder + dispatch_outbox |
| messages.toggleDialogFilterTags | done | real | 持久化 folder tags 开关，写 durable updateDialogFilters reload + dispatch_outbox |
| folders.editPeerFolders | done | real | 支持 folder_id=0/1 的归档/还原；user peer 更新 dialogs.folder_id，channel peer 更新 channel_dialogs.folder_id；返回/记录 updateFolderPeers(pts,pts_count=1)，可靠投递其它 session |
| messages.getDialogs | done | real | 从按 user_id HASH 分区的 dialogs/message_boxes/users 查询当前账号私聊会话，并合并 channel_dialogs/channels/channel_messages；返回 users 统一走 viewer user projection（联系人名/手机号可见性 + 当前 `profile_photos` 头像），避免 TDesktop dialog 列表退回字母头像或手机号标题；支持 exclude_pinned、folder_id(0 主列表/1 归档/2+自定义 filter)、offset_date/offset_id/offset_peer seek pagination、limit、hash notModified；channel offset_peer 校验非零 access_hash；channel dialog 返回持久 `read_outbox_max_id`，可恢复离线/丢失的 `updateReadChannelOutbox`；不会把返回的 channel 宽泛标记为 active viewer，避免 typing/reaction 等瞬时事件误推给全体 dialog 列表；登录后可见 777000 官方系统会话 |
| messages.getPinnedDialogs | done | real | 从 dialogs + channel_dialogs 合并查询 pinned 会话，返回 top messages/users/channels，并附持久化 update state |
| messages.getPeerDialogs | done | real | 按 InputDialogPeer 精确查询当前账号会话，返回 dialog/top message/users/update state；channel peer 同样返回持久 `read_outbox_max_id`；公开 username channel/supergroup 对非成员返回只读 preview dialog/top message，private/ban/kick/view_messages 不暴露；未建会话的 user peer 返回空 dialog 占位；peer vector cap=100 |
| messages.getPeerSettings | done | real | 当前 owner 视角 peerSettings：非联系人显示 add/block；当前 owner 已保存对方但双方未 mutual 时显示 shareContact；acceptContact/add/import 形成 mutual 后清除 shareContact；若该 owner 已 hide peer settings bar 则返回空 action bar；附带当前 owner 视角用户资料 |
| messages.getMessages | done | real-private | TDesktop reply/webpage/media lazy load 入口；当前按 owner 视角 `InputMessageID` 精确拉取私聊 message_box，ID 不存在返回 messageEmpty，单次最多 100 个 |
| messages.getHistory | done | real | 从按 owner_user_id HASH 分区的 message_boxes 按当前账号+peer 查询历史；支持 offset_id、offset_date、add_offset、limit、max_id、min_id、hash，查询条件走分区索引与 seek pagination；`add_offset` clamp 到 `[-100,100]`，避免异常客户端触发超大跳过扫描或内存分配 |
| messages.readHistory | done | real-partial | 标记 reader dialog 的 read_inbox_max_id/unread_count 并清 manual unread；真正发生已读时给 reader 生成 updateReadHistoryInbox，同时推进 sender dialog 的 read_outbox_max_id 并生成 updateReadHistoryOutbox；发送消息时不预先推进 read_outbox，避免假已读 |
| messages.getOutboxReadDate | done | real-private | TDesktop 已读详情入口；按当前 owner+peer+msg_id 校验 outgoing message，读取最早覆盖该 msg_id 的 `read_history_outbox` durable event 日期，无读回执时返回 `MESSAGE_NOT_READ_YET` |
| messages.getMessageReadParticipants | done | real-channel-small | TDesktop 小群已读详情入口；仅 channel/megagroup peer，校验消息存在与可见历史，按 `channel_members.read_inbox_max_id >= msg_id AND read_inbox_date > 0` 返回最多 50 个 `readParticipantDate`，并用 `read_inbox_date` 给出已读时间；初始 join/invite read 水位只用于 unread 基线，不会把新成员伪造成旧消息读者；broadcast/超阈值/过期返回空 |
| messages.getMessageEditData | done | real-text-only | 参考实现/参考实现 的编辑前校验语义：按 peer/msg_id 校验消息存在与作者/管理员 edit_messages 权限；当前只支持文本消息编辑，没有媒体 caption 状态，因此返回 `messages.messageEditData{caption=false}` |
| messages.getWebPagePreview | stub | empty-preview | TDesktop 输入框链接预览入口；参考实现 空预览与 参考实现 空文本校验，`message` trim 后为空返回 `MESSAGE_EMPTY`，entities/text 有界，返回 `messages.webPagePreview{media=messageMediaEmpty}`，不抓取外网、不落缓存 |
| messages.uploadMedia / messages.sendMedia / messages.sendMultiMedia | done | photo/document/sticker | 接入 files/media 存储（documents/photos/file_blobs + 本地 blob backend）与消息 media 快照列；`resolveInputMedia` 解析 `inputMediaUploadedPhoto/Document`（组装上传分片→建 Photo/Document）与 `inputMediaPhoto/Document`（按服务端 document id 引用已存在资源，含贴纸）→ `domain.MessageMedia`，经 sendOutgoing 走与文本相同的 pts/box/outbox/在线推送/离线 difference；私聊与 channel 均支持；`sendMedia(inputMediaEmpty/WebPage)` 仍降级为纯文本 `sendMessage`；`uploadMedia` 返回可复用 `messageMedia`；sendMultiMedia 各条作为独立消息发送（grouped_id 相册聚合留 todo）；geo/contact/poll/todo/dice/story 等仍返回 `MEDIA_INVALID`；album cap=10，caption/entities/random_id/peer/access_hash 均校验 |
| messages.readMessageContents | done | real-content-read | TDesktop 普通消息内容已读入口；校验 id cap=100、message_id 范围与当前账号 exact 可见私聊消息；私聊 incoming media 写入 recipient box 时置 `media_unread`，对端 reaction 写入消息作者 box 时置 `reaction_unread` 并重算 dialog unread reaction 计数。事务内只清理实际 unread 的 message boxes；有变化才分配 user pts、写 durable `updateReadMessagesContents` + dispatch_outbox，其它在线 session 与离线 `updates.getDifference` 可恢复；重复调用/不可见 id/已读 id 返回当前 affectedMessages 且 `pts_count=0` |
| messages.getMessagesViews | done | real-channel-views | TDesktop 频道浏览计数入口；channel/supergroup peer 校验 access_hash 与 id cap=100，`increment=true` 时按 `(channel_id,message_id,viewer_user_id)` 去重后递增 `channel_messages.views_count`，按请求顺序返回显式 `views` 与 replies/comment context；不存在、删除或本地清历史前不可见的 id 返回空 `messageViews`；forwards 计数与 forwarded-channel source views 透传仍待媒体/转发统计模型补齐 |
| messages.getUnreadMentions | done | real-channel-mentions | Channel/supergroup peer 维护 `channel_unread_mentions(user_id,channel_id,message_id,media_unread)` 独立索引；sendMessage 解析 mention-name entity 与 `@username`，写入 active 且可见成员；history/getMessages/getChannelDifference/online update 按 viewer 回填 `message.mentioned/media_unread`，readMentions 后同一 viewer 不再带 flag；查询按 user+channel+top_msg_id+message_id seek，limit cap=100，返回 channel context |
| messages.readMentions | done | real-channel-mentions | Channel/supergroup peer 按 top_msg_id 有界清除 unread mention，单次最多 1000 条，重算 `channel_dialogs.unread_mentions_count`；返回 current channel pts/offset，供 TDesktop channel PtsWaiter 消费 |
| messages.reportSpam / messages.report | stub | reported-bounded | TDesktop peer bar/消息举报入口；校验 peer/access_hash/message id/option/comment 上限，不落 report 表；`report` 空 option 返回举报原因，`other` 返回 addComment，其余合法 option 返回 reported，非法 option 返回 `OPTION_INVALID` |
| messages.reportReaction / messages.reportMessagesDelivery / messages.reportReadMetrics / messages.reportMusicListen / messages.reportSponsoredMessage | stub | telemetry-noop | TDesktop 反应举报、Gateway 送达、阅读指标、音乐播放、广告举报入口；校验 peer/access_hash/id vector/metrics/document/duration/random_id 上限后返回 BoolTrue 或 sponsored reported，不落 telemetry/report 表 |
| messages.sendReaction / messages.getMessagesReactions / messages.getMessageReactionsList | done | real-private-channel-emoji | Private peer 支持 emoji reaction 持久化到 `private_message_reactions(private_message_id,user_id,reaction)`，双端 owner-visible message box 共享 reaction 聚合但按 viewer 重算 `my/chosen_order`，`sendReaction` 返回当前端 `updateMessageReactions`，向当前账号其它 session 与对端在线 session 推对应 peer/msg_id update，并写账号级 `message_reactions` durable event；离线设备经 `updates.getDifference` 补带 `message.reactions` 的 message + `updateMessageReactions`；history/getMessages/search 回填 `message.reactions`。Channel/supergroup peer 继续持久化到 `channel_message_reactions`，按当前用户替换/清除本消息 reaction，在线瞬时推送面向当前 active viewers 与反应者其它 session，history/getMessages/getMessagesReactions 可恢复当前聚合，list 按 `(reaction_date,reacted_user_id,reaction_value)` seek 分页，limit cap=100；custom emoji 普通消息 reaction 仍拒绝，paid reaction 走独立入口 |
| messages.setDefaultReaction | done | real-account-default | TDesktop quick/favorite reaction 保存入口；校验 emoji reaction 后持久化账号默认 reaction（默认 👍），供后续启动/设置路径复用 |
| messages.getPaidReactionPrivacy / messages.togglePaidReactionPrivacy / messages.sendPaidReaction | stub | privacy-real/send-balance-low | paid reaction privacy 持久化 default/anonymous/peer，并向当前账号其它 session 推 `updatePaidReactionPrivacy`；当前无 Stars 余额/paid reaction 计费模型，`sendPaidReaction` 校验 peer/msg_id/count/private 后返回 `BALANCE_TOO_LOW`，不伪造付费 reaction 聚合 |
| messages.deleteParticipantReaction / messages.deleteParticipantReactions | done | real-channel-moderation | 群/超级群管理员删除用户 reaction 入口；校验 peer/access_hash、participant user 与 delete_messages 权限，单条返回真实 `updateMessageReactions`，批量按最多 1000 条删除并给在线 active viewers 推受影响消息反应更新，同时刷新消息作者 unread reaction 计数；private peer 当前返回兼容成功/空 updates |
| messages.getUnreadReactions / messages.readReactions | done | real-channel-emoji-read | Channel/supergroup peer 按 `sender_user_id = 当前用户 AND unread` 维护 emoji reaction 未读状态；`sendReaction` 在反应者不是消息作者时写 unread 并重算 `channel_dialogs.unread_reactions_count`，查询按 user+channel+top_msg_id+message_id seek，limit cap=100，返回带 unread recent reaction 的 channel messages；`readReactions` 单次最多清 1000 条并重算 dialog count，返回 current channel pts/offset；TDesktop 打开 channel/supergroup history 发出的 `channels.readMessageContents` 会清理可见消息上的 unread reaction、回推 `updateMessageReactions` 并刷新 dialog reaction 计数；saved_peer/monoforum 与 reaction tag/ranking 仍留后续模型 |
| messages.getRecentReactions / messages.clearRecentReactions | done | real-account-recent | TDesktop reaction 面板后台入口；`messages.sendReaction(add_to_recent)` 会记录当前账号最近使用的私聊/channel/supergroup emoji reaction，limit cap=100，内容 hash 命中返回 notModified，clear 后返回 hash=0 空列表 |
| messages.getTopReactions | done | real-account-top | TDesktop reaction 面板后台入口；`messages.sendReaction` 会按当前账号累计私聊/channel/supergroup emoji reaction 使用次数，limit 0 时按 TDesktop 默认 14、cap=100，内容 hash 命中返回 notModified；账号 top 不足时优先用真实 `available_reactions` catalog（Files 服务缺失时才静态兜底）有界补齐，保证 top/recent/default 的 emoji id 能被 `messages.getAvailableReactions` 解析，避免右键 reaction selector 只有空白占位 |
| messages.getDefaultTagReactions | stub | empty/notModified | TDesktop saved-message tags 默认候选入口；当前无 saved reaction tag/custom-emoji document store，返回空 reactions/notModified；真实 tag ranking 与 custom emoji 候选后续补 |
| messages.getSavedReactionTags / messages.updateSavedReactionTag | done | real-account-title | TDesktop saved messages tag 入口；全局请求返回账号级 emoji saved reaction tag 标题，内容 hash 命中返回 notModified；update 校验 emoji reaction 与 title<=12 后持久化并向其它 session 推 `updateSavedReactionTags`；可选 peer 仍只校验 access_hash 并返回空，saved-message tag assignment/count、custom emoji 与默认 tag 候选留后续模型 |
| messages.getExtendedMedia | stub | empty-updates | TDesktop paid media 可见消息轮询入口；校验 peer/access_hash 与 id cap=100，当前无 paid media/extended media store，返回空 Updates，不伪造 `updateMessageExtendedMedia` |
| messages.sendVote / messages.getPollResults / messages.getPollVotes / messages.addPollAnswer / messages.deletePollAnswer / messages.getUnreadPollVotes / messages.readPollVotes | blocked | poll-store-missing | TDesktop poll UI 会直接调用这些入口；参考实现 都先由 peer+msg_id 找 poll media/poll_id，getPollVotes 有分页上限。当前 media/poll store 未接入，显式注册并校验 peer/access_hash/msg_id/options/offset/limit；read-only 返回空 updates/votes/unread 或 affectedHistory，真实 vote/add/delete 返回 `MESSAGE_ID_INVALID`，避免伪造 poll update；broadcast channel 拉投票人返回 `BROADCAST_FORBIDDEN` |
| messages.appendTodoList / messages.toggleTodoCompleted | blocked | todo-store-missing | Layer225 todo 媒体未接入；参考实现 的 todo item 上限配置与 append 对 `MessageMediaToDo` 的依赖，当前只做 peer/msg_id/list/id/title/entities 有界校验，空变更返回 `TODO_NOT_MODIFIED`，非空变更返回 `MESSAGE_ID_INVALID`，不生成不可恢复的 fake updates |
| messages.getSearchCounters / messages.search(media filters) | stub | zero-counters | TDesktop 搜索面板 counters 返回每个 filter 的 0 count；资料页 shared media count 会发 `messages.search(limit=0, filter=photo/video/document/url/gif/music/roundVoice/poll)`，当前无 media/webpage/poll/document store，显式返回空页/count=0，避免纯文本 channel history 污染 photos/videos/files 等计数；filter cap=32 |
| messages.getSearchResultsCalendar / messages.getSearchResultsPositions | stub | empty-bounded | TDesktop shared media 日历/位置辅助入口；校验 peer/access_hash/saved_peer/offset/limit 后返回空结果，calendar 回填请求 offset date/id，避免空月份重复拉取；真实媒体索引与按日聚合后续补 |
| messages.getReplies | done | real-channel-thread | 支持 channel/supergroup `reply_to_top_id` thread 分页；broadcast post 若已 linked discussion group，会映射到 linked megagroup root 后读取评论；forum topic root 支持 TDesktop `reply_to_msg_id=0/top_msg_id=topicRoot` 发送路径，返回 `messages.channelMessages{pts,topics}` 与 short topic；limit cap=100，add_offset clamp，按 message id seek，hash 命中返回 notModified，响应补齐 source/group chats/users |
| messages.getDiscussionMessage | done | linked-discussion-root | 校验 channel peer/access_hash/msg_id 后返回 thread root、`max_id/read_inbox/read_outbox/unread_count` 与 source/group chats；broadcast linked post 返回 discussion megagroup 中的 forwarded root message，未 linked 时返回当前 channel root/context；forum topic root 已由 `createForumTopic` 落 service message，topic 消息历史由 `messages.getReplies(topicRoot)` 补齐 |
| messages.readDiscussion | done | linked-read-state | 校验 root 后把 broadcast post 映射到 linked discussion megagroup，再推进该 target channel 的 read inbox 水位；不生成 channel pts，账号级 read update/outbox 用现有 channel read 机制补偿其它 session/作者回执 |
| messages.getForumTopics | done | forum-topic-page | 校验 channel peer/q/limit/offset，非 forum 返回 `CHANNEL_FORUM_MISSING`；forum 开启后返回虚拟 `General` topic(id=1) + 持久化 topic store，topic page 按 `(pinned,pinned_order,date,topic_id)` seek，不用 OFFSET；响应带 channel pts、channel context、root service messages 和 creator users |
| messages.getForumTopicsByID | done | forum-topic-lookup | topic id cap=100，非 forum 返回 `CHANNEL_FORUM_MISSING`；id=1 返回虚拟 `General`，其它 id 从 topic store 精确查询并补 root service message/context |
| messages.createForumTopic | done | root-service-message | 校验 peer/access_hash、forum megagroup、title/random_id/send_as；创建 `messageActionTopicCreate` channel service message，topic_id 使用 root message id，写入 `channel_forum_topics` 分区表，返回 `updateMessageID + updateNewChannelMessage` 并在线推送其它成员；重复 random_id 幂等返回既有 root |
| messages.editForumTopic | done | topic-edit-service | 校验 peer/access_hash、forum topic、title/icon/closed/hidden flags；topic creator 或具备 pin/manage 能力的成员可改；写 `messageActionTopicEdit` service message，reply_to_top_id 指向 topic root，更新 topic read model 并返回/推送 `updateNewChannelMessage` |
| messages.updatePinnedForumTopic / messages.reorderPinnedForumTopics | done | pinned-topic-updates | 校验 topic id/order cap=100 和 pin 权限；更新 `channel_forum_topics.pinned/pinned_order`，返回/在线推送 `updatePinnedForumTopic` 或 `updatePinnedForumTopics(order)`；该 TL update 本身不带 pts，离线重开通过 getForumTopics 补状态 |
| messages.deleteTopicHistory | done | bounded-topic-delete | 校验 forum topic 与权限；每次最多删除 `MaxDeleteHistoryBatch` 条 topic root/reply_to_top_id 消息，返回 `messages.affectedHistory.offset` 供 TDesktop 续删；最后一页才从 topic store 隐藏 topic，避免对十几万消息一次性生成 update 或 id vector |
| messages.getOnlines | done | bounded-online-members | 超级群/频道在线人数入口；校验 peer/access_hash 后用 `SessionManager.OnlineUserIDs(limit<=500)` 与 active channel member 做有界交集，caller 自动加入候选避免本连接快照遗漏；无在线 provider 时保留 参考实现 兼容 `onlines=1` |
| messages.editMessage | done | real-partial | 当前阶段支持私聊文本消息编辑：仅原发送者可编辑自己的 outgoing message，更新 shared private_messages 与所有可见 owner message_boxes，生成 updateEditMessage(pts_count=1) 并可靠投递；`inputMediaWebPage/inputMediaEmpty` 降级为文本编辑，真实 media 返回 `MEDIA_INVALID`，reply_markup 返回 `REPLY_MARKUP_INVALID`，quick replies 返回 `MESSAGE_ID_INVALID`，scheduled 返回 `SCHEDULE_DATE_INVALID` |
| messages.deleteMessages | done | real-partial | 当前 owner 视角软删除指定私聊消息；`revoke` 会删除同一 private_message 在其它 owner 视角的 message_box；按 owner 生成 `updateDeleteMessages`，`pts_count=len(message_ids)`，并重算或删除 dialog |
| messages.deleteHistory | done | real-partial | 当前 owner 视角按 peer/max_id 清空历史；默认清空后无可见消息则删除 dialog，后续新消息可重建；`just_clear` 保留空 dialog；`revoke` 同步清理对端 owner 视角；min_date/max_date 第一阶段兼容 no-op |
| messages.search | done | real | 当前账号消息搜索；user peer 走 `message_boxes`，channel/supergroup peer 走单份 `channel_messages`；支持 q/from_id/min_date/max_date/offset_id/add_offset/limit/max_id/min_id/hash，limit cap，文本命中有 pg_trgm GIN 索引兜底；channel 结果附带 sender/fwd/reply 所需 users/chats |
| messages.searchGlobal | done | real-partial | TDesktop 搜索框全局消息分支；当前查当前 owner 私聊 message_boxes，并合并当前账号 active membership 的频道/超级群单份文本消息；支持 users_only/groups_only/broadcasts_only、filterEmpty、q、limit cap=50、offset_rate+offset_peer+offset_id channel seek、min/max_date 与 folder_id=0/1 下推，media filters 显式空结果；参考实现 的 joined channel list 语义与 参考实现 的 bounded search，后续再接外部全文搜索/媒体索引 |
| messages.toggleDialogPin | done | real | 当前 owner 的 dialog pinned/pinned_order 持久化；user peer 写 dialogs，channel peer 写 channel_dialogs；写 durable updateDialogPinned + dispatch_outbox，可靠投递给其它在线 session |
| messages.reorderPinnedDialogs | done | real | 当前 owner 的 pinned dialog 顺序持久化；支持混合 user/channel peer 与 force 清理未在 order 中的 pinned dialog，写 durable updatePinnedDialogs(order) + dispatch_outbox，可靠投递给其它在线 session |
| messages.markDialogUnread | done | real | 当前 owner manual unread mark 持久化；user peer 写 dialogs，channel peer 写 channel_dialogs/channel_members，`messages.getDialogs/getPeerDialogs` 返回 dialog.unread_mark；readHistory 清除 unread_mark，写 durable updateDialogUnreadMark + dispatch_outbox |
| messages.getDialogUnreadMarks | done | real+monoforum-stub | 返回当前 owner 所有 user/channel manual unread peer；`parent_peer` 分支校验 parent channel 后返回空 monoforum marks，`messages.markDialogUnread(parent_peer)` 同样校验 parent/sublist peer 后 BoolTrue no-op，避免 TDesktop SavedSublist/monoforum 后台触发 `NOT_IMPLEMENTED`；真实 monoforum topic unread store 后续补 |
| messages.hidePeerSettingsBar | done | real | 当前 owner 隐藏 peer action bar 状态持久化；后续 getPeerSettings 返回空 action bar，写 durable updatePeerSettings(settings) + dispatch_outbox，并可靠投递给其它在线 session |
| messages.setTyping | done | real-transient | 私聊 typing/cancel/upload 等 SendMessageAction 实时包装为 updateShort(updateUserTyping) 推给对端在线 session，并排除当前 auth_key_id+session_id；InputPeerChannel 走 `updateChannelUserTyping` 在线瞬时推送；`top_msg_id` 有界校验，非法返回 `MSG_ID_INVALID`；typing 不写 durable log、不做离线补偿 |
| messages.saveDraft | done | real-partial | 接受 TDesktop 输入框云草稿保存/清空 RPC，channel peer 校验非零 access_hash，持久化 user/channel peer 文本、entities、reply、forum `top_msg_id`、no_webpage/invert_media/effect 与 webpage URL，并按 参考实现 语义向同账号其它在线 session 推 `updateDraftMessage`；发送消息 `clear_draft` 会清对应 draft 并推空 draft update；文件 media、suggested_post、monoforum saved_peer 与 getDifference draft event 后续补 |
| messages.getAllDrafts | done | real-partial | 从 `dialog_drafts` 读取最多 1000 条云草稿，返回对应 `updateDraftMessage`，forum draft 设置 `top_msg_id`，附带已知 users/chats；文件 media、suggested_post、monoforum saved_peer 暂不返回 |
| messages.clearAllDrafts | done | real-partial | 有界清空最多 1000 条当前账号云草稿，向其它在线 session 推 `draftMessageEmpty` updates；超大 draft 集合按后续调用继续清理，durable difference 事件后续补 |
| messages.getSavedDialogs | stub | empty/notModified | 第一阶段不做 saved messages 分会话 |
| messages.getPinnedSavedDialogs | stub | empty | 第一阶段不做 saved messages 分会话置顶 |
| messages.toggleSavedDialogPin | stub | ok | 第一阶段不做 saved dialogs 置顶，显式接受避免未知 RPC |
| messages.reorderPinnedSavedDialogs | stub | ok | 第一阶段不做 saved dialogs 置顶排序 |
| messages.getSavedDialogsByID | stub | empty | 第一阶段不做 saved dialogs 指定查询 |
| messages.getSavedHistory | stub | empty/notModified-bounded | TDesktop 打开群/用户资料页时会后台查询 saved history/monoforum 入口；当前不做 saved history，但会校验可选 `parent_peer` 必须是可见 channel、peer/access_hash、offset/add_offset/limit/id/date 边界，返回空 messages 或 hash notModified，并给 parent/peer channel 补 chat context |
| messages.readSavedHistory | stub | ok-bounded | 当前不维护 saved history read state；按 monoforum 语义要求 `parent_peer` 是可见 channel，校验 peer/access_hash 与 max_id 后返回 BoolTrue |
| messages.deleteSavedHistory | stub | affected-empty-bounded | 当前不维护 saved history；校验可选 `parent_peer`、peer/access_hash、max_id/min_date/max_date 后返回 `affectedHistory{pts=current,pts_count=0,offset=0}` |
| messages.getCommonChats | done | common-megagroups | TDesktop 用户资料页共同群/机器人共同群入口；校验 input user/access_hash、max_id、limit<=100，只返回双方均 active 的 megagroup/supergroup，排除 broadcast/left/kicked/deleted，按 channel id seek 分页；PG 走按 `user_id` 主键的 `user_channel_member_index`，避免 `users.getFullUser.common_chats_count` 在私聊打开路径反向规划 `channel_members` 64 分区；`users.getFullUser` 同步填 `common_chats_count` |
| messages.getScheduledHistory / messages.getScheduledMessages / messages.sendScheduledMessages / messages.deleteScheduledMessages | stub | scheduled-store-missing | 第一阶段不做定时消息；history/exact get 返回空 messages 并校验 peer/id cap=100，send-now 对非空 id 返回 `MESSAGE_ID_INVALID`，delete 返回 `updateDeleteScheduledMessages` 清理调用端本地幽灵项；真实 scheduled store、has_scheduled dialog 标记与 job sender 后续补 |
| messages.getAvailableReactions | done | real-resources | 从 `available_reactions` 表（由 `TELESRV_STICKER_SEED_DIR` 真实导出 seed）加载 74 个 reaction，static_icon/appear/select/activate/effect/around/center 均返回真实 `document`（seed source id 已在导入阶段归一为服务端 document id，access_hash/file_reference/dc_id/attributes/thumbs 重写为本 DC 数据），TDesktop 经 `upload.getFile` 从本地 blob backend 下载动画；启动 seed 会校验所有 reaction document 的 `doc:<id>` 主 blob，自动修复半导入状态，且导入扫描只把 `_thumb\d+_` 识别为缩略图，避免 `reaction_thumbs_up/down...` 主文件被误跳过；hash 命中返回 notModified；Files 服务缺失或未 seed 时回退固定 emoji + documentEmpty stub |
| messages.getAvailableEffects | stub | empty | TDesktop message effects 后台预取；参考实现 空响应与 参考实现 无数据响应，返回 `messages.availableEffects{hash=0,effects=[],documents=[]}` |
| messages.getAllStickers / messages.getEmojiStickers | done | real-sets | 列出已 seed 的贴纸集（`getAllStickers`→stickers 类，`getEmojiStickers`→emoji 类）元数据（cover/thumb），稳定 hash 命中返回 notModified；TDesktop 再按集调 `getStickerSet` 拉文档；无 seed 时回退空目录 |
| messages.getRecentStickers | stub | empty/notModified | TDesktop 输入框/emoji 面板预取最近贴纸；第一阶段返回空列表或 hash notModified |
| messages.getFavedStickers | stub | empty/notModified | TDesktop 输入框/emoji 面板预取收藏贴纸；第一阶段返回空列表或 hash notModified |
| messages.getStickers | stub | notModified | 第一阶段不提供贴纸搜索结果 |
| messages.getStickerSet | done | real-sets | `stickerSetRefFromInput` 把 `inputStickerSetID/ShortName/AnimatedEmoji/Dice/EmojiGenericAnimations` 解析为 `domain.StickerSetRef`，从 `sticker_sets` 加载集元数据 + 按 `document_ids` 顺序加载真实文档（含 packs；document id / pack document id / thumb_document_id 均为 seed 阶段归一后的服务端 id），返回完整 `messages.stickerSet`；hash 命中返回 notModified；未 seed 的系统集/未知短名回退空集 stub 避免破坏客户端 |
| messages.getFeaturedStickers | stub | empty/notModified | TDesktop 输入框/emoji 面板预取推荐贴纸；第一阶段返回空列表或 hash notModified |
| messages.getEmojiGroups | stub | notModified | 第一阶段不提供 emoji 分组目录 |
| messages.getEmojiStickerGroups | stub | notModified | 第一阶段不提供 emoji sticker 分组目录 |
| messages.getEmojiProfilePhotoGroups | stub | empty | TDesktop 头像/群头像编辑页 custom-emoji profile photo categories 入口；当前无 custom-emoji profile photo 分组，返回空 `messages.emojiGroups`，避免后台 `NOT_IMPLEMENTED` 重试 |
| messages.getEmojiStickers | stub | empty/notModified | TDesktop 输入框/emoji 面板预取 emoji stickers；第一阶段返回空目录或 hash notModified |
| messages.getFeaturedEmojiStickers | stub | empty/notModified | TDesktop 输入框/emoji 面板预取 featured emoji stickers；第一阶段返回空列表或 hash notModified |
| messages.getEmojiKeywordsLanguages | stub | empty | TDesktop 输入框打开后拉 emoji keyword 语言，第一阶段返回空列表 |
| messages.getEmojiKeywords / messages.getEmojiKeywordsDifference | stub | empty-difference | TDesktop emoji keyword 增量入口；校验 lang_code 和 from_version，返回空 `emojiKeywordsDifference`，difference 版本回显客户端 from_version，避免重复大包拉取 |
| messages.getCustomEmojiDocuments | done | real-documents | 按服务端 document id 批量从 `documents` 表加载真实文档（单批 cap=100、id 必须为正），命中返回真实 `document`，未命中返回 `documentEmpty{id}` 占位；无 Files 服务时返回空 |
| messages.getAttachedStickers | stub | empty | TDesktop 图片/视频“查看相关贴纸包”入口；nil media 返回 `MEDIA_EMPTY`，有效 media 返回空 sticker set list |
| messages.searchStickerSets / messages.searchStickers | stub | empty-bounded | TDesktop 贴纸/emoji 云搜索入口；query/emoticon/lang/offset/limit/hash 有界，hash 非零返回 notModified，当前无云贴纸索引，返回空 found results |
| messages.getSavedGifs | stub | empty/notModified | TDesktop 输入框/emoji 面板预取 saved GIFs；第一阶段返回空列表或 hash notModified |
| messages.getAttachMenuBots | stub | notModified | 第一阶段不提供 attach menu bot |
| messages.getQuickReplies | stub | notModified | 第一阶段不做 business quick replies |
| messages.getWebPage | stub | empty | Settings/消息预览路径可能预取 instant view；第一阶段返回 webPageEmpty |
| messages.getDefaultHistoryTTL | stub | disabled | 参考实现，返回 `defaultHistoryTTL{period=0}`，表示新聊天默认不自毁 |
| messages.getSponsoredMessages | stub | empty | 参考实现，返回 `messages.sponsoredMessagesEmpty`，避免群/频道页面后台广告拉取重试 |
| messages.sendMessage | done | real-private-text | 私聊文本消息；支持 random_id 幂等、silent/noforwards、reply_to 私聊消息头（含 quote 文本/entities/offset 与双端 message_id 翻译；quote_text≤1024，quote_offset 按消息文本 offset 限制在 4096 内）、双端 message box、dialog、pts/update event、transactional outbox、在线 session 批量推送、4096 字符上限与 Redis 用户级窗口限流；outbox 排除当前设备使用 auth_key_id+session_id；pts_count 恒 1；channel peer 的 `send_as` 支持 self/current channel 校验链路；media/scheduled 仍返回 `MEDIA_INVALID`/`SCHEDULE_DATE_INVALID`；reply_markup/quick_reply/effect/paid/suggested 与 story/monoforum/todo/poll reply 返回显式 TL 错误，避免 TDesktop 高级输入路径产生 `NOT_IMPLEMENTED` |
| messages.forwardMessages | done | real-private-text | 支持当前 owner 可见文本消息在 user/channel peer 间转发，保留原 forward header 或生成 messageFwdHeader，支持目标会话 `reply_to`、TDesktop forum topic `top_msg_id`、silent/noforwards/drop_author、random_id 幂等和 100 条上限；目标 channel 的 `send_as` 支持 self/current channel 校验链路；源消息自身 reply 不继承，响应、difference 与 outbox 都补齐 fwd/reply/send_as 来源 users/chats；被 noforwards 保护的源消息返回 `CHAT_FORWARDS_RESTRICTED`；scheduled/monoforum/quick_reply/effect/video_timestamp/paid/suggested 返回显式 TL 错误，不再落 `NOT_IMPLEMENTED` |
| messages.createChat | done | real-as-megagroup | 不落 legacy chat 持久化；按 docs/channel-module.md 直接创建 `megagroup` channel。TDesktop New Group 回调只接受 `updates.chats[0] == chat`，因此 TDesktop ctx 下同步响应返回 `legacy chat(migrated_to=channel)` + 真实 `channel`；后续 `InputPeerChat` 主路径映射回同 id channel，避免 `ContactsBox::creationDone` 的 `chat not found in updates` |
| messages.getChats | done | legacy-as-channel | 无 legacy chat 长期形态；若 TDesktop/export 路径用 chat_id 询问，按同 id 查询 megagroup channel 并返回 `tg.Channel` |
| messages.getFullChat | done | legacy-as-channel | 无 legacy chat 长期形态；将 chat_id 映射为 channel_id，返回 `channels.getFullChannel` 语义 |
| messages.addChatUser | done | legacy-as-channel | legacy 普通群入口映射到 `channels.inviteToChannel`；仍执行 channel invite 权限和单次 invite cap |
| messages.deleteChatUser | done | legacy-as-channel | legacy 普通群入口映射到 self leave 或 channel kick；仍执行 ban/kick 权限 |
| messages.editChatTitle | done | legacy-as-channel | legacy 普通群标题入口映射到 `channels.editTitle`，生成 megagroup service message 与 channel pts |
| messages.editChatPhoto | done | legacy-as-channel | legacy 普通群头像入口映射到 `channels.editPhoto`：上传/引用照片落 `channels.photo_*` 反范式列并返回 `updateChannel` + 推 channel state；`inputChatPhotoEmpty` 清除头像 |
| messages.editChatAdmin | done | legacy-as-channel | legacy 普通群管理员入口映射到 `channels.editAdmin`；`is_admin=true` 授予 basic group 管理权，`false` 清空 admin rights |
| messages.editChatAbout | done | legacy-as-channel | legacy/basic wrapper 支持 `InputPeerChat` 与 `InputPeerChannel`，真实持久化 `channels.about` 并校验 change_info 权限 |
| messages.editChatDefaultBannedRights | done | real-channel-permissions | 按 参考实现 语义要求 ban_users 权限；持久化 `channels.default_banned_rights`，TDesktop full channel 可见；普通成员发送/邀请会实时受 default rights 与个人 banned rights 限制 |
| messages.editChatParticipantRank | done | legacy-as-channel | legacy 管理员 rank 入口映射到 `channels.editAdmin`，保留当前 admin rights，仅更新 rank，rank 长度有界 |
| messages.editChatCreator | blocked | needs-2fa-transfer | 显式注册并校验 peer/user；当前未实现账号 2FA/SRP 与所有权转移事务，返回 `PASSWORD_HASH_INVALID` 而非 fallback |
| messages.setChatTheme | stub | channel-context/private-ack | TDesktop private/theme 入口；user peer 返回空 updates，legacy chat/channel peer 校验可见性并返回 channel state，避免设置页后台 unknown RPC |
| messages.toggleNoForwards | done | real-channel | legacy chat/channel content protection 入口映射到 `channels.noforwards`，校验 change_info 权限，返回/推送 `updateChannel`；后续 channel message 自动继承 noforwards |
| messages.setChatAvailableReactions | done | real-channel-policy | legacy chat/channel reaction 设置入口；参考实现 持久化 channel reaction policy，校验 change_info、reaction vector/reactions_limit cap=64，`channels.getFullChannel` 返回 `available_reactions/reactions_limit/paid_reactions_available`，返回/推送 channel state |
| messages.saveDefaultSendAs | done | real-current-channel | TDesktop send-as 选择器保存入口；当前 `channels.getSendAs` 返回 self，并在 creator、broadcast post admin、megagroup anonymous admin 可用时追加 current channel；保存入口同样只接受 self/current channel，拒绝其它 `send_as`；默认身份写入当前 user 的 `channel_dialogs.default_send_as_peer_*`，保存 self 会清空默认；`channels.getFullChannel` 只在默认身份仍有效时输出 `channelFull.default_send_as`；`sendMessage/forwardMessages(InputPeerChannel)` 未显式带 `send_as` 时会读取并重新校验该默认身份，失效默认值自动降级 self |
| messages.migrateChat | done | legacy-as-channel | 服务端无 legacy chat 长期形态；校验 change_info/creator 权限后把 chat_id 视作既有 megagroup channel_id，返回 `updateChannel + tg.Channel`，不创建迁移双写路径 |
| messages.sendMessage(InputPeerChannel) | done | real-channel-text | channel/supergroup 单份消息写 `channel_messages`，分配 channel message id + channel pts，生成 `updateNewChannelMessage`；支持同 channel 可见消息 reply_to，服务端反算 `reply_to_top_id` 并保留 quote metadata；支持 `send_as` self/current channel，未显式带 `send_as` 时读取已保存默认身份并重新校验，消息 `from_id` 按 send_as 输出并补齐 peers；`random_id` 幂等重试返回原始 send snapshot，不受后续 edit/delete 污染；broadcast 校验 post 权限；发送事务只对小超级群同步刷新 `channel_dialogs` 普通未读缓存，broadcast/超阈值 megagroup 不做全员 unread 写入，`messages.getDialogs/getPeerDialogs/channels.getFullChannel` 按 `channel_members.read_inbox_max_id + available_min_id + channels.top_message_id + channel_messages.deleted` 读时派生普通未读；发送者 `channel_members.read_inbox` 推到新消息，自己消息不计入动态未读；持久 channel updates 在线 fanout 走 updates-ready session 的 channel membership 索引，按当前在线 active 成员 best-effort 推送（不再受 500 cap 截断），离线靠 `updates.getChannelDifference` 补偿 |
| messages.getHistory(InputPeerChannel) | done | real-channel | 查 `channel_messages(channel_id,id)`，按 seek pagination 返回；PG 使用 `limit+1` 探测下一页，不做 `count(*) over()` / SQL 大 OFFSET；带 username 的公开 channel/supergroup 允许非成员只读预览历史，私有频道与 ban/kick/view_messages 禁止仍返回对应错误 |
| messages.readHistory(InputPeerChannel) | done | real-channel | 推进当前 user 的 `channel_dialogs.read_inbox_max_id`，未读惰性重算；bounded 扫描最近 read delta，推进相关发送者 `read_outbox_max_id` 并在线推 `updateReadChannelOutbox`，不对全体成员做 O(n) fanout |
| messages.editMessage(InputPeerChannel) | done | real-channel-text | 作者或具备 edit_messages 权限的管理员可编辑单份 `channel_messages` 文本，生成 `updateEditChannelMessage(pts_count=1)` 并进入 `channel_update_events`；网页预览 media 降级文本编辑，真实 media/reply_markup/quick replies/scheduled 按现有 messages.editMessage 显式错误拒绝 |
| messages.deleteHistory(InputPeerChannel) / channels.deleteHistory(local) | done | real-channel-clear | 当前 user 本地清空时只推进 `available_min_id/read_inbox` 和 `channel_dialogs`，不生成 channel pts；同时写账号级 `channel_available_messages` durable update，并在线推 `updateChannelAvailableMessages` 给同账号其它 session，TDesktop 会 `clearUpTill(available_min_id)`；返回/推送实际应用后的单调 `available_min_id=max(old, requested)`，避免多设备 stale clear 让客户端水位回退；`messages.deleteHistory(InputPeerChannel,revoke)` 映射为有权限时的 channel 维度批量删除，返回 `affectedHistory.offset` 供后续 RPC 续删，单批 cap=1000 |
| messages.forwardMessages(InputPeerChannel) | done | real-channel-text | 支持 channel→channel、channel→user、user→channel 的文本转发，保留 forward header 或按 `drop_author` 隐藏；支持目标会话 `reply_to` 与 forum topic `top_msg_id`，后者映射为 topic-only reply 并由 store 校验 topic/权限/可见性，但不继承源消息自身 reply；目的 channel 仍单份写入并生成 channel pts；目标 channel 支持 `send_as` self/current channel，未显式带 `send_as` 时读取已保存默认身份并重新校验，响应/difference/outbox/channelDifference 补齐可解析的 send_as/fwd_from/reply_to user/channel 上下文，单次 cap=100 |
| messages.setTyping(InputPeerChannel) | done | real-channel-transient | megagroup/topic typing 走 `updateChannelUserTyping` 在线瞬时推送；`top_msg_id` 只作为 forum/thread 维度透传，范围非法返回 `MSG_ID_INVALID` 且不广播；不写 durable log、不做离线补偿；仅推给已通过 channel-specific RPC（如 getHistory/getFullChannel/getChannelDifference/getPeerDialogs）标记的 active viewers，不向全体在线成员扩散 |

## Channels / Supergroups

| method | status | behavior | note |
|---|---|---|---|
| channels.createChannel | done | real | 创建 broadcast channel 或 megagroup；写 channel、creator member、service message、channel_update_events；响应包含 `tg.Channel`；history import/geogroup 暂无业务模型，显式返回 `CHAT_INVALID`/`ADDRESS_INVALID`，负 `ttl_period` 返回 `TTL_PERIOD_INVALID`，避免阶段外 flags 继续落 `NOT_IMPLEMENTED` |
| channels.getChannels | done | real | 按 inputChannel 返回当前用户可见的 `tg.Channel` 列表；精确 ID vector cap=100；非零 access_hash 不匹配时跳过该项，避免异常客户端触发无界逐项查询或绕过最小 channel hash 校验 |
| channels.getFullChannel | done | real-minimal | 返回 `ChannelFull` 最小必需字段：about、participants/read/unread/notify/pts/admin rights/default banned rights；非零 access_hash 不匹配返回 `CHANNEL_PRIVATE`；带 username 的公开 channel/supergroup 允许非成员只读预览 full/history，并以 left/self 无 unread 视图返回，避免 TDesktop 公开搜索结果打开后空白；`read_outbox_max_id` 来自当前 viewer 的 channel dialog/member 状态；有邀请管理权限的管理员会收到 `requests_pending/recent_requesters`，供 TDesktop join request 管理入口恢复 |
| channels.getSendAs | done | real-current-channel | TDesktop 群/频道输入框预取 send-as peers；参考实现/参考实现 语义，当前返回 self，并在 creator、broadcast post admin、megagroup anonymous admin 可用时追加 current channel，同时返回可解析的 current channel chat + self user；公开非成员预览返回 self-only，不因非成员报 `CHANNEL_PRIVATE`；真实 public channel send_as 候选留后续 |
| channels.checkUsername | done | real | 校验 channel 可见性与 username 规则，大小写不敏感检查 users/channel_usernames 占用；同一 channel 当前 username 视为可用 |
| channels.updateUsername | done | real | creator 可设置/清除频道或超级群主 username；PG 事务内更新 `channels.username` 与 `channel_usernames`，大小写不敏感唯一约束兜底，成功后向在线成员推 `updateChannel` |
| channels.getAdminedPublicChannels | done | real | 返回当前用户 active creator/admin 且带主 username 的公开 channel/supergroup，用于 TDesktop public username limit/选择入口 |
| channels.toggleSignatures | done | real-minimal | creator/change_info admin 可切换 `channels.signatures` 并返回/推送 `updateChannel`；profiles_enabled 先不单独持久化 |
| channels.togglePreHistoryHidden | done | real-minimal | creator 可切换 `channels.pre_history_hidden`，返回/在线推 `updateChannel`，`getFullChannel` 暴露 `hidden_prehistory`；新加入/导入/受邀成员按当前 `top_message_id` 初始化 `available_min_id/read_inbox`，并按加入前 channel pts 初始化 `available_min_pts`，避免看到旧历史或补到入群前消息类 durable 事件 |
| channels.toggleSlowMode | done | real-minimal | change_info 管理员可持久化 `channels.slowmode_seconds`，`Channel.slowmode_enabled/ChannelFull.slowmode_seconds` 可见；普通成员发送按 `channel_members.slowmode_last_send_date` 返回 `SLOWMODE_WAIT_X`，管理员/creator 豁免 |
| channels.setStickers | stub | empty-only | 当前无群贴纸集 store；仅 megagroup + `inputStickerSetEmpty` 清空入口做权限校验 no-op 成功，非空 sticker set 返回 `STICKERSET_INVALID`，避免 TDesktop 误以为贴纸集已落库 |
| channels.reorderUsernames | stub | permission-ok | 当前无 Fragment/多 username 模型，限制 order<=32、校验 change_info 后返回 BoolTrue |
| channels.toggleUsername | stub | permission-ok | Fragment/额外 username 入口；当前不改主 username，只校验 username 格式与 change_info 权限后返回 BoolTrue，避免误删 `channels.updateUsername` 的主 username |
| channels.deactivateAllUsernames | stub | permission-ok | Fragment/额外 username 入口；当前无 purchased username 模型，仅校验 change_info 后返回 BoolTrue |
| channels.updateColor | done | real-appearance | 持久化 `channels.color/profile_color` 与 background emoji id，保留 color flag 显式 0；校验 access_hash + change_info 后返回/推送带 `Channel.color/profile_color` 的 `updateChannel`；boost level 暂不强制，避免本地测试频道外观入口失效 |
| channels.updateEmojiStatus | done | real-minimal | 支持 `emojiStatusEmpty` 清除与普通 `emojiStatus(document_id,until)` 持久化并输出到 `Channel.emoji_status`；collectible emoji status 缺少 gift/read model 时返回 `EMOJI_STATUS_INVALID`，不伪造 collectible 元数据 |
| channels.exportMessageLink | done | real-message-link | 校验 channel/access_hash、msg_id 范围和当前成员可见的单份 channel message；公开 channel 返回 `t.me/{username}/{msg_id}`，私有 channel 返回 `t.me/c/{channel_id}/{msg_id}`，`thread=true` 且消息有 reply root 时追加 `?thread={root_id}`；`grouped/html` 与 public discussion `comment=` 精细链接后续补 |
| channels.readMessageContents | done | real-channel-content-read | 校验 channel/access_hash、id vector cap=100 与可见 exact message；按当前作者视角清理 visible messages 的 unread reaction、重算 `channel_dialogs.unread_reactions_count`，并向当前账号其它 session 推 `updateChannelReadMessagesContents` / `updateMessageReactions` 刷新 TDesktop 角标；不生成 channel pts |
| channels.reportSpam | stub | ok | 校验 channel、participant 与 id cap=100 后返回 BoolTrue；风控/举报队列后续补 |
| channels.getLeftChannels | done | real-left-export | TDesktop takeout/export 路径；按当前 user 的 left channel/supergroup membership 返回有界 pageSize=100，offset<=10000，带 full count 与 left channel flag；最终非空页返回 `messages.chats`，越界空页返回空 `messages.chatsSlice` 让导出流程结束 |
| channels.getInactiveChannels | done | real-least-active | Premium limits 路径；按当前用户 active 频道/超级群的可见 top message date 旧到新返回，dates 与 chats 对齐，limit cap=100；不实现 Premium 限额策略 |
| channels.getGroupsForDiscussion | done | real-owned-megagroups | 讨论组选择入口；返回当前用户可管理的 megagroup/supergroup（无 legacy basic group），按 ID 倒序限流，TDesktop 可直接选择 |
| channels.setDiscussionGroup | done | real-link | 校验 access_hash、broadcast/group 类型、管理权限与 hidden prehistory；双向维护 `linked_chat_id`，清理旧链接，返回 BoolTrue 并推送相关 `updateChannel` |
| channels.editLocation | stub | permission-ok | geogroup 未接入；校验 change_info 后返回 BoolTrue |
| channels.convertToGigagroup | stub | updateChannel | 当前 createChat 已直接创建 megagroup；校验 change_info 后返回 `updateChannel` |
| channels.deleteParticipantHistory | done | bounded-delete | 管理员按 `participant` 的 `sender_user_id` 分批软删 channel_messages，单批 cap=1000，返回 `affectedHistory(offset=1 表示仍需续删)` 并生成 `updateDeleteChannelMessages`，`pts_count=len(ids)` |
| channels.toggleJoinToSend | done | real-join-settings | 持久化 `join_to_send`，校验 megagroup + invite/admin 权限，返回/推送带 flags.28 的 `updateChannel` |
| channels.toggleJoinRequest | done | real-join-settings | 持久化 `join_request`，仅 public megagroup 可开启；`channels.joinChannel` 对非成员写入 pending public join request 并返回 `INVITE_REQUEST_SENT`，并向管理员推有界 `updatePendingJoinRequests`；approve/dismiss 复用 `messages.hideChatJoinRequest` 有界路径 |
| channels.toggleForum | done | real-setting | 持久化 megagroup `forum/forum_tabs`，仅 creator 可改；已绑定 discussion group 时返回 `CHAT_DISCUSSION_UNALLOWED`；返回/推送 `updateChannel` 并写 `channelAdminLogEventActionToggleForum`；`messages.getForumTopics*` 与 create/edit/pin/reorder/deleteTopicHistory 已支持虚拟 General + 持久化 topic store |
| channels.toggleAntiSpam | done | real-setting | megagroup 管理员可持久化 `channels.antispam`，返回/推送 `updateChannel`；`ChannelFull.antispam` 恢复 TDesktop 开关状态，并写 `channelAdminLogEventActionToggleAntiSpam`；真实反垃圾删除管线未接入 |
| channels.reportAntiSpamFalsePositive | stub | ok | 校验 change_info 与 msg_id 后返回 BoolTrue；真实 antispam admin-log 后续补 |
| channels.toggleParticipantsHidden | done | real-hidden-members | 持久化 `channels.participants_hidden`；creator/具备 ban_users 的管理员可切换，返回/推送 `updateChannel`；`ChannelFull.participants_hidden` 恢复 UI 状态，非管理员成员 `getParticipants(recent/search/bots/contacts/mentions)` 只返回 aggregate count 不暴露列表，`messages.getMessageReadParticipants` 返回空避免隐藏成员场景泄漏已读名单 |
| channels.toggleViewForumAsMessages | done | real-local-dialog | 持久化当前账号 `channel_dialogs.view_forum_as_messages`；返回/可靠投递 `updateChannelViewForumAsMessages` 同步同账号其它 session，并在 `messages.getDialogs` 的 `Dialog.view_forum_as_messages` 与 `channels.getFullChannel` 的 `ChannelFull.view_forum_as_messages` 回填；forum topic create/read/edit/pin/reorder/delete 已接入 |
| channels.getChannelRecommendations | done | real-public-broadcasts | TDesktop similar/recommended 入口；返回公开 username broadcast channel，指定 source 时校验 access_hash 且排除 source，无 source 时排除当前用户 active membership；默认 10、cap=100，真实相似度/订阅画像/Premium 扩容后续补 |
| channels.setBoostsToUnblockRestrictions | stub | permission-update | boost bypass 未接入；按 Layer225 限制 boosts 为 0..8（0 关闭）并校验 change_info 后返回/推送 `updateChannel` |
| channels.setEmojiStickers | stub | empty-only | 当前无 custom emoji sticker store/boost gating；仅 megagroup + `inputStickerSetEmpty` 清空入口做权限校验 no-op 成功，非空 sticker set 返回 `STICKERSET_INVALID` |
| channels.restrictSponsoredMessages | done | real-setting | 持久化 `channels.restricted_sponsored`，校验 change_info 后返回/推送 `updateChannel`；`channels.getFullChannel` 回填 `ChannelFull.restricted_sponsored`，真实广告投放/boost 等级校验后续补 |
| channels.searchPosts | done | real-public-post-search | 全局公开频道/超级群帖子搜索；按 Layer225 校验 query/hashtag 二选一、文本长度<=256、limit cap=50、offset_id 范围、offset_peer 可为空或公开/可见 channel peer、paid stars 非负；只搜索 username 非空且未删除的公开 channel_messages，PG 走 body trigram `ILIKE` + `(message_date,channel_id,id)` seek 分页，满页返回 `messages.messagesSlice(next_rate,search_flood)` |
| channels.updatePaidMessagesPrice | done | real-setting | 持久化 `channels.send_paid_messages_stars` 与 broadcast `channels.broadcast_messages_allowed`；按 TDesktop app config 默认限制 stars<=10000，supergroup 负数返回 `STARS_AMOUNT_INVALID`，broadcast direct messages 允许 `-1` 表示关闭；返回/推送 `updateChannel` 并在 `Channel`/`ChannelFull` 回填状态；真实 paid messages/monoforum/结算后续补 |
| channels.toggleAutotranslation | done | real-setting | 持久化 `channels.autotranslation` 并记录 admin log `toggle_autotranslation`；校验 change_info 后返回/推送 `updateChannel`，`channels.getChannels` 回填 `Channel.autotranslation` |
| channels.getMessageAuthor | done | real-partial | monoforum 专有权限未接入；按可见 channel message 精确 id 查 sender 并返回 user，找不到或非用户作者报 MESSAGE_ID_INVALID |
| channels.checkSearchPostsFlood | stub | free-bounded | 校验 query 必填且长度<=256，返回 query_is_free=true 与剩余免费次数，避免 Search Posts 付费检查阻塞；真实付费/限额模型后续接入 |
| channels.setMainProfileTab | stub | permission-ok | profile tabs 未接入；校验 change_info 后返回 BoolTrue |
| channels.getMessages | done | real-channel | 按 channel_id + InputMessageID 精确拉取 channel_messages，单次 cap=100；稀疏 id 走 `id=ANY`，不存在返回 messageEmpty |
| channels.getParticipants | done | real | 支持 recent/admins/search/kicked/banned/bots/contacts/mentions 最小过滤语义，limit cap=200、offset cap=10000；banned/kicked 对非 admin 不暴露管理列表，搜索走 user 字段匹配，避免恶意深分页拖垮 PG |
| channels.getParticipant | done | real | 返回当前/指定 user 的成员身份与权限；`inputPeerSelf` 对普通成员返回 `channelParticipantSelf`，避免 TDesktop 记录 `Got self regular participant`；缺失按 TL 错误映射 |
| channels.inviteToChannel | done | real | 校验 invite 权限与用户数量 cap，创建成员/dialog，megagroup 生成 service message；单人重复邀请返回 `USER_ALREADY_PARTICIPANT`，被踢/禁看用户只能由 creator 或具备 ban_users 的 admin 恢复，普通成员单人恢复返回 `USER_KICKED`；broadcast 不写普通成员服务消息 |
| channels.joinChannel | done | real | 创建/恢复 member/dialog；megagroup 生成 join service message；重复加入返回 `USER_ALREADY_PARTICIPANT`；join/rejoin read 水位推进到加入动作后的 top，避免旧历史计入未读或后续打开会话时批量生成旧消息读回执 |
| channels.leaveChannel | done | real | 标记 member left，更新 participants_count；megagroup 生成 leave service message |
| channels.readHistory | done | real | TDesktop 可能直接调用 channels.readHistory；语义同 messages.readHistory(InputPeerChannel)，含发送方 `updateReadChannelOutbox` 在线通知 |
| channels.deleteMessages | done | real-channel | 管理员/作者权限校验后软删单份 channel messages，生成有界 `updateDeleteChannelMessages`，`pts_count=len(ids)`，单次 id cap=1000 |
| channels.deleteHistory | done | real-partial | `for_everyone` 执行一个有界管理员删除 page 并推 `updateDeleteChannelMessages`，单批 cap=1000；该 TL 返回 `Updates` 且 TDesktop 不读取 offset，禁止在同步 RPC 内循环构造超大 id/update；非 for_everyone 只清当前用户可见历史/read/dialog，不写扩散、不生成超大 update |
| channels.editAdmin | done | real | creator 或具备 add_admins 的 admin 可更新 admin rights/rank；非 creator 只能授予自己拥有的权限；禁止改 creator；写 admin log，不占 channel pts、不写 `channel_update_events`；返回/在线推 `updateChannelParticipant + updateChannel`，离线设备通过 full channel/participants 刷新状态 |
| channels.editBanned | done | real | creator 或具备 ban_users 的 admin 可更新 banned rights/kicked 状态，刷新 participants/admin/banned/kicked 计数；写 admin log，不占 channel pts、不写 `channel_update_events`；返回/在线推 `updateChannelParticipant + updateChannel`，若后续产生可见踢人/加人 service message，则 service message 单独占 channel pts |
| channels.editTitle | done | real | creator/change_info admin 可改标题；写 megagroup service message `messageActionChatEditTitle` 与 channel pts，返回 `updateChannel + updateNewChannelMessage` |
| channels.editPhoto | done | real-photo | change_info 权限校验后解析 `inputChatPhoto`：`inputChatUploadedPhoto` 组装上传→建 Photo，`inputChatPhoto{inputPhoto}` 引用已存在照片，`inputChatPhotoEmpty`/`inputPhotoEmpty` 清除；落 `channels.photo_id/photo_dc_id/photo_stripped` 反范式列，`tgChannel.Photo`(ChatPhoto)/`tgChannelFull.ChatPhoto` 渲染真实头像，返回 `updateChannel` + 推 channel state；服务端无 Files 时按 `PHOTO_INVALID` 处理；in-history `MessageActionChatEditPhoto` service 消息留 todo |
| channels.deleteChannel | done | real | creator 权限，标记 channel deleted，返回/推送 `updateChannel + channelForbidden`，dialog 列表过滤 deleted channel；后续 admin log 细化 |
| channels.getAdminLog | done | real-minimal | 校验 creator/admin，按 channel admin log event store 有界查询；支持 actor admins、events_filter、q、max_id/min_id/limit，覆盖 metadata、成员权限、pin、send/edit/delete；forum/group_call/subscription 等长尾 action 待对应业务模型 |
| messages.updatePinnedMessage(InputPeerChannel) | done | real-channel | pin/unpin channel message，校验 pin 权限，更新 `channels.pinned_message_id`，写 `updatePinnedChannelMessages(pts_count=1)`；不生成超大 update |
| messages.unpinAllMessages(InputPeerChannel) | done | real-channel-single | TDesktop pinned section 清空入口；当前 channel 模型只维护单个 `pinned_message_id`，有置顶则复用单条 unpin 生成 `affectedHistory` 与 `updatePinnedChannelMessages`，无置顶返回当前 channel pts + `pts_count=0`；forum/monoforum 参数有界 no-op，后续多置顶模型再做分页 offset |
| stats.getBroadcastStats / stats.getMegagroupStats | stub | empty-graphs | TDesktop 频道/超级群统计页入口；校验 channel/access_hash/admin 与 broadcast/megagroup 类型后返回可解析空图表，真实统计聚合后续补 |
| stats.getMessageStats / stats.getMessagePublicForwards | stub | empty-graphs | TDesktop 消息统计/公开转发入口；校验 channel/access_hash/admin/msg_id，public forwards `limit<=100`、`offset<=128` 后返回空结果，避免 unknown RPC 与无界分页 |
| stats.loadAsyncGraph / stats.getStoryStats / stats.getStoryPublicForwards / stats.getPollStats | stub | empty/error | Layer 225 stats 域剩余入口全部显式注册；graph token 过长返回 `statsGraphError`，story/poll 校验 peer/id/limit 后返回空图表或空 public forwards |
| premium.getBoostsStatus | stub | zero-status | TDesktop 频道统计、颜色/权限和 boost 弹窗入口；校验 channel/access_hash 后返回零 boost 状态和必填 `boost_url` 空串，不写入虚假 boost 状态 |
| premium.getBoostsList / premium.getUserBoosts | stub | empty-list | TDesktop boost 列表入口；校验 channel/access_hash/admin/user，`limit<=100`、`offset<=128` 后返回空列表，真实 boost 聚合后续补 |
| premium.getMyBoosts / premium.applyBoost | stub | empty-noop | TDesktop boost 弹窗入口；`getMyBoosts` 返回空槽位，`applyBoost` 校验 slots 存在、非空、数量上限后空 no-op，真实 premium/boost 模型后续补 |
| messages.exportChatInvite(InputPeerChannel) | done | real-channel | 支持导出 channel invite link，校验 invite/change_info 权限，支持 expire/usage_limit/request_needed/title 与 legacy revoke permanent |
| messages.checkChatInvite | done | real-channel | 按 invite hash 返回 `chatInvite` / `chatInviteAlready`，处理过期、撤销、已封禁与已加入状态 |
| messages.importChatInvite | done | real-channel | 通过 invite hash 加入 channel/megagroup，megagroup 生成 join service message 与 channel pts；read 水位先落到加入前 top，再推进到自己的 join service，避免旧历史计入未读或打开会话时批量生成旧消息读回执；PG 在事务内锁 invite row 后检查/递增 usage_count，usage limit 满返回 `USERS_TOO_MUCH`，request_needed 持久化 pending importer、返回 `INVITE_REQUEST_SENT` 并向管理员推 `updatePendingJoinRequests`，用户后续通过其它 invite 加入会清理旧 pending request |
| messages.getExportedChatInvites / messages.getExportedChatInvite | done | real-invite-list | TDesktop invite links 管理页入口；校验 channel/access_hash/admin、admin user、link、limit/offset 边界后按 admin/revoked 返回持久化 invite 列表与 detail；`limit<=100`，分页使用 `offset_date + offset_link` seek，不做 OFFSET 大分页 |
| messages.editExportedChatInvite / messages.deleteExportedChatInvite / messages.deleteRevokedExportedChatInvites | done | real-invite-state | 支持 title/expire/usage_limit/request_needed 修改、永久链接 revoke 后返回 replacement、单链接删除与按 admin 清理 revoked invites；撤销链接仍可被管理页 detail 查询，import 会拒绝 revoked hash |
| messages.getAdminsWithInvites / messages.getChatInviteImporters | done | real-importers | TDesktop invite admins、importers、join request 管理入口；按 admin 聚合 active/revoked invite 数，importer 支持 requested/link/q/offset_user/offset_date 查询；`limit<=100`、link/q 长度有界，`q+link` 返回 `SEARCH_WITH_LINK_NOT_SUPPORTED` |
| messages.hideChatJoinRequest / messages.hideAllChatJoinRequests | done | real-join-request | 支持 pending join request approve/dismiss；approve 复用 channel join 流并生成 megagroup join service update，dismiss 清理 pending；响应和管理员在线 push 都包含新的 `updatePendingJoinRequests`；`hideAll` 单批 cap=1000，避免对超大 dialog 硬生成无界 updates |

## Contacts / Users

| method | status | behavior | note |
|---|---|---|---|
| contacts.getContacts | done | real | 从 contacts 表查询当前账号通讯录；支持 hash notModified；返回当前 owner 保存的姓名/电话/备注视角，并经统一 user projection 补当前头像/状态；互相关系按双方是否互存维护 |
| contacts.getContactIDs | done | real | 返回当前 owner 通讯录 user_id 列表，支持 hash notModified |
| contacts.getStatuses | done | persisted-presence | 返回当前通讯录联系人 `contactStatus`；状态来自运行时 presence、活跃 session 兜底与持久化 `users.last_seen_at`，离线联系人返回精确 `userStatusOffline.was_online`，未知用户退回 `userStatusRecently`。users/dialog/history projection 已裁剪 StatusTimestamp，`contacts.getStatuses` 的逐项 privacy 降级仍为后续项 |
| contacts.importContacts | done | real | 按手机号匹配已注册用户，写入当前 owner 视角的联系人姓名/电话/备注，返回 imported/users，并写 durable updatePeerSettings + updateContactsReset + dispatch_outbox |
| contacts.addContact | done | real | 写入当前 owner 视角联系人资料；禁止 self/空姓名/不存在 user，维护 reverse mutual，并写 durable updatePeerSettings + updateContactsReset + dispatch_outbox |
| contacts.acceptContact | done | real-share-phone | TDesktop “Share my phone number” 入口；要求当前 owner 已有该联系人，否则 CONTACT_REQ_MISSING；把当前用户手机号/姓名写入对方 owner 视角联系人，维护 mutual，返回 updatePeerSettings(shareContact=false)+updateContactsReset，并为双方写 durable peer settings / contacts reset |
| contacts.deleteContacts | done | real | 删除当前 owner 的联系人关系，并清理对方 reverse mutual；写 durable updatePeerSettings + updateContactsReset + dispatch_outbox |
| contacts.updateContactNote | done | real | 更新当前 owner 对某个联系人的备注与备注实体，不影响其它 owner 对同一 user 的备注，并写 durable updateContactsReset + dispatch_outbox |
| contacts.search | done | real | TDesktop 搜索框 peer 分支；strip `@`、空/过短查询报 SEARCH_QUERY_EMPTY/QUERY_TOO_SHORT，limit cap=50；联系人 user 进 MyResults，非联系人 user 进 Results；公开 username channel/supergroup 同步返回 PeerChannel + Chats，当前已加入的放 MyResults，其它公开命中放 Results 并以 left chat 标记只读预览，避免 TDesktop 误显示已加入；用户搜索走手机号前缀/username/姓名/owner 保存姓名索引，公开频道搜索走 username/title trgm 索引 |
| contacts.resolveUsername | done | real | 按大小写不敏感 username 解析 user 或公开 channel/supergroup peer；channel 返回 PeerChannel + Chats；不存在返回 USERNAME_NOT_OCCUPIED，非法格式返回 USERNAME_INVALID |
| contacts.resolvePhone | done | real-partial | 按手机号解析 user peer；PhoneNumber privacy 已用于资料投影与 ShareContact 例外，手机号反查入口仍按已知手机号解析，未命中返回 PHONE_NOT_OCCUPIED；按 AddedByPhone/PhoneNumber privacy 限制反查为后续项 |
| contacts.block | done | real-blocklist | 写入当前 owner blocklist，幂等；同步刷新 peer settings，story-only block flag 当前按主 blocklist 处理，完整 stories privacy 留后续 |
| contacts.unblock | done | real-blocklist | 从当前 owner blocklist 删除 peer，幂等；同步刷新 peer settings |
| contacts.getBlocked | done | real-blocklist | Settings 隐私/安全预取；`contacts.block/unblock/getBlocked` 维护 `owner_user_id + blocked_user_id` 唯一 blocklist，limit cap=100，按 date/user_id 返回 `peerBlocked` + users；`contacts.getPeerSettings` 按当前 owner block 状态返回 block/unblock action，并按 PhoneNumber privacy 决定 `ShareContact`。blocklist 仍是私聊 send/edit/delete 的拒绝 gate，完整 privacy key 用于资料投影/手机号分享 |
| contacts.getTopPeers | stub | disabled | 第一阶段不维护 top peers 统计 |
| contacts.getSponsoredPeers | stub | empty | 第一阶段不做 sponsored peers，TDesktop 搜索框分支返回 sponsoredPeersEmpty |
| users.getUsers | done | real | InputUserSelf 与已知 InputUser 返回用户（含 777000 官方账号）；未登录则跳过（空列表） |
| users.getFullUser | done | real-minimal | InputUserSelf/已知 InputUser 返回 user + 最小 userFull；空账号主界面需要 |
| users.getSavedMusic | stub | empty | TDesktop 资料页/profile music/export 路径会按 offset/limit/hash 拉取；校验 input user、offset>=0、limit<=100，当前无 profile music media store，返回 `users.savedMusic{count=0,documents=[]}` |
| users.getSavedMusicByID | stub | empty | TDesktop saved music 文件引用刷新入口；校验 input user 与 documents vector cap=100，当前返回空 `users.savedMusic` |

## Stories / Payments / AI Compose

| method | status | behavior | note |
|---|---|---|---|
| stories.getAllStories | stub | empty | 第一阶段不做 stories，返回空列表与空 stealth_mode |
| stories.getStoriesArchive | stub | empty | TDesktop 主界面后台拉 archived stories；第一阶段返回空 archive |
| stories.getPinnedStories | stub | empty | TDesktop 资料页 stories 区域后台查询；当前返回空 `stories.stories` |
| stories.getAlbums | stub | empty | TDesktop 资料页 story albums 后台查询；当前无 story album 模型，返回空 `stories.albums` |
| stories.sendReaction | stub | validated-noop | 当前无 story store；校验 peer/story_id/reaction，`add_to_recent` 会更新账号 top/recent emoji reaction，返回空 updates，避免 story 反应入口落 unknown |
| payments.getStarGiftActiveAuctions | stub | notModified | 第一阶段不做 stars/gifts |
| payments.getSavedStarGifts | stub | empty | TDesktop 资料页 gifts 区域后台查询；当前返回空 `payments.savedStarGifts` |
| payments.getSavedStarGift | stub | empty | 单个 gift 详情入口；当前返回空 `payments.savedStarGifts` |
| aicompose.getTones | stub | notModified | 第一阶段不做 AI compose tones |

## Langpack

| method | status | behavior | note |
|---|---|---|---|
| langpack.getLangPack | done | real+legacy-adapter | PG-backed；启动 seed `tdesktop_en/zh-hans_v12000000.strings`；DrKLO Android legacy `#9ab5c58e lang_code:string` 由 RPC edge 补 `lang_pack` 后复用当前实现 |
| langpack.getDifference | done | real | 按 version 返回语言包差异；当前 seed 同版本全量 |
| langpack.getStrings | done | real+legacy-adapter | 按 key 查询 PG 中的语言包字符串；DrKLO Android legacy `#2e1ee318 lang_code:string keys:Vector<string>` 由 RPC edge 补 `lang_pack`，keys 上限 512 |
| langpack.getLanguages | done | compat-static | Layer 225 `#42c6978f` 已注册；DrKLO Android 仍会发 legacy `#800fd57d` 无参版本，RPC edge 显式 adapter 后返回语言列表 |

## Files / Media / Photos

| method | status | behavior | note |
|---|---|---|---|
| upload.saveFilePart | done | real-localfs | 累积 small file 分片到 `upload_parts`（PG），part>=0、单片 ≤512KB；需已登录 |
| upload.saveBigFilePart | done | real-localfs | 累积 big file 分片，校验 total_parts 上限；组装见 uploadMedia/uploadProfilePhoto |
| upload.getFile | done | real-localfs | 把 `inputDocumentFileLocation/inputPhotoFileLocation/inputPeerPhotoFileLocation` 推导为 `file_blobs.location_key`（document 查 `doc:<id>[:type]`，photo 查 `photo:<id>:<type>`），从本地 blob backend 按 offset/limit 切片返回 `upload.file`，storage type 优先按 bytes 魔数、再按 mime 判定，避免历史 seed 把 WebP thumb 误标 `image/jpeg` 后客户端解码失败；`location_key→FileBlob` 元数据 LRU 消除每 chunk PG 查，≤256KB sticker/reaction/thumb 小 blob 走 `object_key→bytes` LRU 并由启动 `WarmCaches` 预热；CDN/legacy `inputFileLocation`/`inputStickerSetThumb` 返回 `LOCATION_INVALID`（todo）；location 仅按 `id` 解析，**不校验 `access_hash`/`file_reference`**（安全取舍见下方 note） |
| upload.getFileHashes | stub | empty | 本阶段不做 CDN/分片完整性校验，返回空 hash 列表（客户端信任数据） |
| photos.uploadProfilePhoto | done | real-photo | 组装上传分片→建头像 Photo（合成 a/c 尺寸，落 `photos`/`file_blobs`/`profile_photos(kind=profile|fallback)`），`fallback=true` 设为 fallback 当前头像，否则设为真实 profile 当前头像；返回 `photos.photo{photo, 带头像 self}` 并向其它在线 session 推 `updateUser`+self；video/emoji-markup 返回 `PHOTO_INVALID` |
| photos.updateProfilePhoto | done | real-photo | `inputPhoto` 把历史 photo 设为当前 profile/fallback；`inputPhotoEmpty` 停用对应 kind 当前头像；变更后向其它在线 session 推 `updateUser`+self 同步头像 |
| photos.uploadContactProfilePhoto | todo | personal-real/suggest-pending | `save=true + file` 设置当前 viewer 对目标 contact 的 personal photo，后续 `userProfilePhoto.personal=true`；`save=true + no file` 清除 personal photo；`suggest=true` 当前只创建/返回 photo，私聊 `messageActionSuggestProfilePhoto` 仍待 private service-message action 模型 |
| photos.getUserPhotos | done | real-photo | 只按真实 `profile_photos(kind=profile)` 返回某用户头像历史（最新在前），不混入 fallback/personal；offset/limit/max_id 有界，返回 `photos.photos[Slice]` + target user |
| photos.deletePhotos | done | real-photo | 只停用真实 profile kind 的 inputPhoto id，返回被删 id 列表；fallback/personal 不受影响 |

> 头像渲染：用户头像统一来自 `internal/app/userprojection`，顺序为 viewer personal photo > ProfilePhoto privacy 允许的真实 profile > fallback > 空头像；同时叠加联系人备注/本地手机号、PhoneNumber/StatusTimestamp privacy 裁剪，`tgUser` 输出 `userProfilePhoto.personal`。`users.getFullUser` 额外回填 `personal_photo/profile_photo/fallback_photo` 与 About privacy。channel 头像反范式 `channels.photo_*`，`tgChannel.Photo`/`tgChannelFull.ChatPhoto` 渲染。资源 seed 来自 `TELESRV_STICKER_SEED_DIR` 真实导出，启动时幂等导入（reactions/default 系统集/常规集）。

> 头像多设备同步：`uploadProfilePhoto`/`updateProfilePhoto`（含 `inputPhotoEmpty` 清除）变更后复用 `pushUserUpdates`，向该账号其它在线 session 推 `updateUser` + `Updates.users` 携带含新 `userProfilePhoto` 的 self；当前设备经 RPC 返回更新，TDesktop 经 `processUser→setPhoto→peerUpdated(Photo)` 即时刷新（与 参考实现 `MakeUpdatesByUpdatesUsers([self],updateUser)` 对齐；`updateUserName` 不含 photo 无法刷新头像）。联系人/对话方不主动广播头像（Telegram 同行为，对端下次拉取 user 时刷新）；频道头像变更经 `channels.editPhoto` → `pushChannelStateToMembers` 推在线成员。

> 安全取舍（待生产补强）：`upload.getFile` 与 `messages.sendMedia` 引用既有 `inputMediaPhoto`/`inputMediaDocument` 均只按 `id` 解析、**不校验 `access_hash`/`file_reference`**（不返回 `FILE_REFERENCE_EXPIRED`），依赖 64-bit `crypto/rand` 不可枚举 id 防越权——任意登录用户若得知某 `document`/`photo` id 即可引用/下载其内容。dev 单 DC 可接受，生产需补 owner/access_hash 校验与 file_reference 轮换。

> 媒体性能与容量债见 `performance-audit.md`「媒体管线」条目：`upload_parts` 无 GC/每用户配额（未 assemble 分片滞留 PG）、`media` JSONB 内联放大 fan-out 写、`getUserPhotos` N+1 + OFFSET 分页。`upload.getFile` 整文件入内存 + 每 chunk 查 PG 已于 2026-06-03 修：`BlobBackend.GetRange` 段读（`ReadAt`）+ `location_key→FileBlob` 元数据 LRU；sticker/reaction/thumb 首开冷路径已补 `object_key→bytes` 小 blob LRU、完整 sticker set cache 与启动预热。

## Unknown Trace

未注册 RPC 经 rpc.Router fallback 记录到日志（type_id + layer），返回 NOT_IMPLEMENTED rpc_error。
最近一次 TDesktop 联调发现的异步预取 trace 已全部转入上方各分区并实现第一阶段空响应；2026-05-31 复跑登录注册→空账号主界面后，无 `Unhandled RPC` / `NOT_IMPLEMENTED`。

| method/type_id | first_seen | raw_note |
|---|---|---|
| — | — | 当前无未实现 RPC trace |

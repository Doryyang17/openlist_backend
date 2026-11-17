# 账号视频观看历史协议 v1

使用当前登录账号，游客与禁用账号不可用。路由独立于媒体库开关。

| 方法/路径 | 请求 | 响应 data |
|---|---|---|
| GET `/api/watch_history/capabilities` | 无 | `{version:1}` |
| POST `/api/watch_history/list` | `{page,per_page}`，默认 1/20，最大每页 100 | `{content,total}` |
| POST `/api/watch_history/get` | `{path,password?}` | 当前记录或 null |
| POST `/api/watch_history/progress` | `{path,password?,position,duration,completed,revision}` | 已保存记录 |
| POST `/api/watch_history/resolve` | `{id,password?}` | 当前账号相对路径 `{path}` |

路径是账号根目录下的虚拟文件路径。查询、保存及打开校验当前目录权限、密码及对象类型；仅允许 `GetObjType == VIDEO`。名称从文件系统对象读取。列表为当前账号所有历史，超出账号根目录的项返回空 path/parent；打开时重新检查权限。

记录包含 id、name、path、parent、scope_status、position_seconds、duration_seconds、completed、revision、last_watched_at。内部表 `CustomWatchHistory` 按 user_id 和 path_key 唯一，按 user_id/last_watched_at/id 建立排序索引，兼容表前缀。last_watched_at 由服务器生成 UTC 时间。

写入必须携带读取到的 revision；新路径为 0，成功后加 1，冲突返回 code=409、reason=conflict。请求中不能指定 user_id，账号来自认证上下文。负数、非有限秒数、超大数值或缺少 revision 返回 invalid_request；进度不超过有效总时长，总时长未知按 0 处理且不标记已看完。

沿用现有响应封装：JSON code=200 表示成功；错误 data.reason 为 invalid_request、invalid_path、not_video、not_found、outside_root、password_required、forbidden、conflict、rate_limited 或 unavailable；未登录为 code=401。请求体最多 8 KiB，每账号限速 2 次/秒，突发 20 次，闲置限速器过期回收。

SQLite 写入与收藏/账号删除共用进程内锁；其他数据库使用账号行锁与条件更新。账号删除同事务清理历史。迁移只新增表，回滚程序可保留该表。文件路径变化不自动迁移历史，不保存下载直链、浏览器 token、目录密码或宿主机路径。

```sh
GOMAXPROCS=1 go test -p 1 -parallel 1 ./internal/db ./server/handles
```

SQLite 已覆盖归属、去重、原子版本冲突、主动回退、分页、删除账号与路径展示。其他数据库需在对应环境补充验收。

# 个人收藏接口与维护

收藏属于 OpenList 内置账号功能。独立表 `custom_favorites` 使用既有表前缀与数据库；新增时检查文件权限与对象，列表只读收藏表，打开时重新检查权限。无独立收藏服务、文件扫描任务或下载直链缓存。

## API v1

全部路由位于 `/api/favorites`，复用 `Auth(false)` 与 `AuthNotGuest`。账号取自认证上下文，客户端不能指定所有者。沿用 `{code,message,data}` 包装，业务错误也可能使用 HTTP 200。

| 方法/后缀        | 请求                                    | 成功 data                               |
| ---------------- | --------------------------------------- | --------------------------------------- |
| GET capabilities | 无                                      | `version:1, files:true, folders:true`   |
| POST list        | `page=1, per_page=50, q="", type="all"` | `content:[], total`                     |
| POST status      | `paths:[]`，最多 100 个                 | `[{path,id,favorited}]`，未收藏 id 为 0 |
| POST add         | `path,password?`                        | 收藏条目；重复请求保持原 id 和时间      |
| POST delete      | `id`                                    | 幂等成功；无权或不存在同样成功          |
| POST resolve     | `id,action:"open"或"parent",password?`  | `path,is_dir`，账号相对虚拟路径         |

条目字段：`id,name,is_dir,created_at,path,parent,scope_status`。名称和类型为新增时的真实对象快照。`scope_status` 为 `in_root` 或 `outside_root`；范围外 path/parent 均为 null；恰好成为账号根目录的条目 parent 为 null。前端按 id 解析目标，不能直接使用旧快照路径跳转。

列表每页最多 100，页数最多 1,000,000；q 去空白后最多 1,024 字节，按名称大小写不敏感的字面包含筛选，`%`、`_` 不作为通配符。type 为 all/file/folder；排序 created_at DESC、id DESC。首版未做语言学排序或 Unicode 归一化。

## 数据与权限

- 规范化完整 OpenList 虚拟路径以 SHA-256 辅助唯一约束 `(user_id,path_key)`，命中后比较原路径，避免数据库长路径和大小写 collation 改变路径身份。
- 唯一索引与排序索引由 GORM 按实际表名生成，包含配置的表前缀，支持同库使用不同前缀。已有固定名称索引保留，迁移补齐新索引；不删除收藏数据或旧索引。
- 禁止根目录、系统路由、穿越和非法路径。输入不含网站部署前缀；不保存宿主机物理路径、目录密码、token 或下载链接。
- 新增与删除账号按相同顺序锁用户并执行事务；删除账号同时清除收藏。数据库唯一约束兜底重复新增；取消后重加使用新 id，迟到的旧 id 删除不会影响新记录。
- SQLite 不支持行锁，进程内的新增收藏、取消收藏与删除账号共用写入串行控制，在开始事务前等待；收藏请求取消或超时可退出等待。MySQL/PostgreSQL 保留原有事务与行锁。该控制不覆盖其他业务写入或其他进程。
- list/status 无远端存储调用；add/resolve 使用既有 CanAccess 和 fs.Get。resolve 的目标与父目录分别检查；取消历史条目不要求原文件仍可访问。
- 对象改名/移动不追踪；同一路径以后换成新对象会打开新对象。临时存储错误不删除收藏。
- `data.reason`：invalid_request、invalid_path、forbidden、password_required、outside_root、not_found、favorite_not_found、unavailable。登录/游客错误遵循现有认证中间件的 code。

## 检查与 QA

```sh
GOMAXPROCS=1 go test -p 1 -tags=jsoniter ./internal/db ./server/handles -run 'TestFavorite' -count=1
python3 scripts/qa-favorites.py
```

第一条在临时 SQLite 上验证账号隔离、幂等、持久化、用户清理、1/50/51/500/5000 条分页和根目录转换；同时覆盖 DELETE/WAL 模式下重复收藏、不同路径收藏、取消收藏与删除账号的写入竞争，以及等待超时取消。迁移回归覆盖同库多前缀（含空前缀）、已有固定索引的升级、重复迁移、收藏保留与组合索引约束。第二条必须先启动独立 QA 前后端并配置 `/sandbox` 合成文件挂载；脚本只接受 127.0.0.1:5245、5173 及 `~/openlist-dev` 数据路径，使用私有 credentials 文件认证，不输出凭据。

HTTP 脚本会创建合成账号/文件/元信息，删除本次专用的生命周期测试账号，保留浏览器验收样例。重复运行会追加样例，不清理既有 QA 数据。完整 package 测试中的原有搜索用例 `outside_base_path` 在未修改上游基线亦失败，需单独处理；收藏专项通过不代表所有上游测试通过。

本轮实测 SQLite。MySQL/PostgreSQL、生产迁移与容器数据卷恢复未验收。备份整个 OpenList 数据库即可包含收藏；回退旧后端前必须另验上游迁移兼容性，不能直接删表或覆盖现有数据库。

## 同步上游

重点复查：Auth/AuthNotGuest、User.JoinPath/权限位、CanAccess/MetaCoversPath、fs.Get 错误类型、DeleteUserById、GORM 迁移与各数据库方言。即使合并没有冲突，也运行上述用例。不要用上游 build.sh 下载的官方前端覆盖二开 dist；正式镜像应固定前后端版本并分别保留构建输入。

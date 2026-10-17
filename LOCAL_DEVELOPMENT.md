# 后端二开与本地开发

本仓库为 `Doryyang17/openlist_backend`，上游为 `OpenListTeam/OpenList`。初始定制基线是官方 v4.2.7，提交 `3b28270a4066ea52943a69ecddd850d83f7e8bdc`。保留上游历史、许可证与 Go 模块路径，通过集中的功能提交维护定制能力。

## 工具链与编译

要求以 `go.mod` 为准，当前声明 `go 1.26.0` 和工具链 `go1.27.1`。Go 模块仍为 `github.com/OpenListTeam/OpenList/v4`，不因 fork 名称不同而批量重写 import。

```sh
GOTOOLCHAIN=go1.27.1 go version
GOTOOLCHAIN=go1.27.1 GOMAXPROCS=1 go mod download
GOTOOLCHAIN=go1.27.1 GOMAXPROCS=1 go mod verify
GOTOOLCHAIN=go1.27.1 GOMAXPROCS=1 go build -p 1 -mod=readonly -tags=jsoniter -o ./bin/openlist-dev .
./bin/openlist-dev version
```

直接编译生成当前主机平台的程序，不自动下载前端，也不启动服务。未注入正式版本信息时，版本显示 `dev`、构建信息显示 `unknown` 属于预期行为，源码版本以 Git 提交为准。`bin/` 产物由 `.gitignore` 排除。

不要为了初始化执行 `go get -u`。需要更新依赖时，单独审查 `go.mod` 和 `go.sum` 的差异。

## 前后端联调

建议将配套前端放在同级 `../frontend`，分别保留两个 Git 仓库。后端仓库内 `public/dist` 初始只包含上游说明文件，后端编译成功不等于定制网页已经可用。

测试实例必须使用独立端口、配置和数据目录。配套前端提供测试环境管理工具，初次使用先按照前端 `LOCAL_DEVELOPMENT.md` 完成 setup/start/seed，再在前端根目录执行：

```sh
python3 scripts/local-env.py backend-build --source ../backend
python3 scripts/local-env.py restart backend
python3 scripts/local-env.py start frontend
python3 scripts/local-env.py check
```

默认 QA 使用 `~/openlist-dev/` 数据目录、后端 `127.0.0.1:5245` 和前端 `127.0.0.1:5173`。`backend-build` 编译本仓库，备份 QA SQLite 与配置，并选择源码后端；它不会自行重启服务。后续 `restart backend` 切换测试程序，`check` 检查直连与 Vite 同源代理的登录和收藏协议。

这些目录和端口是开发工具默认值，不代表稳定实例。凭据、token、数据库、日志及实际局域网地址只留在本机；开发测试不复用稳定数据。重启后端后，内存中的登录 token 失效，需要重新登录；收藏仍保存在数据库。

## 收藏检查

```sh
GOMAXPROCS=1 go test -p 1 -mod=readonly -tags=jsoniter ./internal/db ./server/handles -run 'TestFavorite' -count=1
```

专项测试使用临时 SQLite，覆盖账号隔离、幂等、分页、根目录变化、用户清理、并发写入和表前缀迁移。完整 API 契约和测试边界见 [docs/favorites.md](docs/favorites.md)。

`python3 scripts/qa-favorites.py` 需要已经初始化的独立 QA 环境及 `/sandbox` 合成文件挂载。该脚本会创建合成账号、文件和元信息，并删除本次专用测试账号；不会清理已有 QA 数据。运行前阅读脚本和接口文档，不用于稳定实例。

完整包测试中的原有搜索用例 `TestIsSearchNodeAccessible/outside_base_path` 在未修改上游基线同样失败，应与收藏专项结果分别记录。MySQL/PostgreSQL、真实手机、生产迁移和 Linux 镜像／数据卷恢复仍需验收。

## 同步上游

将自有仓库作为 `origin`、官方仓库作为只拉取的 `upstream`。检查当前配置后再添加缺失远端；本地 `.git/config` 不会随提交传播。

```sh
git status --short --branch
git remote -v
git fetch origin
git fetch upstream
git rev-list --left-right --count HEAD...origin/main
git rev-list --left-right --count HEAD...upstream/main
```

fetch 只更新远端引用，不自动合并。按需获取目标发布 tag，不强制覆盖上游可能移动的 beta 标签。

同步官方版本时，在独立升级分支审查旧基线到目标 tag 的 diff，再显式合并。保留二开提交，不用 reset 或强制推送替代合并；即使没有文本冲突，也需复核认证、路径、权限、数据库迁移和文件接口。功能历史记录在 README，不手工维护第二份源码补丁。

## 构建与发布边界

现有 `build.sh` 会下载官方前端并覆盖 `public/dist`，部分输入使用浮动版本；`Dockerfile` 沿用该流程。发布二开镜像前必须固定前后端提交和 dist，明确嵌入或挂载方式，不能只验证后端可编译。

继承的 `beta_release.yml`、`test_docker.yml` 和 `sync_repo.yml` 已增加上游仓库条件，本 fork 的普通 main 推送不会执行其 beta 发布、镜像发布或 Gitee 同步。其他上游 tag、Release 和手动工作流仍需在使用前逐项适配，不能将继承配置视为已经可用的二开发布流程。

当前仓库没有 `.dockerignore`。未来进行 Docker 构建前需明确构建上下文，排除本机数据、备份、配置和凭据；Git 忽略规则不会自动成为 Docker 忽略规则。

启动、发布与回退均使用明确的目标实例。生产数据库迁移、Local 存储映射、容器权限、外置卷就绪及恢复流程需要单独验收；源码更新不表示已完成运行环境升级。

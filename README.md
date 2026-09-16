# Valheim 管理平台

仿照饥荒管理平台 DMP 的交互方式，使用 Golang 实现的 Valheim 专用服务器管理面板。生产环境只需要：

```text
run.sh
bin/valheim-panel
```

当前版本：`1.0.5`

版本规则：每次修改后补丁号 `+0.0.1`。项目根目录的 `VERSION` 是唯一版本源，也可以运行：

```bash
./scripts/bump-version.sh
```

## 功能

- DMP 风格后台：状态胶囊、左侧菜单、实例卡片、控制按钮、模组表格
- 多实例创建、编辑、删除、启动、停止、重启
- SteamCMD App `896660` 安装、更新和校验
- 面板内一键下载、安装和修复 SteamCMD
- BepInExPack_Valheim 自动安装
- Thunderstore 搜索、依赖解析、模组安装、启停、删除
- 热门模组推荐栏目
- 本地 ZIP 模组上传
- `BepInEx/config` 配置在线编辑
- 存档管理和 BepInEx 配置备份、恢复、下载
- PBKDF2 密码、HMAC JWT 会话、密码修改
- 演示模式

## Linux 部署

Linux 发布包只包含脚本、Linux 二进制和示例环境文件，不安装 Go。

```bash
tar -xzf valheim-panel-linux-1.0.5.tar.gz
cd valheim-panel
chmod +x run.sh bin/valheim-panel
./run.sh doctor
./run.sh start
```

访问：

```text
http://服务器IP:8787
```

首次登录：

```text
用户名：admin
密码：admin123
```

登录后立即在“平台管理 / 账户安全”修改密码。

## Linux 脚本

```bash
./run.sh start
./run.sh stop
./run.sh restart
./run.sh status
./run.sh doctor
./run.sh logs
./run.sh firewall
./run.sh systemd-install
./run.sh systemd-remove
```

常用环境变量：

```bash
PORT=8787 DATA_DIR=data GAME_PORT=2456 ./run.sh start
```

systemd 服务默认读取项目目录下的 `panel.env`：

```bash
cp panel.env.example panel.env
```

可以在这里覆盖管理员密码和 Thunderstore 社区。

## SteamCMD

Linux 脚本不会自动安装 Go，也不会强制安装 SteamCMD。启动面板后进入：

```text
平台管理 -> 安装 SteamCMD -> 安装 / 修复
```

面板会自动下载并初始化 SteamCMD。

## 防火墙

```bash
./run.sh firewall
```

默认放行：

```text
TCP 8787
UDP 2456-2457
```

## 手动编译

只有修改源码时才需要 Go：

```bash
./scripts/build-linux.sh
```

输出：

```text
bin/valheim-panel
```

## 开发与测试

```bash
go fmt ./...
go test ./...
go vet ./...
```

## 数据目录

```text
valheim-panel/
├── bin/valheim-panel
├── data/
│   ├── panel.json
│   ├── steamcmd/
│   └── instances/<instance-id>/
├── panel.env.example
└── run.sh
```

迁移时把整个 `data/` 一起复制。

## 主要 API

```text
POST   /api/auth/login
PUT    /api/auth/password
GET    /api/overview
POST   /api/platform/steamcmd/install
GET    /api/instances
POST   /api/instances
POST   /api/instances/:id/install
POST   /api/instances/:id/action
GET    /api/mods/search
GET    /api/mods/recommended
POST   /api/instances/:id/mods/install
GET    /api/instances/:id/backups
POST   /api/instances/:id/backups
```

# Valheim Panel Linux 部署包

这个包只包含：

```text
run.sh
bin/valheim-panel
panel.env.example
VERSION
```

不需要安装 Go，不需要 Node.js，不需要 npm。

## 启动

```bash
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
admin / admin123
```

## 安装 systemd

```bash
./run.sh systemd-install
```

服务名：

```text
valheim-panel
```

## 放行端口

```bash
./run.sh firewall
```

默认放行：

```text
TCP 8787
UDP 2456-2457
```

## 安装 SteamCMD

Linux 脚本不会自动安装 Go。启动面板后进入：

```text
平台管理 -> 安装 SteamCMD -> 安装 / 修复
```

面板会自动下载并初始化 SteamCMD。

## 常用命令

```bash
./run.sh start
./run.sh stop
./run.sh restart
./run.sh status
./run.sh doctor
./run.sh logs
./run.sh systemd-install
./run.sh systemd-remove
./run.sh firewall
```

# Portlight

使用 Go + Wails 重写的轻量 SSH 工作台，目标是替换 Electron，同时保留现有核心功能：

- SSH 连接与会话管理
- 远程目录浏览
- 上传本地文件到远程目录
- 下载远程文件到本地
- PDF、图片、HTML、文本/CSV/TSV 预览
- 远程命令执行
- 应用内保存 SSH 配置

## 开发

前端依赖安装：

```bash
cd frontend
npm install
```

前端打包：

```bash
cd frontend
npm run build
```

桌面启动：

```bash
go run .
```

浏览器调试模式：

```bash
go run . -http-dev
```

默认监听地址：

- HTTP 开发模式: `http://127.0.0.1:4173`

## Windows 打包

安装 Wails CLI：

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@v2.11.0
```

构建 Windows 可执行文件和 NSIS 安装包：

```bash
wails build -platform windows/amd64 -nsis
```

默认输出：

- `build/bin/Portlight.exe`
- `build/bin/Portlight-amd64-installer.exe`

## 发布

推送形如 `v0.1.0` 的 tag 会触发 GitHub Actions，自动构建 Windows 版本并把产物上传到 GitHub Release。

## 数据目录

新版本默认使用：

```text
~/.portlight/profiles.json
```

如果检测到旧版 `~/.remote-viewer/profiles.json`，会自动读取其中的已保存连接配置。

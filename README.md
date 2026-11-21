# ApiTool — API 调试工具

Postman 风格的桌面 API 调试工具，中文界面。用 [Viva](https://github.com/vivaui/viva) 框架（Go，GPU 渲染，无 WebView）构建，单文件、无依赖、启动即用。

## 功能

- 多请求**标签页**：每个标签独立保留 URL/参数/头/体/响应，切换不丢状态，支持并发发送；工作区随应用持久化，重启恢复
- **认证助手**：Basic / Bearer / API Key，自动合成请求头（可与手输头覆盖并存）
- 参数表 ↔ URL **双向同步**，中文友好的 URL 编码
- 请求体实时 **JSON 校验**（行列级错误定位）与格式化（Ctrl+B）
- **环境变量**（多套环境，`{{var}}` 全局替换，含请求体）
- 收藏**子分组**（嵌套、拖拽排序/移动、重命名级联）、集合**导出/导入**
- 集合**运行器** + 轻量**断言**（状态码 / 包含 / JSON 路径），通过率统计
- 历史按日分组、删除/清空
- 响应 JSON 树形渲染、响应头表格、复制为 cURL/Go/JavaScript/Python、cURL 导入
- 6 套主题（浅色 / 深色 / 暖纸 / 深海 / 墨绿 / 暗紫）

## 快捷键

`Ctrl+Enter` 发送 · `Ctrl+S` 保存到收藏 · `Ctrl+T` 新标签 · `Ctrl+W` 关标签 · `Ctrl+N` 新请求 · `Ctrl+B` 格式化 JSON

## 构建

需要 Go 1.27+。仓库自带补丁版 Viva 框架（`third_party/viva`），克隆后无需额外步骤：

```sh
go build .                      # 直接构建（控制台子系统）
go test ./...                   # 全部测试
```

Windows 推荐用 `build.cmd`（GUI 子系统 + 图标资源）：

```sh
build.cmd                       # 产出 apitool.exe
```

`viva.toml` 支持 `go tool viva build` / `go tool viva dev`（热重载）。

## 三平台发布

推送 `v*` 标签后，GitHub Actions 自动构建并发布：

- `apitool-windows-amd64-setup.exe`（Windows 安装器：自选目录、快捷方式、可从系统设置卸载）
- `apitool-windows-amd64.exe` / `.zip`（免安装单文件）
- `apitool-linux-amd64` / `.tar.gz`
- `apitool-darwin-arm64` / `apitool-darwin-amd64`（macOS 需要 CGO，由 macOS runner 构建）

手动触发：Actions 页面 → Release → Run workflow。

## 数据

配置与数据存储在 `%AppData%/apitool/data.json`（收藏、历史、环境、打开的标签、主题）。

## 结构

- `main.go` — 页面与模型（标签、收藏树、运行器、主题）
- `client.go` — HTTP 客户端（变量替换、认证头、multipart）
- `asserts.go` — 断言引擎（状态码 / 包含 / JSON 路径）
- `jsonview.go` — 响应 JSON 树
- `themes.go` — 主题预设
- `tools/patchicon` — Windows 资源图标补丁
- `third_party/viva` — Viva 框架（含本项目的补丁，随仓库分发）

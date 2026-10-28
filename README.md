# study class

支持西工商课程学习的 Windows x64 客户端。解压后通过一个启动菜单完成配置、检查、运行和停止。

**[下载 Windows v1.0.11](https://github.com/polar400/study-class/releases/download/v1.0.11/study-class-v1.0.11-windows-amd64.zip)** · [最新版本](https://github.com/polar400/study-class/releases/latest) · [完整使用教程](docs/使用教程.md)

免费使用和修改，禁止收费销售、收费下载、收费授权、收费代操作及变相收费。使用前请阅读 [许可条款](LICENSE.md) 和 [免责声明](docs/免责声明.md)。

## 三步开始

1. 下载 `study-class-v1.0.11-windows-amd64.zip`，完整解压到自己有写入权限的文件夹。普通使用者不需要安装 Go，也不要把 GitHub 自动生成的“Source code”源码存档当成运行包。
2. 用记事本编辑解压目录的 `config.json`，把 `username`、`password` 改为本人或已获明确授权的学号和密码，按 UTF-8 保存。其他字段先保留默认值。
3. 双击 `启动.bat`，先选择“检查”，检查无误后选择“前台运行”。之后可按需要选择“后台运行”“打开状态页”或“停止”。

详细步骤、单账号与多账号示例、字段说明和常见问题见 [使用教程](docs/使用教程.md)。下载包内还附带可离线打开的 `docs/使用教程.html`，启动菜单提供“打开教程”。

## 下载后看到什么

```text
study-class-v1.0.11-windows-amd64/
├─ 启动.bat        检查、运行、后台运行、状态页、停止、预览和帮助
├─ config.json     填写自己的账号配置
├─ app/            程序及运行数据
└─ docs/           离线教程、许可、免责声明和来源说明
```

运行后日志、会话缓存和状态记录位于 `app/logs`、`app/state`、`app/reports`。不要公开自己的配置、缓存、日志或备份。

## 使用规则

- 当前适配站点为 `https://bxait.haiqikeji.com`；其他平台不保证兼容。
- 支持多账号及多课程任务；每门课程按目录顺序，从第一个未完成的视频继续。当前视频只有得到平台回读确认后才进入下一节，失败暂停该课程。
- “当前进度”来自平台记录；“计时进度”是独立的本地时间展示。计时到 100% 仍等待平台确认，不能作为平台保存成功或课程完成的证明。
- 普通 INFO 标签为蓝色，平台确认或已完成跳过为绿色，失败与暂停的 ERROR 标签为红色；文件日志和重定向输出保持纯文本。
- 状态页默认是 [http://127.0.0.1:10087](http://127.0.0.1:10087)，只在运行程序的本机使用。前台窗口关闭会中断该进程，退出前建议使用“停止”。
- 本程序使用后台 HTTP 会话，不解码或播放视频。是否认可学习记录由平台决定；不保证学时、作业、考试、成绩或结课结果。

## 源码与构建

仓库按用途归类：`src/` 放源码、测试及嵌入 Logo，`docs/` 放教程和来源说明，`scripts/` 放启动与维护脚本。

开发构建需要 Windows 和 Go 1.26 或更高版本。在仓库根目录可执行：

```powershell
go test ./...
go vet ./...
.\scripts\构建.bat
```

构建脚本生成 `dist/study-class-v1.0.11-windows-amd64/` 成品目录。直接构建时，目标 Go 包是 `./src`；例如先创建 `app/` 后执行 `go build -trimpath -o app/study-class.exe ./src`，并配合统一入口和根层配置使用。

`src/polar-logo.txt` 通过 `go:embed` 编入 EXE，构建时须与源码一起保留。维护备份会包含源码子目录，最多保留最近 5 份，只留本机。

## 作者署名与致谢

启动画面的 **POLAR 字符 Logo 是作者署名，原样保留**；项目显示名称为 **study class**，Logo 下方显示本项目 GitHub 仓库网址，便于核对发布来源。

本项目参考了 [aoaostar/mooc](https://github.com/aoaostar/mooc) 的公开资料、代码与设计思路。感谢原项目作者 **Pluto / aoaostar** 及贡献者；原作者主页：[aoaostar](https://github.com/aoaostar)。这不是原项目作者的官方发布，也不代表学校或平台授权。

字体与第三方来源见 [THIRD_PARTY_NOTICES.md](docs/THIRD_PARTY_NOTICES.md)。本项目的自定义许可不替代第三方作品各自的许可。

# 发版镜像离线打包 (Offline release image packaging)

把当前代码打成一个**自包含、可离线部署的 Docker 镜像 tar**,传到线上服务器 `docker load` 即可运行。每次发版用这一条命令。

> 面向:维护者 / 自动化 agent。这是本仓库约定的**离线发版**流程(区别于 `deploy/DOCKER.md` 里描述的 Docker Hub 拉取方式)。

## 一条命令

```bash
# 在仓库根目录,当前 git 分支/commit 就是要发的版本
deploy/package_release.sh
```

脚本做了什么(见 `deploy/package_release.sh`):

1. 用根 `Dockerfile` 构建 **release 镜像**(多阶段:前端 `vite build` → `-tags embed` 嵌入 Go 二进制 → alpine 运行时),强制 `--platform linux/amd64`。
2. 按 `<分支>-<短commit>` 打 tag,例如 `sub2api:feature-online-c07d43ef`,同时打 `sub2api:latest`。
3. `docker save` 导出到 **`deploy/sub2api-<分支>-<短commit>-linux-amd64.tar`**(命名约定固定)。
4. 校验 tar 自包含(含镜像索引),打印部署指令。

### 常用变体

```bash
deploy/package_release.sh --skip-build      # 复用已构建的 sub2api:latest,只重新打包(秒级)
VERSION=1.2.3 deploy/package_release.sh      # 覆盖内嵌版本号(默认取 backend/scripts/resolve-version.sh)
GOPROXY=direct deploy/package_release.sh     # 不走国内 goproxy 镜像
```

## 目标机部署

把 tar 传到服务器后:

```bash
docker load -i sub2api-<分支>-<短commit>-linux-amd64.tar
docker tag  sub2api:<分支>-<短commit> sub2api:latest      # compose 默认用 :latest
cd deploy && docker compose up -d
```

## 注意事项

- **先提交再打包**:镜像 `COPY` 的是当前**工作树**(含未提交改动),但文件名只标 commit。工作树不干净时脚本会告警——发版前先把改动提交,保证镜像与该 commit 一致。
- **产物不入 git**:`deploy/.gitignore` 已忽略 `*.tar` / `*.tar.gz` / `*.tgz`,几十 MB 的镜像包不会误提交。
- **架构固定 amd64**:脚本强制 `--platform linux/amd64`,在 ARM 机器(如 Apple Silicon)上也会交叉构建出线上通用的 amd64 镜像。
- **大小**:`docker save` 导出的是压缩层,tar 约 ~40MB;`docker load` 后镜像约 ~170MB(含嵌入前端)。
- `deploy/build_image.sh` 是只构建 `sub2api:latest`(不导出 tar)的精简版;完整发版打包用本脚本。

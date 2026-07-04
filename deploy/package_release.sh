#!/usr/bin/env bash
# =============================================================================
# 发版镜像离线打包 (Release image → offline .tar)
# =============================================================================
# 每次发版跑这一个脚本即可:
#   构建 release 镜像(embed 前端 + release 后端)
#     → 按 <分支>-<短commit> 打 tag
#     → docker save 成可离线部署的 .tar 放到 deploy/
#     → 校验 tar 自包含可 load
#
# 用法:
#   deploy/package_release.sh                 # 构建 + 打包(默认)
#   deploy/package_release.sh --skip-build    # 复用已有的 sub2api:latest,只打包
#   VERSION=1.2.3 deploy/package_release.sh    # 覆盖内嵌版本号(默认取 resolve-version.sh)
#
# 环境变量(可选):
#   VERSION   内嵌版本号,默认由 backend/scripts/resolve-version.sh 解析
#   GOPROXY   Go 代理,默认 https://goproxy.cn,direct(国内加速)
#   GOSUMDB   默认 sum.golang.google.cn
#
# 产物:  deploy/sub2api-<分支>-<短commit>-linux-amd64.tar
#         (已被 deploy/.gitignore 忽略,不会误入 git)
#
# 目标机部署:
#   docker load -i sub2api-<分支>-<短commit>-linux-amd64.tar
#   docker tag  sub2api:<分支>-<短commit> sub2api:latest   # compose 默认用 :latest
#   (cd deploy && docker compose up -d)
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"

PLATFORM="linux/amd64"                                   # 发版统一 amd64,ARM 机器上也强制交叉构建
GOPROXY_ARG="${GOPROXY:-https://goproxy.cn,direct}"
GOSUMDB_ARG="${GOSUMDB:-sum.golang.google.cn}"

BRANCH="$(git rev-parse --abbrev-ref HEAD | tr '/' '-')"  # feature/online -> feature-online
COMMIT="$(git rev-parse --short=8 HEAD)"
TAG="sub2api:${BRANCH}-${COMMIT}"
TARBALL="deploy/sub2api-${BRANCH}-${COMMIT}-linux-amd64.tar"

SKIP_BUILD=0
[ "${1:-}" = "--skip-build" ] && SKIP_BUILD=1

# 工作树不干净时告警:镜像 COPY 的是当前工作树(含未提交改动),文件名却只标了 commit。
if [ -n "$(git status --porcelain)" ]; then
  echo "⚠️  工作树有未提交改动 —— 打出的镜像会包含它们,但文件名只标了 ${COMMIT}。" >&2
  echo "    发版前建议先提交,保证镜像内容与 ${COMMIT} 一致。" >&2
fi

if [ "${SKIP_BUILD}" -eq 0 ]; then
  echo "==> 构建 release 镜像 ${TAG} (embed 前端 + release 后端, ${PLATFORM})"
  docker build \
    --platform "${PLATFORM}" \
    -t "${TAG}" -t "sub2api:latest" \
    --build-arg GOPROXY="${GOPROXY_ARG}" \
    --build-arg GOSUMDB="${GOSUMDB_ARG}" \
    ${VERSION:+--build-arg VERSION="${VERSION}"} \
    -f "${REPO_ROOT}/Dockerfile" \
    "${REPO_ROOT}"
else
  echo "==> 跳过构建,复用现有 sub2api:latest 并打 tag ${TAG}"
  docker tag sub2api:latest "${TAG}"
fi

echo "==> 导出镜像到 ${TARBALL}"
docker save "${TAG}" -o "${TARBALL}"

# 完整性校验:确认是一个含镜像索引的归档(index.json / manifest.json)。
if ! tar tf "${TARBALL}" 2>/dev/null | grep -qE '(^|/)(index\.json|manifest\.json)$'; then
  echo "❌ 导出的 tar 缺少 index.json/manifest.json,可能不完整" >&2
  exit 1
fi

SIZE="$(du -h "${TARBALL}" | cut -f1)"
echo ""
echo "✅ 打包完成"
echo "   产物 : ${TARBALL}  (${SIZE})"
echo "   镜像 : ${TAG}  (= sub2api:latest)"
echo ""
echo "目标机部署:"
echo "   docker load -i $(basename "${TARBALL}")"
echo "   docker tag ${TAG} sub2api:latest"
echo "   (cd deploy && docker compose up -d)"

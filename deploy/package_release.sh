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

# 工作树不干净时告警。仓库既有的 images/ 不在 Dockerfile COPY 范围内,可安全忽略。
TRACKED_DIRTY=0
if ! git diff --quiet || ! git diff --cached --quiet; then
  TRACKED_DIRTY=1
fi
UNTRACKED_OUTSIDE_IMAGES="$(git ls-files --others --exclude-standard | grep -vE '^images(/|$)' || true)"
if [ "${TRACKED_DIRTY}" -ne 0 ] || [ -n "${UNTRACKED_OUTSIDE_IMAGES}" ]; then
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

# 完整性校验:两个 OCI/Docker 索引入口都必须存在。
TAR_CONTENTS="$(tar tf "${TARBALL}" 2>/dev/null)" || {
  echo "❌ 无法读取导出的 tar,可能不完整" >&2
  exit 1
}
for REQUIRED_ENTRY in index.json manifest.json; do
  if ! printf '%s\n' "${TAR_CONTENTS}" | grep -qE "(^|/)${REQUIRED_ENTRY}$"; then
    echo "❌ 导出的 tar 缺少 ${REQUIRED_ENTRY},可能不完整" >&2
    exit 1
  fi
done

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

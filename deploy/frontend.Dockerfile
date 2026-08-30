# Multi-stage build for the frontend.
#
# One difference from the upstream Dockerfile: corepack enables the pnpm version
# pinned in package.json instead of `npm i -g pnpm`. The latter fails on
# linux/arm64 with
#   Cannot verify the identity of the @pnpm/exe.linux-arm64 native binary:
#   it is missing from pnpm-lock.yaml
# Upstream CI runs on amd64 through pnpm/action-setup and never hits it.
#
# The build produces dist/ (static files) plus dist/server.js, the Fastify
# gateway. That gateway serves the static files and reverse-proxies /api to the
# backend; the frontend SDK requests relative paths, which is why the frontend
# and API have to share an origin, and why only one port is exposed.

FROM node:23-alpine AS builder
WORKDIR /app

ARG BUILD_MODE=launcher

RUN corepack enable
COPY package.json pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile

COPY . .
RUN pnpm run build --mode=${BUILD_MODE}

FROM node:23-alpine
WORKDIR /app
ENV NODE_ENV=production

RUN corepack enable
COPY --from=builder /app/dist ./dist
COPY --from=builder /app/package.json ./package.json
COPY --from=builder /app/pnpm-lock.yaml ./pnpm-lock.yaml
COPY --from=builder /app/pnpm-workspace.yaml ./pnpm-workspace.yaml

RUN pnpm install --prod --frozen-lockfile --ignore-scripts

EXPOSE 80
CMD ["node", "dist/server.js"]

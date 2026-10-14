FROM node:24-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci

FROM node:24-alpine AS build
WORKDIR /app
ENV NEXT_TELEMETRY_DISABLED=1
COPY --from=deps /app/node_modules ./node_modules
COPY . .
RUN npm run build

FROM node:24-alpine AS runtime
WORKDIR /app
ENV NODE_ENV=production \
    NEXT_TELEMETRY_DISABLED=1 \
    PORT=3000 \
    HOSTNAME=0.0.0.0
RUN addgroup -S -g 1001 aether && adduser -S -u 1001 -G aether aether
COPY --from=build --chown=aether:aether /app/public ./public
COPY --from=build --chown=aether:aether /app/.next/standalone ./
COPY --from=build --chown=aether:aether /app/.next/static ./.next/static
USER aether
EXPOSE 3000
CMD ["node", "server.js"]

FROM node:24-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci

FROM node:24-alpine AS builder
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .
ARG NEXT_PUBLIC_USE_MOCK_AUTH=false
ARG API_PROXY_TARGET
ARG NEXT_PUBLIC_API_BASE_URL
ARG NEXT_PUBLIC_API_URL
ARG IDENTITY_ACCESS_URL
ARG BATCH_WORKFLOW_URL
ARG CLAIM_LOGISTICS_URL
ARG CUSTODY_VERIFICATION_URL
ENV NEXT_PUBLIC_USE_MOCK_AUTH=$NEXT_PUBLIC_USE_MOCK_AUTH
ENV API_PROXY_TARGET=$API_PROXY_TARGET
ENV NEXT_PUBLIC_API_BASE_URL=$NEXT_PUBLIC_API_BASE_URL
ENV NEXT_PUBLIC_API_URL=$NEXT_PUBLIC_API_URL
ENV IDENTITY_ACCESS_URL=$IDENTITY_ACCESS_URL
ENV BATCH_WORKFLOW_URL=$BATCH_WORKFLOW_URL
ENV CLAIM_LOGISTICS_URL=$CLAIM_LOGISTICS_URL
ENV CUSTODY_VERIFICATION_URL=$CUSTODY_VERIFICATION_URL
ENV NEXT_TELEMETRY_DISABLED=1
RUN npm run build

FROM node:24-alpine AS runner
WORKDIR /app
ENV NODE_ENV=production
ENV HOSTNAME=0.0.0.0
ENV PORT=3000
ENV NEXT_TELEMETRY_DISABLED=1
RUN addgroup -S nodejs && adduser -S nextjs -G nodejs
COPY --from=builder --chown=nextjs:nodejs /app/public ./public
COPY --from=builder --chown=nextjs:nodejs /app/.next ./.next
COPY --from=builder --chown=nextjs:nodejs /app/node_modules ./node_modules
COPY --from=builder --chown=nextjs:nodejs /app/package.json ./package.json
COPY --from=builder --chown=nextjs:nodejs /app/next.config.ts ./next.config.ts
USER nextjs
EXPOSE 3000
CMD ["npm", "run", "start"]

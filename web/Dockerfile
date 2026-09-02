# The read-only dashboard. It renders on the server and talks to the ReLab API,
# so the browser never holds a credential and there is no client-side fetching.
#
# With no RELAB_API_URL it serves the recorded demo instead, which is why this
# image is useful on its own.

FROM node:22-alpine AS build
WORKDIR /app
COPY package.json package-lock.json* ./
RUN npm ci --ignore-scripts
COPY . .
ENV NEXT_TELEMETRY_DISABLED=1
# The standalone build traces the modules actually imported and copies just
# those, which is a fraction of node_modules.
ENV RELAB_STANDALONE=1
RUN npm run build

FROM node:22-alpine
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 PORT=3000 HOSTNAME=0.0.0.0
COPY --from=build /app/.next/standalone ./
COPY --from=build /app/.next/static ./.next/static
# Non-root: the dashboard reads an HTTP API and needs nothing else.
USER node
EXPOSE 3000
CMD ["node", "server.js"]

# Frontend image: builds the app, then serves it with nginx, which also forwards /api to the backend.
# Build context: frontend-react/
FROM node:24-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM nginx:1.29-alpine
# Resolve the backend through Docker's DNS each time (not once at start), so a restarted backend is found again.
ENV NGINX_ENTRYPOINT_LOCAL_RESOLVERS=1
COPY docker/default.conf.template /etc/nginx/templates/default.conf.template
COPY docker/app.conf /etc/nginx/app.conf
COPY docker/security-headers.conf /etc/nginx/security-headers.conf
COPY --from=build /app/dist /usr/share/nginx/html

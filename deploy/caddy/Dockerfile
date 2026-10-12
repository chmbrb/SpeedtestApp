# HTTPS front door. Build context: deploy/caddy/. (Baked in rather than bind-mounted, because SELinux hosts refuse bind mounts.)
FROM caddy:2.10-alpine
COPY Caddyfile /etc/caddy/Caddyfile
COPY entrypoint.sh /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
CMD ["caddy", "run", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"]

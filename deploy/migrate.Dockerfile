# Applies the database schema. Build context: the repository root (only sql/postgres and deploy/migrate.sh are sent).
FROM postgres:17-alpine
COPY sql/postgres /migrations
COPY deploy/migrate.sh /migrate.sh
USER postgres
CMD ["sh", "/migrate.sh"]

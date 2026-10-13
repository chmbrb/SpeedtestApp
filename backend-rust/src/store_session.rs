//! The key that signs login sessions; see sql/postgres/009_session_key.sql. Ports backend-go/internal/store/session.go.

use sqlx::Row;

use crate::store::Store;

impl Store {
    /// The stored key, or None when none has been created yet.
    pub async fn session_key(&self) -> Result<Option<Vec<u8>>, sqlx::Error> {
        sqlx::query("SELECT key FROM session_key").fetch_optional(&self.pool).await?.map(|r| r.try_get(0)).transpose()
    }

    /// Stores the key unless one exists already (two servers starting together must end up with the same one).
    pub async fn save_session_key(&self, key: &[u8]) -> Result<(), sqlx::Error> {
        sqlx::query("INSERT INTO session_key (key) VALUES ($1) ON CONFLICT DO NOTHING").bind(key).execute(&self.pool).await.map(|_| ())
    }
}

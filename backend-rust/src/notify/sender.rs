//! Delivering a message, and sending one message through several channels.

use std::sync::Arc;

use async_trait::async_trait;

/// What gets sent. `priority` is ntfy's 1 (min) to 5 (max); 3 is the default.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Message {
    pub title: String,
    pub body: String,
    pub priority: u8,
    pub tags: Vec<String>,
}

#[derive(Debug, Clone, PartialEq)]
pub enum SendError {
    /// Nobody is listening on this channel (for example no browser has turned on push). Neither a delivery nor a failure.
    NoRecipients,
    Failed(String),
}

impl std::fmt::Display for SendError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            SendError::NoRecipients => f.write_str("no recipients"),
            SendError::Failed(m) => f.write_str(m),
        }
    }
}

/// Delivers a message. The real ones talk to ntfy and to browsers; tests use a fake.
#[async_trait]
pub trait Sender: Send + Sync {
    async fn send(&self, m: &Message) -> Result<(), SendError>;
}

/// Sends each message through every sender. The message counts as delivered when at least one channel delivered it, so an alert
/// is not repeated on the channels that worked just because another one is down. It fails only when nothing was delivered; the
/// error then names every channel that failed.
pub struct Fanout(pub Vec<Arc<dyn Sender>>);

#[async_trait]
impl Sender for Fanout {
    async fn send(&self, m: &Message) -> Result<(), SendError> {
        let (mut delivered, mut errors) = (0, Vec::new());
        for s in &self.0 {
            match s.send(m).await {
                Ok(()) => delivered += 1,
                Err(SendError::NoRecipients) => {}
                Err(SendError::Failed(e)) => errors.push(e),
            }
        }
        match (delivered, errors.is_empty()) {
            (n, _) if n > 0 => Ok(()),
            (_, false) => Err(SendError::Failed(errors.join("; "))),
            _ => Err(SendError::NoRecipients),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicUsize, Ordering};

    struct Stub(Result<(), SendError>, AtomicUsize);
    #[async_trait]
    impl Sender for Stub {
        async fn send(&self, _: &Message) -> Result<(), SendError> {
            self.1.fetch_add(1, Ordering::SeqCst);
            self.0.clone()
        }
    }

    #[tokio::test]
    async fn fanout_rules() {
        let boom = || SendError::Failed("ntfy is down".into());
        let down = || SendError::Failed("push is down".into());
        type Case = (&'static str, Vec<Result<(), SendError>>, Result<(), SendError>);
        let cases: Vec<Case> = vec![
            ("all delivered", vec![Ok(()), Ok(())], Ok(())),
            ("one channel is enough", vec![Err(boom()), Ok(())], Ok(())),
            ("nobody on the other channel", vec![Ok(()), Err(SendError::NoRecipients)], Ok(())),
            ("both fail", vec![Err(boom()), Err(down())], Err(SendError::Failed("ntfy is down; push is down".into()))),
            (
                "one fails, the other has nobody",
                vec![Err(boom()), Err(SendError::NoRecipients)],
                Err(SendError::Failed("ntfy is down".into())),
            ),
            ("nobody anywhere", vec![Err(SendError::NoRecipients), Err(SendError::NoRecipients)], Err(SendError::NoRecipients)),
        ];
        for (name, results, want) in cases {
            let stubs: Vec<Arc<Stub>> = results.into_iter().map(|r| Arc::new(Stub(r, AtomicUsize::new(0)))).collect();
            let fan = Fanout(stubs.iter().map(|s| s.clone() as Arc<dyn Sender>).collect());
            assert_eq!(fan.send(&Message::default()).await, want, "{name}");
            assert!(stubs.iter().all(|s| s.1.load(Ordering::SeqCst) == 1), "{name}: every channel is tried once");
        }
    }
}

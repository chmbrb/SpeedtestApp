//! Fans a test's live events out to any number of listeners. Ports backend-go/internal/ondemand/hub.go.

use std::{
    collections::HashMap,
    sync::Mutex,
    time::{Duration, Instant},
};

use tokio::sync::mpsc;

/// One server-sent event. `id` increases within a test so a reconnecting browser can resume.
#[derive(Debug, Clone, PartialEq)]
pub struct Event {
    pub id: i64,
    /// state | progress | result | error (equals the "type" field in `data`).
    pub name: &'static str,
    pub data: String,
}

const REPLAY_LIMIT: usize = 400; // events kept per test; a whole Ookla run is about 150
const SUB_BUFFER: usize = 64; // events a slow reader may fall behind before it is dropped (it can reconnect and resume)
const KEEP_FINISHED: Duration = Duration::from_secs(600); // how long a finished test's events stay available to late listeners

struct Stream {
    events: Vec<Event>,
    next: i64,
    subs: Vec<mpsc::Sender<Event>>,
    closed_at: Option<Instant>,
}

impl Stream {
    fn new() -> Self {
        Self { events: Vec::new(), next: 1, subs: Vec::new(), closed_at: None }
    }
}

#[derive(Default)]
pub struct Hub {
    streams: Mutex<HashMap<String, Stream>>,
}

/// What a listener gets: the events it missed, then live ones.
pub struct Subscription {
    pub replay: Vec<Event>,
    /// None when the stream is already over (there will be no live events).
    pub live: Option<mpsc::Receiver<Event>>,
    /// False when this process has no record of the test (it ended before a restart, or never ran here).
    pub exists: bool,
    pub closed: bool,
}

impl Hub {
    pub fn new() -> Self {
        Self::default()
    }

    fn lock(&self) -> std::sync::MutexGuard<'_, HashMap<String, Stream>> {
        self.streams.lock().unwrap_or_else(|e| e.into_inner())
    }

    /// Records an event and delivers it to current listeners. Events for a closed stream are ignored.
    pub fn publish(&self, id: &str, name: &'static str, data: &impl serde::Serialize) {
        let Ok(data) = serde_json::to_string(data) else { return };
        let mut streams = self.lock();
        let st = streams.entry(id.to_string()).or_insert_with(Stream::new);
        if st.closed_at.is_some() {
            return;
        }
        let ev = Event { id: st.next, name, data };
        st.next += 1;
        st.events.push(ev.clone());
        if st.events.len() > REPLAY_LIMIT {
            let excess = st.events.len() - REPLAY_LIMIT;
            st.events.drain(..excess);
        }
        // A listener too far behind (or gone) is dropped; a browser reconnects with Last-Event-ID and catches up from the buffer.
        st.subs.retain(|tx| tx.try_send(ev.clone()).is_ok());
    }

    /// Ends a test's stream: listeners' channels end after what they have already been sent.
    pub fn close(&self, id: &str) {
        let mut streams = self.lock();
        let st = streams.entry(id.to_string()).or_insert_with(Stream::new);
        if st.closed_at.is_none() {
            st.closed_at = Some(Instant::now());
            st.subs.clear(); // dropping the senders ends the receivers
        }
    }

    /// The events after `last_id` and a channel for new ones.
    pub fn subscribe(&self, id: &str, last_id: i64) -> Subscription {
        let mut streams = self.lock();
        let Some(st) = streams.get_mut(id) else {
            return Subscription { replay: Vec::new(), live: None, exists: false, closed: false };
        };
        let replay = st.events.iter().filter(|e| e.id > last_id).cloned().collect();
        let closed = st.closed_at.is_some();
        let live = (!closed).then(|| {
            let (tx, rx) = mpsc::channel(SUB_BUFFER);
            st.subs.push(tx);
            rx
        });
        Subscription { replay, live, exists: true, closed }
    }

    /// Drops streams that finished long ago.
    pub fn forget(&self) {
        self.lock().retain(|_, st| st.closed_at.is_none_or(|t| t.elapsed() <= KEEP_FINISHED));
    }

    #[cfg(test)]
    fn age_closed(&self, id: &str, by: Duration) {
        if let Some(st) = self.lock().get_mut(id) {
            st.closed_at = st.closed_at.map(|t| t.checked_sub(by).unwrap_or(t));
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn ids(evs: &[Event]) -> Vec<i64> {
        evs.iter().map(|e| e.id).collect()
    }

    #[tokio::test]
    async fn a_late_listener_gets_what_it_missed_then_live_events() {
        let hub = Hub::new();
        for i in 0..3 {
            hub.publish("t", "progress", &json!({"n": i}));
        }
        let mut sub = hub.subscribe("t", 1);
        assert!(sub.exists && !sub.closed);
        assert_eq!(ids(&sub.replay), [2, 3]);
        hub.publish("t", "progress", &json!({"n": 3}));
        let live = sub.live.as_mut().unwrap().recv().await.unwrap();
        assert_eq!((live.id, live.name, live.data.as_str()), (4, "progress", r#"{"n":3}"#));
        assert_eq!(ids(&hub.subscribe("t", 0).replay), [1, 2, 3, 4]);
        assert!(hub.subscribe("t", 99).replay.is_empty());
    }

    #[tokio::test]
    async fn every_listener_gets_every_event() {
        let hub = Hub::new();
        hub.publish("t", "state", &json!({}));
        let mut a = hub.subscribe("t", 0);
        let mut b = hub.subscribe("t", 0);
        hub.publish("t", "progress", &json!({}));
        assert_eq!(a.live.as_mut().unwrap().recv().await.unwrap().id, 2);
        assert_eq!(b.live.as_mut().unwrap().recv().await.unwrap().id, 2);
    }

    #[tokio::test]
    async fn closing_ends_listeners_after_what_they_were_sent() {
        let hub = Hub::new();
        hub.publish("t", "state", &json!({}));
        let mut sub = hub.subscribe("t", 0);
        hub.publish("t", "result", &json!({}));
        hub.close("t");
        hub.publish("t", "progress", &json!({})); // ignored: the stream is over
        let rx = sub.live.as_mut().unwrap();
        assert_eq!(rx.recv().await.unwrap().name, "result");
        assert!(rx.recv().await.is_none());
        let late = hub.subscribe("t", 0);
        assert!(late.closed && late.live.is_none());
        assert_eq!(ids(&late.replay), [1, 2]);
    }

    #[tokio::test]
    async fn a_listener_that_falls_too_far_behind_is_dropped_and_can_resume() {
        let hub = Hub::new();
        hub.publish("t", "state", &json!({})); // a test always has its first state published before anyone listens
        let mut slow = hub.subscribe("t", 0);
        for i in 0..(SUB_BUFFER + 5) {
            hub.publish("t", "progress", &json!({"n": i}));
        }
        let rx = slow.live.as_mut().unwrap();
        let mut got = Vec::new();
        while let Some(e) = rx.recv().await {
            got.push(e.id);
        }
        assert_eq!(got.len(), SUB_BUFFER, "it received what fitted, then its channel ended");
        let last = *got.last().unwrap();
        // resuming from the last id it saw gets the rest from the buffer
        assert_eq!(ids(&hub.subscribe("t", last).replay), ((last + 1)..=(SUB_BUFFER as i64 + 6)).collect::<Vec<_>>());
    }

    #[tokio::test]
    async fn only_the_latest_events_are_kept() {
        let hub = Hub::new();
        for i in 0..(REPLAY_LIMIT + 10) {
            hub.publish("t", "progress", &json!({"n": i}));
        }
        let replay = hub.subscribe("t", 0).replay;
        assert_eq!(replay.len(), REPLAY_LIMIT);
        assert_eq!(replay[0].id, 11);
    }

    #[test]
    fn unknown_tests_and_old_streams() {
        let hub = Hub::new();
        let s = hub.subscribe("never", 0);
        assert!(!s.exists && s.live.is_none() && s.replay.is_empty());
        hub.publish("done", "state", &json!({}));
        hub.close("done");
        hub.publish("running", "state", &json!({}));
        hub.forget();
        assert!(hub.subscribe("done", 0).exists, "a stream that just finished is kept");
        hub.age_closed("done", KEEP_FINISHED + Duration::from_secs(1));
        hub.forget();
        assert!(!hub.subscribe("done", 0).exists, "an old finished stream is dropped");
        assert!(hub.subscribe("running", 0).exists, "a running one never is");
    }
}

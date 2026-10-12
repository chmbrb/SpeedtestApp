//! The two cryptographic parts of Web Push: signing who is sending (VAPID, RFC 8292) and encrypting what is sent so that only the
//! subscriber's browser can read it (RFC 8291, with the aes128gcm content coding of RFC 8188). Key formats match the Go
//! backend's, so the two can share one database.

use aes_gcm::{Aes128Gcm, KeyInit, Nonce, aead::Aead};
use base64::{Engine, engine::general_purpose::URL_SAFE_NO_PAD};
use hkdf::Hkdf;
use p256::{
    PublicKey,
    ecdh::EphemeralSecret,
    ecdsa::{Signature, SigningKey, signature::Signer},
    elliptic_curve::{rand_core::OsRng, sec1::ToEncodedPoint},
};
use sha2::Sha256;

/// One record of 4096 bytes: the largest message is that minus the header, the tag and the padding delimiter.
const RECORD_SIZE: u32 = 4096;
pub const MAX_PAYLOAD: usize = 3993;

pub fn b64(bytes: &[u8]) -> String {
    URL_SAFE_NO_PAD.encode(bytes)
}

pub fn unb64(s: &str) -> Option<Vec<u8>> {
    URL_SAFE_NO_PAD.decode(s.trim_end_matches('=')).ok()
}

/// The server's identity towards push services.
pub struct Vapid {
    signing: SigningKey,
    pub public_b64: String,
}

/// A new key pair, as (public, private): the public key as the 65-byte uncompressed point, the private key as the raw 32-byte
/// scalar, both base64url without padding (what the Go backend stores).
pub fn generate_keys() -> (String, String) {
    let sk = SigningKey::random(&mut OsRng);
    let public = sk.verifying_key().to_encoded_point(false);
    (b64(public.as_bytes()), b64(&sk.to_bytes()))
}

impl Vapid {
    pub fn from_keys(public_b64: &str, private_b64: &str) -> Result<Self, String> {
        let raw = unb64(private_b64).ok_or("the stored push private key is not base64url")?;
        let signing = SigningKey::from_slice(&raw).map_err(|_| "the stored push private key is not a P-256 key")?;
        let derived = b64(signing.verifying_key().to_encoded_point(false).as_bytes());
        if derived != public_b64 {
            return Err("the stored push public key does not belong to the private key".into());
        }
        Ok(Self { signing, public_b64: derived })
    }

    /// The `Authorization` header value for a push service at `endpoint`.
    pub fn authorization(&self, endpoint: &str, subject: &str, now_unix: u64) -> Result<String, String> {
        let url = url::Url::parse(endpoint).map_err(|e| e.to_string())?;
        let audience = format!(
            "{}://{}",
            url.scheme(),
            url.host_str().map(|h| url.port().map_or(h.to_string(), |p| format!("{h}:{p}"))).ok_or("no host")?
        );
        let sub = if subject.starts_with("https://") || subject.starts_with("mailto:") {
            subject.to_string()
        } else {
            format!("mailto:{subject}")
        };
        let header = b64(br#"{"typ":"JWT","alg":"ES256"}"#);
        let claims = b64(serde_json::json!({"aud": audience, "exp": now_unix + 12 * 3600, "sub": sub}).to_string().as_bytes());
        let signing_input = format!("{header}.{claims}");
        let sig: Signature = self.signing.sign(signing_input.as_bytes());
        Ok(format!("vapid t={signing_input}.{}, k={}", b64(&sig.to_bytes()), self.public_b64))
    }
}

/// Encrypts `payload` for a subscription whose browser keys are `p256dh` (65-byte point) and `auth` (16 bytes): a single
/// aes128gcm record, ready to be the body of the request to the push service.
pub fn encrypt(payload: &[u8], p256dh: &[u8], auth: &[u8]) -> Result<Vec<u8>, String> {
    if payload.len() > MAX_PAYLOAD {
        return Err("the message is too large to send".into());
    }
    let ua_public = PublicKey::from_sec1_bytes(p256dh).map_err(|_| "the browser's public key is not a valid P-256 point")?;
    let secret = EphemeralSecret::random(&mut OsRng);
    let as_public = secret.public_key().to_encoded_point(false);
    let shared = secret.diffie_hellman(&ua_public);

    // RFC 8291 section 3.4: mix the shared secret with the subscription's auth secret, naming both public keys
    let mut key_info = b"WebPush: info\0".to_vec();
    key_info.extend_from_slice(p256dh);
    key_info.extend_from_slice(as_public.as_bytes());
    let mut ikm = [0u8; 32];
    Hkdf::<Sha256>::new(Some(auth), shared.raw_secret_bytes()).expand(&key_info, &mut ikm).map_err(|e| e.to_string())?;

    // RFC 8188: a content-encryption key and a nonce from a random salt
    let mut salt = [0u8; 16];
    getrandom::fill(&mut salt).map_err(|e| e.to_string())?;
    let hk = Hkdf::<Sha256>::new(Some(&salt), &ikm);
    let (mut cek, mut nonce) = ([0u8; 16], [0u8; 12]);
    hk.expand(b"Content-Encoding: aes128gcm\0", &mut cek).map_err(|e| e.to_string())?;
    hk.expand(b"Content-Encoding: nonce\0", &mut nonce).map_err(|e| e.to_string())?;

    let mut plaintext = payload.to_vec();
    plaintext.push(0x02); // the last record ends with this delimiter
    let ciphertext = Aes128Gcm::new_from_slice(&cek)
        .map_err(|e| e.to_string())?
        .encrypt(Nonce::from_slice(&nonce), plaintext.as_ref())
        .map_err(|_| "encryption failed")?;

    let mut body = Vec::with_capacity(86 + ciphertext.len());
    body.extend_from_slice(&salt);
    body.extend_from_slice(&RECORD_SIZE.to_be_bytes());
    body.push(as_public.len() as u8);
    body.extend_from_slice(as_public.as_bytes());
    body.extend_from_slice(&ciphertext);
    Ok(body)
}

#[doc(hidden)]
pub mod testing {
    //! A browser's side of Web Push, for tests: it owns the subscription keys and can read what a push service relays. Public only
    //! so the integration tests can use it; not part of the API.
    use super::*;
    use p256::{
        SecretKey,
        ecdsa::{VerifyingKey, signature::Verifier},
    };

    pub struct Browser {
        secret: SecretKey,
        pub auth: [u8; 16],
    }

    impl Default for Browser {
        fn default() -> Self {
            Self::new()
        }
    }

    impl Browser {
        pub fn new() -> Self {
            let mut auth = [0u8; 16];
            getrandom::fill(&mut auth).unwrap();
            Self { secret: SecretKey::random(&mut OsRng), auth }
        }
        pub fn p256dh(&self) -> Vec<u8> {
            self.secret.public_key().to_encoded_point(false).as_bytes().to_vec()
        }
        pub fn p256dh_b64(&self) -> String {
            b64(&self.p256dh())
        }
        pub fn auth_b64(&self) -> String {
            b64(&self.auth)
        }

        /// RFC 8291 section 3.4 from the receiving side.
        pub fn decrypt(&self, body: &[u8]) -> Vec<u8> {
            assert!(body.len() > 21, "body too short");
            let (salt, rs, idlen) = (&body[..16], u32::from_be_bytes(body[16..20].try_into().unwrap()), body[20] as usize);
            let (key_id, ciphertext) = (&body[21..21 + idlen], &body[21 + idlen..]);
            assert!(ciphertext.len() as u32 <= rs, "more than one record");
            let as_public = PublicKey::from_sec1_bytes(key_id).unwrap();
            let shared = p256::ecdh::diffie_hellman(self.secret.to_nonzero_scalar(), as_public.as_affine());
            let mut key_info = b"WebPush: info\0".to_vec();
            key_info.extend_from_slice(&self.p256dh());
            key_info.extend_from_slice(key_id);
            let mut ikm = [0u8; 32];
            Hkdf::<Sha256>::new(Some(&self.auth), shared.raw_secret_bytes()).expand(&key_info, &mut ikm).unwrap();
            let hk = Hkdf::<Sha256>::new(Some(salt), &ikm);
            let (mut cek, mut nonce) = ([0u8; 16], [0u8; 12]);
            hk.expand(b"Content-Encoding: aes128gcm\0", &mut cek).unwrap();
            hk.expand(b"Content-Encoding: nonce\0", &mut nonce).unwrap();
            let mut pt =
                Aes128Gcm::new_from_slice(&cek).unwrap().decrypt(Nonce::from_slice(&nonce), ciphertext).expect("could not decrypt");
            while pt.last() == Some(&0) {
                pt.pop();
            }
            assert_eq!(pt.pop(), Some(2), "missing padding delimiter");
            pt
        }
    }

    /// Checks a VAPID `Authorization` value and returns its claims.
    pub fn verify_vapid(header: &str, public_b64: &str) -> serde_json::Value {
        let rest = header.strip_prefix("vapid ").expect("not a vapid header");
        let (t, k) = rest.split_once(", ").expect("t and k");
        assert_eq!(k, format!("k={public_b64}"));
        let jwt = t.strip_prefix("t=").unwrap();
        let (signing_input, sig) = jwt.rsplit_once('.').unwrap();
        let vk = VerifyingKey::from_sec1_bytes(&unb64(public_b64).unwrap()).unwrap();
        let sig = Signature::from_slice(&unb64(sig).unwrap()).expect("a 64-byte r||s signature");
        vk.verify(signing_input.as_bytes(), &sig).expect("the token does not verify");
        let (h, c) = signing_input.split_once('.').unwrap();
        assert_eq!(
            serde_json::from_slice::<serde_json::Value>(&unb64(h).unwrap()).unwrap(),
            serde_json::json!({"typ":"JWT","alg":"ES256"})
        );
        serde_json::from_slice(&unb64(c).unwrap()).unwrap()
    }
}

#[cfg(test)]
mod tests {
    use super::{testing::*, *};

    #[test]
    fn keys_round_trip_and_have_the_expected_shape() {
        let (public, private) = generate_keys();
        assert_eq!(unb64(&public).unwrap().len(), 65);
        assert_eq!(unb64(&public).unwrap()[0], 4);
        assert_eq!(unb64(&private).unwrap().len(), 32);
        let v = Vapid::from_keys(&public, &private).unwrap();
        assert_eq!(v.public_b64, public);
        // a public key that does not belong to the private one is refused
        let (other_public, _) = generate_keys();
        assert!(Vapid::from_keys(&other_public, &private).is_err());
        assert!(Vapid::from_keys(&public, "!!!").is_err());
        assert!(Vapid::from_keys(&public, &b64(&[1u8; 5])).is_err());
    }

    #[test]
    fn the_vapid_token_is_signed_for_the_push_services_origin() {
        let (public, private) = generate_keys();
        let v = Vapid::from_keys(&public, &private).unwrap();
        let header = v.authorization("https://fcm.googleapis.com/fcm/send/abc", "https://speedtest.example.org", 1_700_000_000).unwrap();
        let claims = verify_vapid(&header, &public);
        assert_eq!(
            claims,
            serde_json::json!({"aud": "https://fcm.googleapis.com", "exp": 1_700_043_200u64, "sub": "https://speedtest.example.org"})
        );
        // an email address gets "mailto:", and a port stays in the audience
        let claims = verify_vapid(&v.authorization("https://127.0.0.1:8443/x", "me@example.org", 0).unwrap(), &public);
        assert_eq!(
            (claims["aud"].clone(), claims["sub"].clone()),
            (serde_json::json!("https://127.0.0.1:8443"), serde_json::json!("mailto:me@example.org"))
        );
        assert_eq!(verify_vapid(&v.authorization("https://a/b", "mailto:x@y.z", 0).unwrap(), &public)["sub"], "mailto:x@y.z");
    }

    #[test]
    fn what_is_encrypted_the_browser_can_read_and_nobody_else() {
        let browser = Browser::new();
        for payload in [&b"hello"[..], &b""[..], &[b'x'; MAX_PAYLOAD][..], "Zażółć gęślą jaźń".as_bytes()] {
            let body = encrypt(payload, &browser.p256dh(), &browser.auth).unwrap();
            assert_eq!(browser.decrypt(&body), payload);
            assert_eq!(body[20], 65, "the sender's public key is in the header");
        }
        let body = encrypt(b"secret", &browser.p256dh(), &browser.auth).unwrap();
        let other = Browser::new();
        let wrong = std::panic::catch_unwind(|| other.decrypt(&body));
        assert!(wrong.is_err(), "another browser must not be able to read it");
        // each message is encrypted afresh (new salt, new sender key)
        assert_ne!(encrypt(b"secret", &browser.p256dh(), &browser.auth).unwrap(), body);
    }

    #[test]
    fn bad_inputs_are_refused() {
        let browser = Browser::new();
        assert!(encrypt(&vec![0u8; MAX_PAYLOAD + 1], &browser.p256dh(), &browser.auth).is_err());
        let mut not_on_the_curve = vec![4u8];
        not_on_the_curve.extend([7u8; 64]);
        assert!(encrypt(b"x", &not_on_the_curve, &browser.auth).is_err());
        assert!(encrypt(b"x", &[1, 2, 3], &browser.auth).is_err());
    }
}

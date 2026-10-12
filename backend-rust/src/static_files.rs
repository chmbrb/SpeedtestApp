//! Serves the built single-page app (the frontend's `dist` folder) for every GET that is not under /api/.
//! Same rules as backend-go/internal/api/static.go: hashed assets are cached for a year, everything else (the page shell, the
//! service worker, the manifest) must be revalidated, and a missing file is a real 404 rather than the HTML shell.

use axum::{
    body::Body,
    http::{HeaderValue, Method, StatusCode, header},
    response::Response,
};
use std::path::{Component, Path, PathBuf};

fn content_type(path: &Path) -> &'static str {
    match path.extension().and_then(|e| e.to_str()).unwrap_or("").to_ascii_lowercase().as_str() {
        "html" => "text/html; charset=utf-8",
        "js" | "mjs" => "text/javascript; charset=utf-8",
        "css" => "text/css; charset=utf-8",
        "json" | "map" => "application/json",
        "webmanifest" => "application/manifest+json",
        "svg" => "image/svg+xml",
        "png" => "image/png",
        "jpg" | "jpeg" => "image/jpeg",
        "webp" => "image/webp",
        "ico" => "image/x-icon",
        "woff2" => "font/woff2",
        "woff" => "font/woff",
        "txt" => "text/plain; charset=utf-8",
        _ => "application/octet-stream",
    }
}

/// The file a URL path names inside `root`, or None if it tries to leave it.
fn resolve(root: &Path, url_path: &str) -> Option<PathBuf> {
    let mut out = root.to_path_buf();
    for part in Path::new(url_path.trim_start_matches('/')).components() {
        match part {
            Component::Normal(p) => out.push(p),
            Component::CurDir => {}
            _ => return None, // "..", a root or a drive prefix
        }
    }
    Some(out)
}

pub async fn serve(root: &Path, method: &Method, url_path: &str) -> Response {
    let name = url_path.trim_matches('/');
    let is_asset = name.starts_with("assets/");
    let ext = Path::new(name).extension().and_then(|e| e.to_str()).unwrap_or("");

    if !name.is_empty() {
        if let Some(file) = resolve(root, name) {
            if let Ok(meta) = tokio::fs::metadata(&file).await {
                if meta.is_file() {
                    let cache = if is_asset { "public, max-age=31536000, immutable" } else { "no-cache" };
                    return file_response(&file, method, cache).await;
                }
            }
        }
        if is_asset || (!ext.is_empty() && ext != "html") {
            return plain(StatusCode::NOT_FOUND, "404 page not found\n");
        }
    }
    file_response(&root.join("index.html"), method, "no-cache").await
}

async fn file_response(file: &Path, method: &Method, cache: &'static str) -> Response {
    match tokio::fs::read(file).await {
        Ok(bytes) => {
            let len = bytes.len();
            let body = if method == Method::HEAD { Body::empty() } else { Body::from(bytes) };
            let mut r = Response::new(body);
            let h = r.headers_mut();
            h.insert(header::CONTENT_TYPE, HeaderValue::from_static(content_type(file)));
            h.insert(header::CACHE_CONTROL, HeaderValue::from_static(cache));
            h.insert(header::CONTENT_LENGTH, HeaderValue::from(len));
            r
        }
        Err(_) => plain(StatusCode::NOT_FOUND, "404 page not found\n"),
    }
}

fn plain(status: StatusCode, text: &'static str) -> Response {
    let mut r = Response::new(Body::from(text));
    *r.status_mut() = status;
    r.headers_mut().insert(header::CONTENT_TYPE, HeaderValue::from_static("text/plain; charset=utf-8"));
    r
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn paths_cannot_leave_the_folder() {
        let root = Path::new("/srv/app");
        assert_eq!(resolve(root, "/assets/a.js"), Some(PathBuf::from("/srv/app/assets/a.js")));
        assert_eq!(resolve(root, "/../etc/passwd"), None);
        assert_eq!(resolve(root, "/a/../../x"), None);
        assert_eq!(resolve(root, "//etc/passwd"), Some(PathBuf::from("/srv/app/etc/passwd")));
    }

    #[test]
    fn manifest_has_its_own_type() {
        assert_eq!(content_type(Path::new("manifest.webmanifest")), "application/manifest+json");
        assert!(content_type(Path::new("sw.js")).starts_with("text/javascript"));
    }
}

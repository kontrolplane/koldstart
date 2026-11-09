//! The http side of the quote function: POST /quotes of the stage's http api, answered with a
//! quote, or a problem as rfc 9457 describes one.

use lambda_http::http::{Method, StatusCode};
use lambda_http::{Body, Error, Request, RequestExt, Response, tracing};
use serde::Serialize;

use crate::rates::{Parcel, RateCard, quote};

/// An error as rfc 9457 describes one: what went wrong, in words the caller can act on.
#[derive(Serialize)]
struct Problem<'a> {
    #[serde(rename = "type")]
    kind: &'a str,
    status: u16,
    title: &'a str,
    detail: String,
}

/// Answers a request, with the request's id in `x-request-id`, and logs one line of it.
pub async fn handle(card: &impl RateCard, request: Request) -> Result<Response<Body>, Error> {
    let request_id = request
        .lambda_context_ref()
        .map(|context| context.request_id.clone())
        .unwrap_or_default();
    let mut response = answer(
        card,
        request.method(),
        request.uri().path(),
        request.body().as_ref(),
    )?;
    response
        .headers_mut()
        .insert("x-request-id", request_id.parse()?);
    tracing::info!(
        method = %request.method(),
        path = request.uri().path(),
        status = response.status().as_u16(),
        "answered a request"
    );
    Ok(response)
}

/// What a request of method to path, with body, is answered with.
pub fn answer(
    card: &impl RateCard,
    method: &Method,
    path: &str,
    body: &[u8],
) -> Result<Response<Body>, Error> {
    if path != "/quotes" {
        return problem(
            StatusCode::NOT_FOUND,
            "no such route",
            format!("quotes are at POST /quotes, not {path}"),
        );
    }
    if method != Method::POST {
        let mut response = problem(
            StatusCode::METHOD_NOT_ALLOWED,
            "the method is not allowed",
            "send a POST".to_string(),
        )?;
        response.headers_mut().insert("allow", "POST".parse()?);
        return Ok(response);
    }
    let parcel: Parcel = match serde_json::from_slice(body) {
        Ok(parcel) => parcel,
        Err(error) => {
            return problem(
                StatusCode::BAD_REQUEST,
                "the parcel cannot be read",
                format!(
                    r#"send {{"destination": "DE", "weightGrams": 1200, "service": "standard" or "express"}}: {error}"#
                ),
            );
        }
    };
    match quote(card, &parcel) {
        Ok(quoted) => {
            tracing::debug!(destination = %quoted.destination, price_cents = quoted.price_cents, "quoted a parcel");
            json(
                StatusCode::OK,
                "application/json",
                serde_json::to_string(&quoted)?,
            )
        }
        Err(refusal) => problem(
            StatusCode::UNPROCESSABLE_ENTITY,
            "the parcel cannot be sent",
            refusal.to_string(),
        ),
    }
}

/// A problem of status, with its title and detail.
fn problem(status: StatusCode, title: &str, detail: String) -> Result<Response<Body>, Error> {
    let body = serde_json::to_string(&Problem {
        kind: "about:blank",
        status: status.as_u16(),
        title,
        detail,
    })?;
    json(status, "application/problem+json", body)
}

/// A response of status with body, and the headers every answer has.
fn json(status: StatusCode, content_type: &str, body: String) -> Result<Response<Body>, Error> {
    Ok(Response::builder()
        .status(status)
        .header("content-type", content_type)
        .header("cache-control", "no-store")
        .header("x-content-type-options", "nosniff")
        .body(Body::from(body))?)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::rates::{Rate, Service, Zone};

    /// A card that sends anywhere at one price, so the tests are about the http alone.
    struct FakeCard;

    impl RateCard for FakeCard {
        fn zone(&self, country: &str) -> Option<Zone> {
            (country != "ZZ").then_some(Zone::Europe)
        }

        fn rate(&self, _zone: Zone, _service: Service) -> Option<Rate> {
            Some(Rate {
                base_cents: 1000,
                per_kilogram_cents: 100,
                days: 2,
            })
        }
    }

    /// The body of a response, as text.
    fn text(response: &Response<Body>) -> String {
        String::from_utf8(response.body().as_ref().to_vec()).expect("a body of utf-8")
    }

    /// Each request is answered with its status and content type, and every answer is neither
    /// cached nor sniffed.
    #[test]
    fn answers_each_request() {
        let examples = [
            (
                Method::POST,
                "/quotes",
                r#"{"destination": "DE", "weightGrams": 1200}"#,
                200,
                "application/json",
            ),
            (
                Method::POST,
                "/quotes",
                r#"{"destination": "de", "weightGrams": 1200, "service": "express"}"#,
                200,
                "application/json",
            ),
            (
                Method::POST,
                "/quotes",
                r#"{"destination": "ZZ", "weightGrams": 1200}"#,
                422,
                "application/problem+json",
            ),
            (
                Method::POST,
                "/quotes",
                r#"{"destination": "DE", "weightGrams": 0}"#,
                422,
                "application/problem+json",
            ),
            (
                Method::POST,
                "/quotes",
                r#"{"destination": "DE"}"#,
                400,
                "application/problem+json",
            ),
            (
                Method::POST,
                "/quotes",
                r#"{"destination": "DE", "weightGrams": 1200, "insured": true}"#,
                400,
                "application/problem+json",
            ),
            (
                Method::POST,
                "/quotes",
                "a parcel",
                400,
                "application/problem+json",
            ),
            (Method::GET, "/quotes", "", 405, "application/problem+json"),
            (
                Method::POST,
                "/orders",
                "{}",
                404,
                "application/problem+json",
            ),
        ];
        for (method, path, body, status, content_type) in examples {
            let response = answer(&FakeCard, &method, path, body.as_bytes()).expect("an answer");
            assert_eq!(response.status().as_u16(), status, "{method} {path} {body}");
            assert_eq!(
                response.headers()["content-type"],
                content_type,
                "{method} {path} {body}"
            );
            assert_eq!(response.headers()["cache-control"], "no-store");
            assert_eq!(response.headers()["x-content-type-options"], "nosniff");
        }
    }

    /// A quote answers with its price, zone and days, from the card.
    #[test]
    fn answers_with_the_quote() {
        let response = answer(
            &FakeCard,
            &Method::POST,
            "/quotes",
            br#"{"destination": "DE", "weightGrams": 1200}"#,
        )
        .expect("an answer");
        assert_eq!(
            text(&response),
            r#"{"destination":"DE","zone":"europe","service":"standard","priceCents":1200,"currency":"EUR","days":2}"#
        );
    }

    /// A method that is not allowed names the one that is.
    #[test]
    fn names_the_method_allowed() {
        let response = answer(&FakeCard, &Method::DELETE, "/quotes", b"").expect("an answer");
        assert_eq!(response.headers()["allow"], "POST");
    }
}

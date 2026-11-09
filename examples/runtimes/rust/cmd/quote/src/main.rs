//! The quote function: what a parcel costs to send, on POST /quotes of the stage's http api.

mod handler;
mod rates;

use lambda_http::{Error, run, service_fn, tracing};

/// Starts the function: logs at the level lambda sets as AWS_LAMBDA_LOG_LEVEL, as json when the
/// function's logFormat is json, and answers each request with the shop's published rates.
#[tokio::main(flavor = "current_thread")]
async fn main() -> Result<(), Error> {
    tracing::init_default_subscriber();
    let card = rates::Published;
    run(service_fn(|request| handler::handle(&card, request))).await
}
